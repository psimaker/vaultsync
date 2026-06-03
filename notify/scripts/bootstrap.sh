#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
NOTIFY_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
ENV_FILE="$NOTIFY_DIR/.env"
EXAMPLE_ENV_FILE="$NOTIFY_DIR/.env.example"

info() {
	printf '%s\n' "$*"
}

warn() {
	printf 'WARN: %s\n' "$*" >&2
}

fail() {
	printf 'ERROR: %s\n' "$*" >&2
	exit 1
}

prompt_default() {
	label="$1"
	default_value="$2"
	printf '%s [%s]: ' "$label" "$default_value" >&2
	IFS= read -r value || true
	if [ -z "$value" ]; then
		value="$default_value"
	fi
	printf '%s' "$value"
}

prompt_yes_no() {
	label="$1"
	default_answer="$2" # y or n

	if [ "$default_answer" = "y" ]; then
		suffix='[Y/n]'
	else
		suffix='[y/N]'
	fi

	while :; do
		printf '%s %s: ' "$label" "$suffix" >&2
		IFS= read -r answer || true
		if [ -z "$answer" ]; then
			answer="$default_answer"
		fi
		case "$answer" in
			y|Y|yes|YES) return 0 ;;
			n|N|no|NO) return 1 ;;
			*) warn "Please answer yes or no." ;;
		esac
	done
}

prompt_secret_required() {
	label="$1"
	while :; do
		printf '%s: ' "$label" >&2
		stty -echo
		IFS= read -r value || true
		stty echo
		printf '\n' >&2
		if [ -n "$value" ]; then
			printf '%s' "$value"
			return 0
		fi
		warn "Value cannot be empty."
	done
}

extract_api_key() {
	config_path="$1"
	awk '
		/<apikey>/ {
			line = $0
			sub(/^.*<apikey>[[:space:]]*/, "", line)
			sub(/[[:space:]]*<\/apikey>.*$/, "", line)
			if (length(line) > 0) {
				print line
				exit
			}
		}
	' "$config_path"
}

extract_gui_address() {
	config_path="$1"
	# Scope to the <gui> element. A top-level <device><address>dynamic</address>
	# appears before <gui> in document order, so a naive "first <address>" scan
	# wrongly returns "dynamic" and builds http://dynamic. Only read <address>
	# once we are inside <gui>…</gui> (matches the Go encoding/xml parser, which
	# resolves configuration>gui>address precisely).
	awk '
		/<gui[ >]/ { in_gui = 1 }
		in_gui && /<address>/ {
			line = $0
			sub(/^.*<address>[[:space:]]*/, "", line)
			sub(/[[:space:]]*<\/address>.*$/, "", line)
			if (length(line) > 0) {
				print line
				exit
			}
		}
		/<\/gui>/ { in_gui = 0 }
	' "$config_path"
}

extract_gui_tls() {
	config_path="$1"
	awk '
		/<gui[[:space:]][^>]*>/ {
			if ($0 ~ /tls="true"/) {
				print "true"
			} else {
				print "false"
			}
			exit
		}
	' "$config_path"
}

infer_api_url() {
	address="$1"
	tls_enabled="$2"

	scheme="http"
	if [ "$tls_enabled" = "true" ]; then
		scheme="https"
	fi

	if [ -z "$address" ]; then
		printf '%s://localhost:8384\n' "$scheme"
		return 0
	fi

	case "$address" in
		http://*|https://*)
			printf '%s\n' "$address"
			return 0
			;;
	esac

	case "$address" in
		0.0.0.0:*)
			address="localhost:${address#*:}"
			;;
		:*)
			address="localhost$address"
			;;
		'[::]':*)
			address="localhost:${address#'[::]:'}"
			;;
	esac

	printf '%s://%s\n' "$scheme" "$address"
}

find_syncthing_config() {
	if [ -n "${SYNCTHING_CONFIG:-}" ] && [ -f "${SYNCTHING_CONFIG:-}" ]; then
		printf '%s\n' "$SYNCTHING_CONFIG"
		return 0
	fi

	# Probe order mirrors the Go binary's candidate list: Syncthing 1.27+ moved the
	# default to ~/.local/state (state) before the legacy ~/.config, and the
	# container paths include the official image (/var/syncthing/config) and
	# linuxserver (/config).
	for candidate in \
		"$HOME/.local/state/syncthing/config.xml" \
		"$HOME/.config/syncthing/config.xml" \
		"$HOME/Library/Application Support/Syncthing/config.xml" \
		"/var/syncthing/config/config.xml" \
		"/config/config.xml" \
		"/var/syncthing/config.xml" \
		"/var/lib/syncthing/config.xml" \
		"/etc/syncthing/config.xml"; do
		if [ -f "$candidate" ]; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done

	return 1
}

run_curl_validations() {
	syncthing_url="$1"
	syncthing_key="$2"
	relay_url="$3"

	status_body=$(mktemp)
	health_body=$(mktemp)
	trap 'rm -f "$status_body" "$health_body"' EXIT

	if ! curl --silent --show-error --fail --retry 3 --retry-delay 1 --max-time 8 \
		-H "X-API-Key: $syncthing_key" \
		"$syncthing_url/rest/system/status" >"$status_body"; then
		fail "Syncthing API validation failed. Check SYNCTHING_API_URL and SYNCTHING_API_KEY."
	fi

	device_id=$(tr -d '\n' <"$status_body" | sed -n 's/.*"myID"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
	if [ -z "$device_id" ]; then
		fail "Syncthing API responded but myID was missing in /rest/system/status."
	fi

	if ! curl --silent --show-error --fail --retry 3 --retry-delay 1 --max-time 8 \
		"$relay_url/api/v1/health" >"$health_body"; then
		fail "Relay health check failed. Check RELAY_URL and outbound HTTPS connectivity."
	fi

	info "Validation successful: Syncthing reachable and relay health endpoint reachable."
	info "Detected Syncthing Device ID: $device_id"
}

run_builtin_doctor_if_available() {
	info "Running built-in doctor checks (includes trigger sanity check)."

	if [ -x "$NOTIFY_DIR/vaultsync-notify" ]; then
		(
			cd "$NOTIFY_DIR"
			SYNCTHING_API_URL="$SYNCTHING_API_URL" \
			SYNCTHING_API_KEY="$SYNCTHING_API_KEY" \
			RELAY_URL="$RELAY_URL" \
			DEBOUNCE_SECONDS="$DEBOUNCE_SECONDS" \
			WATCHED_FOLDERS="$WATCHED_FOLDERS" \
			./vaultsync-notify --doctor
		)
		return 0
	fi

	if command -v go >/dev/null 2>&1; then
		(
			cd "$NOTIFY_DIR"
			SYNCTHING_API_URL="$SYNCTHING_API_URL" \
			SYNCTHING_API_KEY="$SYNCTHING_API_KEY" \
			RELAY_URL="$RELAY_URL" \
			DEBOUNCE_SECONDS="$DEBOUNCE_SECONDS" \
			WATCHED_FOLDERS="$WATCHED_FOLDERS" \
			go run . --doctor
		)
		return 0
	fi

	warn "Go toolchain or local vaultsync-notify binary not found; skipping built-in doctor command."
	warn "Run this later from notify/: SYNCTHING_API_URL=... SYNCTHING_API_KEY=... RELAY_URL=... ./vaultsync-notify --doctor"
	return 0
}

if [ ! -f "$EXAMPLE_ENV_FILE" ]; then
	warn "Missing .env.example in notify directory. Continuing with generated defaults."
fi

info "vaultsync-notify bootstrap"
info "Notify directory: $NOTIFY_DIR"

detected_config_path=$(find_syncthing_config || true)
detected_api_key=""
detected_api_url="http://localhost:8384"

if [ -n "$detected_config_path" ]; then
	info "Detected Syncthing config: $detected_config_path"
	detected_api_key=$(extract_api_key "$detected_config_path" || true)
	gui_address=$(extract_gui_address "$detected_config_path" || true)
	gui_tls=$(extract_gui_tls "$detected_config_path" || true)
	if [ -z "$gui_tls" ]; then
		gui_tls="false"
	fi
	detected_api_url=$(infer_api_url "$gui_address" "$gui_tls")
else
	warn "Could not auto-detect Syncthing config.xml. Set SYNCTHING_CONFIG to a custom path if needed."
fi

# If detection only succeeded because the operator pointed SYNCTHING_CONFIG at a
# non-standard path, remember it so the generated .env can pin it. Otherwise a
# later bare-binary/systemd start would probe only the standard locations and miss
# the custom config (the commented auto-detect lines below would not help then).
custom_config_path=""
if [ -n "${SYNCTHING_CONFIG:-}" ] && [ "$detected_config_path" = "${SYNCTHING_CONFIG:-}" ]; then
	custom_config_path="$SYNCTHING_CONFIG"
fi

SYNCTHING_API_URL="${SYNCTHING_API_URL:-$detected_api_url}"
SYNCTHING_API_KEY="${SYNCTHING_API_KEY:-$detected_api_key}"
RELAY_URL="${RELAY_URL:-https://relay.vaultsync.eu}"
DEBOUNCE_SECONDS="${DEBOUNCE_SECONDS:-5}"
WATCHED_FOLDERS="${WATCHED_FOLDERS:-}"

if [ -t 0 ]; then
	SYNCTHING_API_URL=$(prompt_default "Syncthing API URL" "$SYNCTHING_API_URL")

	if [ -n "$SYNCTHING_API_KEY" ]; then
		if prompt_yes_no "Use auto-detected Syncthing API key from config.xml" "y"; then
			:
		else
			SYNCTHING_API_KEY=$(prompt_secret_required "Syncthing API key")
		fi
	else
		SYNCTHING_API_KEY=$(prompt_secret_required "Syncthing API key")
	fi

	RELAY_URL=$(prompt_default "Relay URL" "$RELAY_URL")
	DEBOUNCE_SECONDS=$(prompt_default "Debounce seconds" "$DEBOUNCE_SECONDS")
	WATCHED_FOLDERS=$(prompt_default "Watched folder IDs (comma-separated, blank = all)" "$WATCHED_FOLDERS")
fi

[ -n "$SYNCTHING_API_URL" ] || fail "SYNCTHING_API_URL is required."
[ -n "$SYNCTHING_API_KEY" ] || fail "SYNCTHING_API_KEY is required."
[ -n "$RELAY_URL" ] || fail "RELAY_URL is required."

run_curl_validations "$SYNCTHING_API_URL" "$SYNCTHING_API_KEY" "$RELAY_URL"

if [ -f "$ENV_FILE" ]; then
	backup_file="$ENV_FILE.bak.$(date +%Y%m%d%H%M%S)"
	cp "$ENV_FILE" "$backup_file"
	warn "Existing .env detected; backup created at $backup_file"
fi

umask 077
{
	printf '%s\n' "# Generated by scripts/bootstrap.sh"
	printf '%s\n' "#"
	printf '%s\n' "# SYNCTHING_API_URL/KEY are auto-detected by vaultsync-notify from Syncthing's"
	printf '%s\n' "# config.xml at runtime, so they are only written below if you overrode them."
	printf '%s\n' "# Leaving them unset keeps the Docker Compose path correct: compose reaches"
	printf '%s\n' "# Syncthing by its service name and reads the key from the shared config volume"
	printf '%s\n' "# — a host-detected URL/key would be wrong inside the sibling container."
	printf '%s\n' "RELAY_URL=$RELAY_URL"
	printf '%s\n' "DEBOUNCE_SECONDS=$DEBOUNCE_SECONDS"
	printf '%s\n' "WATCHED_FOLDERS=$WATCHED_FOLDERS"
	if [ -n "$custom_config_path" ]; then
		printf '%s\n' "# Custom config path you set — pinned so the binary finds the same config"
		printf '%s\n' "# (auto-detection would otherwise only probe the standard locations)."
		printf '%s\n' "SYNCTHING_CONFIG=$custom_config_path"
	fi
	if [ "$SYNCTHING_API_URL" != "$detected_api_url" ]; then
		printf '%s\n' "SYNCTHING_API_URL=$SYNCTHING_API_URL"
	else
		printf '%s\n' "#SYNCTHING_API_URL=$SYNCTHING_API_URL  # auto-detected; uncomment to override"
	fi
	if [ -n "$SYNCTHING_API_KEY" ] && [ "$SYNCTHING_API_KEY" != "$detected_api_key" ]; then
		printf '%s\n' "SYNCTHING_API_KEY=$SYNCTHING_API_KEY"
	else
		printf '%s\n' "#SYNCTHING_API_KEY=  # auto-detected from config.xml; uncomment to override"
	fi
} >"$ENV_FILE"
chmod 600 "$ENV_FILE" 2>/dev/null || true

info "Wrote $ENV_FILE (mode 600)."

if [ -t 0 ]; then
	if prompt_yes_no "Run built-in doctor mode now" "y"; then
		run_builtin_doctor_if_available
	fi
else
	run_builtin_doctor_if_available
fi

# bootstrap validated your *host* Syncthing, so the matching way to run the helper is
# the bare binary / a systemd service next to it — NOT `docker compose up -d
# vaultsync-notify` alone, which expects Syncthing as a sibling Compose service and
# would not reach your host Syncthing. The Docker Compose stack is a separate,
# self-contained topology (Syncthing + helper together) and is key-free on its own.
info ""
info "Next — start the helper next to your host Syncthing:"
info "  cd \"$NOTIFY_DIR\" && go build -o vaultsync-notify"
info "  set -a; . \"$ENV_FILE\"; set +a; ./vaultsync-notify   # load RELAY_URL etc. from .env"
info "  (or install it as a systemd service with EnvironmentFile=$ENV_FILE)."
info ""
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
	info "Prefer Docker? The Docker Compose stack runs Syncthing AND the helper together"
	info "and is key-free on its own (no bootstrap/.env needed for the key — it auto-detects"
	info "it from the shared volume): cd \"$NOTIFY_DIR\" && docker compose up -d"
	info "(override RELAY_URL to a mock first when testing — see docker-compose.yml)."
fi

info "Bootstrap complete."

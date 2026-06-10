#!/usr/bin/env sh
# vaultsync-notify one-line installer.
#
#   curl -fsSL https://vaultsync.eu/notify.sh | sh
#
# Skeptical of curl|sh? Quite right — append `-s -- --dry-run` to see every
# action without changing anything, or read this file first:
#   https://github.com/psimaker/vaultsync/blob/main/notify/scripts/install.sh
#
# What it does, in order:
#   1. Finds Syncthing's config.xml (same probe order as the helper binary).
#   2. Reads the file owner, so the helper runs with exactly that uid:gid —
#      config.xml is mode 0600, and a mismatched uid is the #1 setup failure.
#   3. Starts the helper:
#        - Linux with Docker  → ghcr.io/psimaker/vaultsync-notify container
#        - Linux without      → prebuilt binary + systemd service
#        - macOS              → prebuilt binary + launchd agent
#      Docker on macOS is skipped on purpose: without host networking the
#      container cannot reach a natively-running Syncthing on 127.0.0.1.
#   4. Runs the helper's --doctor preflight where possible, then verifies the
#      service actually came up.
#
# The helper sends only your Syncthing Device ID to the relay — never file
# names, folder names, or content. There is nothing user-specific in this
# script: identity comes from your own Syncthing instance at runtime.
#
# Environment overrides (all optional):
#   SYNCTHING_CONFIG        path to config.xml when auto-detection misses it
#                           (Synology/QNAP usually need this)
#   RELAY_URL               relay endpoint (default: production relay)
#   VAULTSYNC_NOTIFY_MODE   auto|docker|binary (default: auto)
#   VAULTSYNC_NOTIFY_IMAGE  container image override (development)
set -eu

RELAY_URL="${RELAY_URL:-https://relay.vaultsync.eu}"
MODE="${VAULTSYNC_NOTIFY_MODE:-auto}"
IMAGE="${VAULTSYNC_NOTIFY_IMAGE:-ghcr.io/psimaker/vaultsync-notify:latest}"
REPO="psimaker/vaultsync"
CONTAINER_NAME="vaultsync-notify"
DRY_RUN=0

for arg in "$@"; do
	case "$arg" in
		--dry-run) DRY_RUN=1 ;;
		*)
			printf 'ERROR: unknown argument: %s (only --dry-run is supported)\n' "$arg" >&2
			exit 1
			;;
	esac
done

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

# Execute a simple command, or print it instead under --dry-run.
run() {
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would run: $*"
		return 0
	fi
	"$@"
}

SUDO=""
need_root() {
	# Returns a prefix that makes the following command run as root. sudo prompts
	# on /dev/tty, so this works under `curl | sh` too.
	if [ "$(id -u)" = 0 ]; then
		SUDO=""
		return 0
	fi
	if command -v sudo >/dev/null 2>&1; then
		SUDO="sudo"
		return 0
	fi
	return 1
}

# --- 1. Locate config.xml ----------------------------------------------------

find_syncthing_config() {
	if [ -n "${SYNCTHING_CONFIG:-}" ]; then
		if [ -e "${SYNCTHING_CONFIG}" ]; then
			printf '%s\n' "$SYNCTHING_CONFIG"
			return 0
		fi
		fail "SYNCTHING_CONFIG is set to $SYNCTHING_CONFIG but no file exists there."
	fi

	# Probe order mirrors the helper binary (notify/syncthing_config.go):
	# current XDG state dir, legacy config dir, macOS, then container/system
	# service layouts.
	for candidate in \
		"${XDG_STATE_HOME:-$HOME/.local/state}/syncthing/config.xml" \
		"${XDG_CONFIG_HOME:-$HOME/.config}/syncthing/config.xml" \
		"$HOME/Library/Application Support/Syncthing/config.xml" \
		"/var/syncthing/config/config.xml" \
		"/config/config.xml" \
		"/var/syncthing/config.xml" \
		"/var/lib/syncthing/config.xml" \
		"/etc/syncthing/config.xml"; do
		if [ -e "$candidate" ]; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done

	return 1
}

# --- 2. File owner -----------------------------------------------------------

owner_of() {
	# GNU stat (Linux) vs BSD stat (macOS). Reading the owner needs no read
	# permission on the file itself, so this works even when the config is 0600
	# under another user.
	path="$1"
	if stat -c '%u:%g' "$path" 2>/dev/null; then
		return 0
	fi
	stat -f '%u:%g' "$path" 2>/dev/null
}

# --- 3a. Docker path ---------------------------------------------------------

resolve_docker() {
	command -v docker >/dev/null 2>&1 || return 1
	if docker info >/dev/null 2>&1; then
		DOCKER="docker"
		return 0
	fi
	if command -v sudo >/dev/null 2>&1 && sudo docker info >/dev/null 2>&1; then
		DOCKER="sudo docker"
		return 0
	fi
	return 1
}

install_docker() {
	config_dir=$(dirname -- "$CONFIG_PATH")
	config_name=$(basename -- "$CONFIG_PATH")

	if $DOCKER ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER_NAME"; then
		info "Replacing existing $CONTAINER_NAME container (it keeps no state)."
		run $DOCKER rm -f "$CONTAINER_NAME"
	fi

	info "Running preflight checks (doctor)..."
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would run: $DOCKER run --rm --network host -u $OWNER -v $config_dir:/config:ro -e SYNCTHING_CONFIG=/config/$config_name -e RELAY_URL=$RELAY_URL $IMAGE --doctor"
	else
		$DOCKER run --rm --network host \
			-u "$OWNER" \
			-v "$config_dir":/config:ro \
			-e SYNCTHING_CONFIG="/config/$config_name" \
			-e RELAY_URL="$RELAY_URL" \
			"$IMAGE" --doctor \
			|| fail "Preflight failed — see the messages above for the fix, then re-run this installer."
	fi

	info "Starting the helper container..."
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would run: $DOCKER run -d --name $CONTAINER_NAME --restart unless-stopped --network host -u $OWNER -v $config_dir:/config:ro -e SYNCTHING_CONFIG=/config/$config_name -e RELAY_URL=$RELAY_URL $IMAGE"
		return 0
	fi
	$DOCKER run -d --name "$CONTAINER_NAME" --restart unless-stopped \
		--network host \
		-u "$OWNER" \
		-v "$config_dir":/config:ro \
		-e SYNCTHING_CONFIG="/config/$config_name" \
		-e RELAY_URL="$RELAY_URL" \
		"$IMAGE" >/dev/null
}

# --- 3b. Binary path ---------------------------------------------------------

detect_asset() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
		Linux) goos="linux" ;;
		Darwin) goos="darwin" ;;
		*) fail "Unsupported OS for the binary install: $os. Use Docker, or build from source (notify/README.md)." ;;
	esac
	case "$arch" in
		x86_64 | amd64) goarch="amd64" ;;
		aarch64 | arm64) goarch="arm64" ;;
		*) fail "Unsupported CPU architecture: $arch (prebuilt binaries cover amd64 and arm64). Build from source: notify/README.md." ;;
	esac
	printf 'vaultsync-notify_%s_%s\n' "$goos" "$goarch"
}

latest_notify_tag() {
	# The repo also publishes app releases (v*); pick the newest notify-v* tag.
	curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" \
		| grep -o '"tag_name": *"notify-v[^"]*"' \
		| head -1 \
		| sed 's/.*"\(notify-v[^"]*\)"/\1/'
}

download_binary() {
	asset="$1"
	tag="$2"
	dest="$3"
	base="https://github.com/$REPO/releases/download/$tag"

	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would download: $base/$asset -> $dest (and verify against $base/SHA256SUMS)"
		return 0
	fi

	tmpdir=$(mktemp -d)
	trap 'rm -rf "$tmpdir"' EXIT
	curl -fsSL -o "$tmpdir/$asset" "$base/$asset" \
		|| fail "Download failed: $base/$asset"

	# Verify against the release checksums when a SHA-256 tool exists.
	if curl -fsSL -o "$tmpdir/SHA256SUMS" "$base/SHA256SUMS" 2>/dev/null; then
		if command -v sha256sum >/dev/null 2>&1; then
			(cd "$tmpdir" && grep " $asset\$" SHA256SUMS | sha256sum -c - >/dev/null) \
				|| fail "Checksum mismatch for $asset — aborting."
		elif command -v shasum >/dev/null 2>&1; then
			(cd "$tmpdir" && grep " $asset\$" SHA256SUMS | shasum -a 256 -c - >/dev/null) \
				|| fail "Checksum mismatch for $asset — aborting."
		else
			warn "No sha256sum/shasum found; skipping checksum verification."
		fi
	else
		warn "Could not fetch SHA256SUMS; skipping checksum verification."
	fi

	chmod 755 "$tmpdir/$asset"
	if [ -w "$(dirname -- "$dest")" ]; then
		mv "$tmpdir/$asset" "$dest"
	else
		need_root || fail "Cannot write $dest and sudo is unavailable. Re-run as root."
		$SUDO mv "$tmpdir/$asset" "$dest"
	fi
}

run_doctor_binary() {
	bin="$1"
	# Preflight as the current user. If the config is unreadable for us (it
	# belongs to a dedicated syncthing user), skip — the service runs as the
	# owner, and the post-start check below still catches real failures.
	if [ ! -r "$CONFIG_PATH" ]; then
		info "Skipping doctor preflight ($CONFIG_PATH is not readable by $(id -un); the service will run as uid:gid $OWNER)."
		return 0
	fi
	info "Running preflight checks (doctor)..."
	run env SYNCTHING_CONFIG="$CONFIG_PATH" RELAY_URL="$RELAY_URL" "$bin" --doctor \
		|| fail "Preflight failed — see the messages above for the fix, then re-run this installer."
}

install_systemd() {
	bin="/usr/local/bin/vaultsync-notify"
	unit="/etc/systemd/system/vaultsync-notify.service"
	need_root || fail "Installing the systemd service needs root. Re-run with sudo, or use Docker."

	download_binary "$ASSET" "$TAG" "$bin"
	run_doctor_binary "$bin"

	owner_uid=${OWNER%%:*}
	owner_gid=${OWNER#*:}
	unit_content="[Unit]
Description=VaultSync notify helper (Cloud Relay wake-ups)
Documentation=https://github.com/$REPO/blob/main/notify/README.md
After=network-online.target syncthing.service
Wants=network-online.target

[Service]
ExecStart=$bin
Environment=RELAY_URL=$RELAY_URL
Environment=SYNCTHING_CONFIG=$CONFIG_PATH
User=$owner_uid
Group=$owner_gid
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target"

	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would write $unit:"
		printf '%s\n' "$unit_content" | sed 's/^/[dry-run]   /'
		info "[dry-run] would run: $SUDO systemctl daemon-reload && $SUDO systemctl enable --now vaultsync-notify"
		return 0
	fi

	printf '%s\n' "$unit_content" | $SUDO tee "$unit" >/dev/null
	$SUDO systemctl daemon-reload
	$SUDO systemctl enable --now vaultsync-notify

	sleep 2
	if ! $SUDO systemctl is-active --quiet vaultsync-notify; then
		$SUDO journalctl -u vaultsync-notify -n 20 --no-pager 2>/dev/null || true
		fail "The service did not stay up — the log above explains why. Fix it and re-run this installer."
	fi
}

install_launchd() {
	bin_dir="$HOME/.local/bin"
	bin="$bin_dir/vaultsync-notify"
	agent_dir="$HOME/Library/LaunchAgents"
	plist="$agent_dir/eu.vaultsync.notify.plist"
	label="eu.vaultsync.notify"

	run mkdir -p "$bin_dir" "$agent_dir"
	download_binary "$ASSET" "$TAG" "$bin"
	run_doctor_binary "$bin"

	plist_content="<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">
<plist version=\"1.0\">
<dict>
	<key>Label</key>
	<string>$label</string>
	<key>ProgramArguments</key>
	<array>
		<string>$bin</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>RELAY_URL</key>
		<string>$RELAY_URL</string>
		<key>SYNCTHING_CONFIG</key>
		<string>$CONFIG_PATH</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>/tmp/vaultsync-notify.log</string>
</dict>
</plist>"

	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would write $plist and (re)load it via launchctl"
		return 0
	fi

	printf '%s\n' "$plist_content" >"$plist"
	uid=$(id -u)
	launchctl bootout "gui/$uid/$label" 2>/dev/null || true
	launchctl bootstrap "gui/$uid" "$plist"

	sleep 2
	if ! launchctl print "gui/$uid/$label" >/dev/null 2>&1; then
		fail "The launchd agent did not start — check /tmp/vaultsync-notify.log, fix it, and re-run this installer."
	fi
}

install_binary() {
	command -v curl >/dev/null 2>&1 || fail "curl is required for the binary install."
	ASSET=$(detect_asset)
	TAG=$(latest_notify_tag) || TAG=""
	[ -n "$TAG" ] || fail "Could not find a notify release on GitHub ($REPO). Check your network, or build from source: notify/README.md."
	info "Installing $ASSET from release $TAG."

	case "$(uname -s)" in
		Darwin)
			install_launchd
			;;
		Linux)
			if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
				install_systemd
			else
				bin="./vaultsync-notify"
				download_binary "$ASSET" "$TAG" "$bin"
				run_doctor_binary "$bin"
				warn "No systemd found — downloaded $bin but could not install a service."
				info "Start it manually and keep it running:"
				info "  SYNCTHING_CONFIG=\"$CONFIG_PATH\" RELAY_URL=\"$RELAY_URL\" $bin"
				exit 0
			fi
			;;
	esac
}

# --- Main --------------------------------------------------------------------

info "VaultSync Cloud Relay — server helper installer"
[ "$DRY_RUN" = 1 ] && info "(dry run — nothing will be changed)"

if CONFIG_PATH=$(find_syncthing_config); then
	info "Found Syncthing config: $CONFIG_PATH"
else
	fail "Could not find Syncthing's config.xml. Is Syncthing installed on THIS machine?
  - If it lives elsewhere, re-run with its path:  SYNCTHING_CONFIG=/path/to/config.xml sh -s
  - Synology/QNAP and custom setups: https://github.com/$REPO/blob/main/notify/README.md"
fi

OWNER=$(owner_of "$CONFIG_PATH") || fail "Could not read the owner of $CONFIG_PATH."
info "Helper will run as uid:gid $OWNER (the owner of config.xml)."

case "$MODE" in
	docker)
		resolve_docker || fail "VAULTSYNC_NOTIFY_MODE=docker, but Docker is not usable here."
		install_docker
		;;
	binary)
		install_binary
		;;
	auto)
		# Docker on macOS cannot use host networking to reach a native Syncthing
		# on 127.0.0.1, so macOS always takes the launchd binary path.
		if [ "$(uname -s)" = "Linux" ] && resolve_docker; then
			install_docker
		else
			install_binary
		fi
		;;
	*)
		fail "Invalid VAULTSYNC_NOTIFY_MODE: $MODE (use auto, docker, or binary)."
		;;
esac

info ""
if [ "$DRY_RUN" = 1 ]; then
	info "Dry run complete — nothing was changed. Re-run without --dry-run to install."
else
	info "Done. The helper has sent a first wake-up — within a minute, VaultSync on"
	info "your iPhone shows \"Cloud Relay active\" (Relay tab). Nothing but your"
	info "Syncthing Device ID ever leaves this machine."
fi

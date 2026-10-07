#!/usr/bin/env sh
# VaultSync setup — one link for every device.
#
#   curl -fsSL https://vaultsync.eu/setup.sh | sh
#
# It asks one question:
#   1) Obsidian device   this computer edits notes and syncs them with your Hub:
#                        installs VaultSync for Mac and Linux (`vaultsync`, it
#                        brings its own Syncthing) and runs its setup
#   2) Hub               a server or NAS that keeps every vault (set up once)
#
# Skeptical of curl|sh? Append `-s -- --dry-run` to see every action without
# changing anything, or read this file: hub/scripts/setup.sh in the repo.
#
# Flags:            --hub | --device   skip the menu
#                   --code WORD-WORD-NN pairing code (device)
#                   --dry-run          print actions only
#                   -- ARGS            (device) passed on to `vaultsync setup`,
#                                      e.g. -- --vault Notes --path ~/Vaults/Notes
# Environment:      VAULTSYNC_HUB_DIR  where the Hub stack lives (default /srv/vaultsync)
#                   VAULTSYNC_HUB_NAME how the Hub introduces itself
#                   VAULTSYNC_HUB_IMAGE / RELAY_URL   development overrides
set -eu

REPO="psimaker/vaultsync"
HUB_DIR="${VAULTSYNC_HUB_DIR:-/srv/vaultsync}"
HUB_NAME="${VAULTSYNC_HUB_NAME:-VaultSync Hub}"
HUB_IMAGE="${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.2.0}"
# Captured: an exported value outranks .env in Compose, so left in the
# environment it would decide the image of an existing Hub over its .env —
# and over a "no" to the offer below (#216).
unset VAULTSYNC_HUB_IMAGE
RELAY_URL="${RELAY_URL:-https://relay.vaultsync.eu}"
DRY_RUN=0
CHOICE=""
CODE=""

while [ $# -gt 0 ]; do
	case "$1" in
		--dry-run) DRY_RUN=1 ;;
		--hub) CHOICE=2 ;;
		--device) CHOICE=1 ;;
		--code)
			shift
			[ $# -gt 0 ] || { printf 'ERROR: --code needs a value\n' >&2; exit 1; }
			CODE="$1"
			;;
		--)
			# Everything after -- belongs to `vaultsync setup` (device path).
			shift
			break
			;;
		*)
			printf 'ERROR: unknown argument: %s\n' "$1" >&2
			exit 1
			;;
	esac
	shift
done

info() { printf '%s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# Execute a simple command, or print it instead under --dry-run.
run() {
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would run: $*"
		return 0
	fi
	"$@"
}

# Read one line from the terminal. Under `curl | sh` stdin is the script, so
# prompts must go through /dev/tty.
ask() {
	prompt="$1"
	default="$2"
	if [ -r /dev/tty ]; then
		printf '%s' "$prompt" >/dev/tty
		if read -r answer </dev/tty; then
			[ -n "$answer" ] && { printf '%s\n' "$answer"; return 0; }
		fi
		printf '%s\n' "$default"
		return 0
	fi
	return 1
}

# is_release VERSION: a plain x.y.z in canonical numbers (no leading zeros,
# at most nine digits each), as the official image tags are.
is_release() {
	printf '%s' "$1" | grep -Eq '^(0|[1-9][0-9]{0,8})(\.(0|[1-9][0-9]{0,8})){2}$'
}

# version_newer A B: true when A is a higher x.y.z than B — compared as
# three integers, never as text or floating point.
version_newer() {
	a1=${1%%.*}; rest=${1#*.}; a2=${rest%%.*}; a3=${rest#*.}
	b1=${2%%.*}; rest=${2#*.}; b2=${rest%%.*}; b3=${rest#*.}
	[ "$a1" -gt "$b1" ] && return 0
	[ "$a1" -lt "$b1" ] && return 1
	[ "$a2" -gt "$b2" ] && return 0
	[ "$a2" -lt "$b2" ] && return 1
	[ "$a3" -gt "$b3" ]
}

# plain_env_file FILE: only blank lines, comments and NAME=value lines
# without quotes — the shape setup writes — and VAULTSYNC_HUB_IMAGE exactly
# once. Anything else (a quoted value may span lines) is not edited.
plain_env_file() {
	! grep -Eqv '^([[:space:]]*(#.*)?|[A-Za-z_][A-Za-z0-9_]*=[^"'"'"']*)$' "$1" &&
		[ "$(grep -c '^VAULTSYNC_HUB_IMAGE=' "$1")" = 1 ]
}

manual_image_hint() {
	info "  To move later: set VAULTSYNC_HUB_IMAGE=$HUB_IMAGE in $HUB_DIR/.env,"
	info "  then run docker compose pull && docker compose up -d there (docs/hub.md → Update the Hub)."
}

# An existing Hub stays on the image its .env names — re-running the setup
# never moves it by itself. When that is an older official release than the
# one this setup ships, the move is offered: only on a terminal, only with
# consent, never for a custom image, never a downgrade, and only on an .env
# in the plain shape setup writes, so the one line changed is the one that
# was read (#216). Declined, without a terminal or on any other .env, the
# manual step is named instead.
offer_newer_hub_image() {
	env_file="$1"
	official="ghcr.io/psimaker/vaultsync-hub:"
	case "$HUB_IMAGE" in
		"$official"*) ;;
		*) return 0 ;; # this setup ships a custom image: nothing to offer
	esac
	new_ver=${HUB_IMAGE#"$official"}
	is_release "$new_ver" || return 0
	if ! plain_env_file "$env_file"; then
		info "Your Hub's .env has a shape setup does not edit (a quoted value, or VAULTSYNC_HUB_IMAGE more than once)."
		manual_image_hint
		return 0
	fi
	current=$(sed -n 's/^VAULTSYNC_HUB_IMAGE=//p' "$env_file")
	case "$current" in
		"" | "$HUB_IMAGE") return 0 ;;
		"$official"*) ;;
		*) info "Your Hub runs a custom image ($current); setup leaves it as it is."; return 0 ;;
	esac
	cur_ver=${current#"$official"}
	is_release "$cur_ver" || return 0
	version_newer "$new_ver" "$cur_ver" || return 0
	info "Your Hub runs vaultsync-hub $cur_ver; this setup ships $new_ver."
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would offer to set VAULTSYNC_HUB_IMAGE=$HUB_IMAGE in $env_file"
		return 0
	fi
	if answer=$(ask "  Move it to $new_ver now? Vaults, devices, ports and names carry over; a new pairing code is printed at the end, as always. [y/N] " "n"); then
		case "$answer" in
			[yY]*)
				# Through a copy, back into the same file: .env keeps its
				# owner and mode, and nothing depends on a GNU sed.
				sed "s|^VAULTSYNC_HUB_IMAGE=.*|VAULTSYNC_HUB_IMAGE=$HUB_IMAGE|" "$env_file" >"$env_file.setup-tmp" &&
					cat "$env_file.setup-tmp" >"$env_file"
				rm -f "$env_file.setup-tmp"
				info "  .env now names $HUB_IMAGE; the stack restarts on it below."
				return 0
				;;
		esac
	fi
	info "  Kept $cur_ver."
	manual_image_hint
}

# --- Menu --------------------------------------------------------------------

info ""
info "  VaultSync setup"
info ""
[ "$DRY_RUN" = 1 ] && info "  (dry run — nothing will be changed)"

if [ -z "$CHOICE" ]; then
	info "  1) Obsidian device   — this computer edits notes and syncs them with your Hub"
	info "  2) Hub               — a server or NAS that keeps every vault (set up once)"
	info ""
	CHOICE=$(ask "  Choice [1]: " "1") ||
		fail "No terminal to ask on. Re-run with --device or --hub."
fi

case "$CHOICE" in
	1 | 2) ;;
	*) fail "Please answer 1 or 2." ;;
esac

# --- Shared helpers ----------------------------------------------------------

resolve_docker() {
	command -v docker >/dev/null 2>&1 || return 1
	if docker info >/dev/null 2>&1; then
		DOCKER="docker"
	elif command -v sudo >/dev/null 2>&1 && sudo docker info >/dev/null 2>&1; then
		DOCKER="sudo docker"
	else
		return 1
	fi
	$DOCKER compose version >/dev/null 2>&1 || return 1
	return 0
}

port_in_use() {
	port="$1"
	if command -v ss >/dev/null 2>&1; then
		ss -Hlnt 2>/dev/null | awk '{print $4}' | grep -q ":$port\$" && return 0
	elif command -v netstat >/dev/null 2>&1; then
		netstat -lnt 2>/dev/null | awk '{print $4}' | grep -q "[.:]$port\$" && return 0
	fi
	return 1
}

# detect_asset NAME prints the release asset for this computer: NAME_<os>_<arch>.
detect_asset() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
		Linux) goos="linux" ;;
		Darwin) goos="darwin" ;;
		*) fail "Unsupported OS: $os (Linux and macOS are supported today; Windows follows)." ;;
	esac
	case "$arch" in
		x86_64 | amd64) goarch="amd64" ;;
		aarch64 | arm64) goarch="arm64" ;;
		*) fail "Unsupported CPU architecture: $arch (prebuilt binaries cover amd64 and arm64)." ;;
	esac
	printf '%s_%s_%s\n' "$1" "$goos" "$goarch"
}

# latest_stable_tag PREFIX prints the newest stable release tagged PREFIX<x.y.z>:
# no draft, no pre-release, no "-rc" style suffix, compared as versions —
# GitHub lists releases by their commit date, not by version, and 100 per
# page, so every page is read (until an empty one; at most 10). A failed
# request fails the lookup instead of choosing from a partial list. The
# release objects come pretty-printed with "draft" and "prerelease" after
# "tag_name".
latest_stable_tag() {
	prefix="$1"
	found=""
	page=1
	while [ "$page" -le 10 ]; do
		json=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=100&page=$page") || return 1
		printf '%s\n' "$json" | grep -q '"tag_name":' || break
		found="$found
$(printf '%s\n' "$json" | awk -v prefix="$prefix" '
			function flush() {
				if (tag != "" && draft == "false" && pre == "false") print tag
				tag = ""; draft = ""; pre = ""
			}
			/"tag_name":/ {
				flush()
				t = $0
				sub(/.*"tag_name": *"/, "", t); sub(/".*/, "", t)
				if (index(t, prefix) == 1 && substr(t, length(prefix) + 1) ~ /^[0-9]+\.[0-9]+\.[0-9]+$/) tag = t
				next
			}
			tag != "" && draft == "" && /"draft":/ { draft = ($0 ~ /true/) ? "true" : "false" }
			tag != "" && pre == "" && /"prerelease":/ { pre = ($0 ~ /true/) ? "true" : "false" }
			END { flush() }
		')"
		page=$((page + 1))
	done
	printf '%s\n' "$found" |
		sed -n "s/^$prefix//p" |
		sort -t. -k1,1n -k2,2n -k3,3n |
		tail -1 |
		sed "s/^/$prefix/"
}

# download_binary ASSET TAG DEST WORKDIR downloads a release binary into
# WORKDIR, verifies it against the release's SHA256SUMS and moves it to DEST.
# The caller owns WORKDIR and its cleanup.
download_binary() {
	asset="$1"
	tag="$2"
	dest="$3"
	tmpdir="$4"
	base="https://github.com/$REPO/releases/download/$tag"
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would download: $base/$asset -> $dest (verified against $base/SHA256SUMS)"
		return 0
	fi
	curl -fsSL -o "$tmpdir/$asset" "$base/$asset" || fail "Download failed: $base/$asset"
	curl -fsSL -o "$tmpdir/SHA256SUMS" "$base/SHA256SUMS" || fail "Could not fetch SHA256SUMS for $tag — nothing was installed."
	checksum=$(awk -v asset="$asset" '$2 == asset { n++; v = $1 } END { if (n != 1) exit 1; print v }' "$tmpdir/SHA256SUMS") ||
		fail "SHA256SUMS must contain exactly one checksum for $asset — nothing was installed."
	case $checksum in
		*[!0-9a-f]*) fail "Non-canonical checksum for $asset — nothing was installed." ;;
	esac
	[ "${#checksum}" -eq 64 ] || fail "Non-canonical checksum for $asset — nothing was installed."
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$tmpdir" && printf '%s  %s\n' "$checksum" "$asset" | sha256sum -c - >/dev/null) || fail "Checksum mismatch for $asset — aborting."
	elif command -v shasum >/dev/null 2>&1; then
		(cd "$tmpdir" && printf '%s  %s\n' "$checksum" "$asset" | shasum -a 256 -c - >/dev/null) || fail "Checksum mismatch for $asset — aborting."
	else
		fail "sha256sum or shasum is required to verify $asset — nothing was installed."
	fi
	chmod 755 "$tmpdir/$asset"
	mkdir -p "$(dirname -- "$dest")"
	mv "$tmpdir/$asset" "$dest"
}

# --- 2) Hub ------------------------------------------------------------------

write_hub_files() {
	compose="$HUB_DIR/docker-compose.yml"
	env_file="$HUB_DIR/.env"
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would write: $compose and $env_file (PUID=$PUID PGID=$PGID SYNC_PORT=$SYNC_PORT GUI_PORT=$GUI_PORT)"
		return 0
	fi
	cat >"$compose" <<'COMPOSE'
# VaultSync Hub — the self-hosted stack `setup.sh` installs.
#
#   syncthing   the Hub's own Syncthing instance (your notes live in ./vaults)
#   hub         pairing coordinator: LAN discovery + pairing codes (host network,
#               so UDP broadcasts from your devices reach it)
#   notify      wake-up helper for VaultSync on iPhone (Cloud Relay subscribers)
#
# Everything is a bind mount under this directory — plain files you can back up,
# inspect and move. No named volumes, no hidden state.
#
# Values come from .env (written by setup.sh): PUID/PGID, ports, image tags.

services:
  syncthing:
    image: syncthing/syncthing:1
    container_name: vaultsync-hub-syncthing
    hostname: ${VAULTSYNC_HUB_NAME:-VaultSync Hub}
    environment:
      PUID: ${PUID:-1000}
      PGID: ${PGID:-1000}
      STGUIADDRESS: 0.0.0.0:8384
      STNODEFAULTFOLDER: "1"
      STNOUPGRADE: "1"
    volumes:
      - ./syncthing:/var/syncthing
      - ./vaults:/var/syncthing/vaults
    ports:
      - "127.0.0.1:${GUI_PORT:-8384}:8384"   # Web UI, loopback only (ssh tunnel to reach it)
      - "${SYNC_PORT:-22000}:22000/tcp"      # sync protocol
      - "${SYNC_PORT:-22000}:22000/udp"      # sync protocol (QUIC)
      - "21027:21027/udp"                    # local discovery
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://127.0.0.1:8384/rest/noauth/health"]
      interval: 30s
      timeout: 10s
      start_period: 30s
      retries: 3
    restart: unless-stopped

  hub:
    image: ${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.2.0}
    container_name: vaultsync-hub
    # Host networking: LAN discovery answers UDP broadcasts, which never cross a
    # Docker bridge. The pairing port (8390) is LAN-only by design — do not
    # forward it on your router.
    network_mode: host
    user: "${PUID:-1000}:${PGID:-1000}"
    environment:
      SYNCTHING_CONFIG: /var/syncthing/config/config.xml
      SYNCTHING_API_URL: http://127.0.0.1:${GUI_PORT:-8384}
      VAULTSYNC_HUB_VAULTS: /var/syncthing/vaults
      VAULTSYNC_HUB_PORT: ${HUB_PORT:-8390}
      VAULTSYNC_HUB_NAME: ${VAULTSYNC_HUB_NAME:-VaultSync Hub}
    volumes:
      - ./syncthing:/var/syncthing:ro        # reads config.xml (API key) only
      - ./vaults:/var/syncthing/vaults       # creates vault directories
      - ./hub:/var/lib/vaultsync-hub         # pairing state (0600)
    depends_on:
      syncthing:
        condition: service_healthy
    restart: unless-stopped

  notify:
    image: ghcr.io/psimaker/vaultsync-notify:2.0.2
    # Distinct from the `vaultsync-notify` container the notify.sh installer
    # creates for a pre-existing Syncthing, so both can coexist on one host.
    container_name: vaultsync-hub-notify
    user: "${PUID:-1000}:${PGID:-1000}"
    environment:
      SYNCTHING_CONFIG: /var/syncthing/config/config.xml
      SYNCTHING_API_URL: http://syncthing:8384
      RELAY_URL: ${RELAY_URL:-https://relay.vaultsync.eu}
      STARTUP_ANNOUNCE: ${STARTUP_ANNOUNCE:-true}
      SYNCTHING_CONFIG_WAIT_SECONDS: "120"
    volumes:
      - ./syncthing:/var/syncthing:ro
    depends_on:
      syncthing:
        condition: service_healthy
    restart: unless-stopped
COMPOSE
	if [ ! -f "$env_file" ]; then
		cat >"$env_file" <<ENV
# Written by setup.sh — edit and run \`docker compose up -d\` to apply.
PUID=$PUID
PGID=$PGID
SYNC_PORT=$SYNC_PORT
GUI_PORT=$GUI_PORT
HUB_PORT=8390
VAULTSYNC_HUB_NAME=$HUB_NAME
VAULTSYNC_HUB_IMAGE=$HUB_IMAGE
RELAY_URL=$RELAY_URL
ENV
	fi
}

setup_hub() {
	[ "$(uname -s)" = "Linux" ] ||
		fail "The Hub runs on a Linux server or NAS with Docker. On this computer choose 1) Obsidian device."
	resolve_docker || fail "Docker (with the compose plugin) is required on the Hub.
  Install it first:  curl -fsSL https://get.docker.com | sh
  then re-run this setup."

	if [ -n "${SUDO_UID:-}" ] && [ -n "${SUDO_GID:-}" ]; then
		PUID="$SUDO_UID"
		PGID="$SUDO_GID"
	elif [ "$(id -u)" != 0 ]; then
		PUID="$(id -u)"
		PGID="$(id -g)"
	else
		PUID=1000
		PGID=1000
	fi

	SYNC_PORT=22000
	GUI_PORT=8384
	if [ -d "$HUB_DIR" ] && [ -f "$HUB_DIR/.env" ]; then
		info "Existing Hub found in $HUB_DIR — updating it (your .env is kept)."
		# The hints below must name the ports this Hub actually uses.
		existing_sync=$(sed -n 's/^SYNC_PORT=//p' "$HUB_DIR/.env" | tail -1)
		existing_gui=$(sed -n 's/^GUI_PORT=//p' "$HUB_DIR/.env" | tail -1)
		[ -z "$existing_sync" ] || SYNC_PORT="$existing_sync"
		[ -z "$existing_gui" ] || GUI_PORT="$existing_gui"
		offer_newer_hub_image "$HUB_DIR/.env"
	else
		if port_in_use 22000; then
			warn "Port 22000 is already in use (another Syncthing?). The Hub will use 22001."
			SYNC_PORT=22001
		fi
		if port_in_use 8384; then
			GUI_PORT=8385
		fi
	fi

	info "Hub directory: $HUB_DIR (vaults live in $HUB_DIR/vaults)"
	if [ ! -d "$HUB_DIR" ] && [ "$DRY_RUN" = 0 ]; then
		mkdir -p "$HUB_DIR" 2>/dev/null || {
			command -v sudo >/dev/null 2>&1 || fail "Cannot create $HUB_DIR. Re-run as root or set VAULTSYNC_HUB_DIR."
			sudo mkdir -p "$HUB_DIR" && sudo chown "$(id -u):$(id -g)" "$HUB_DIR"
		}
	fi
	run mkdir -p "$HUB_DIR/syncthing" "$HUB_DIR/vaults" "$HUB_DIR/hub"
	run chown -R "$PUID:$PGID" "$HUB_DIR/syncthing" "$HUB_DIR/vaults" "$HUB_DIR/hub" 2>/dev/null || true
	write_hub_files

	info "Starting the Hub (this pulls three images on first run)..."
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR pull"
		info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR up -d"
		info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR exec -T hub vaultsync-hub code"
		return 0
	fi
	$DOCKER compose --project-directory "$HUB_DIR" pull --quiet
	$DOCKER compose --project-directory "$HUB_DIR" up -d

	i=0
	until $DOCKER compose --project-directory "$HUB_DIR" exec -T hub vaultsync-hub status >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt 45 ] || {
			$DOCKER compose --project-directory "$HUB_DIR" logs --tail 30 hub >&2 || true
			fail "The Hub did not become ready. The log above explains why; fix it and re-run."
		}
		sleep 2
	done

	info ""
	info "✓ Hub is running."
	$DOCKER compose --project-directory "$HUB_DIR" exec -T hub vaultsync-hub status | sed -n '2p'
	$DOCKER compose --project-directory "$HUB_DIR" exec -T hub vaultsync-hub code
	info "  Web UI (advanced):  ssh -L $GUI_PORT:127.0.0.1:$GUI_PORT <this host>, then http://127.0.0.1:$GUI_PORT"
	info "  New code any time:  cd $HUB_DIR && docker compose exec hub vaultsync-hub code"
	info ""
}

# --- 1) Obsidian device ------------------------------------------------------

# A Syncthing the person already runs. The agent never touches it; it is
# only worth a word because pairing that Syncthing itself is another route.
find_syncthing_config() {
	for c in \
		"${XDG_STATE_HOME:-$HOME/.local/state}/syncthing/config.xml" \
		"${XDG_CONFIG_HOME:-$HOME/.config}/syncthing/config.xml" \
		"$HOME/Library/Application Support/Syncthing/config.xml"; do
		[ -r "$c" ] && return 0
	done
	return 1
}

# The device path installs VaultSync for Mac and Linux (`vaultsync`, it brings
# its own Syncthing) from the newest stable desktop-v* release, verified
# against that release's SHA256SUMS, and runs its setup: the agent asks for
# the code and the vault on the terminal itself (it reads /dev/tty, so
# curl|sh can ask), installs its background service and links itself as
# ~/.local/bin/vaultsync. The downloaded copy is only the installer and is
# removed afterwards. Arguments after -- go to `vaultsync setup`.
setup_device() {
	command -v curl >/dev/null 2>&1 || fail "curl is required."
	asset=$(detect_asset vaultsync)
	tag=$(latest_stable_tag desktop-v) || tag=""
	[ -n "$tag" ] || fail "Could not find a VaultSync release for Mac and Linux on GitHub ($REPO). Check your network."
	info "VaultSync for Mac and Linux ${tag#desktop-v} ($asset)"
	if find_syncthing_config; then
		info "  Syncthing is already set up on this computer. VaultSync runs its own and never"
		info "  touches yours. To pair your own Syncthing with your Hub instead, see"
		info "  https://github.com/$REPO/blob/main/docs/hub.md (vaultsync-hub pair)."
	fi
	set -- setup ${CODE:+--code "$CODE"} "$@"
	if [ "$DRY_RUN" = 1 ]; then
		download_binary "$asset" "$tag" "<temporary folder>/vaultsync" ""
		# Never print the pairing code, in any spelling the agent accepts.
		shown=""
		hide=0
		for a in "$@"; do
			if [ "$hide" = 1 ]; then
				a="<code>"
				hide=0
			fi
			case "$a" in
				--code | -code) hide=1 ;;
				--code=* | -code=*) a="${a%%=*}=<code>" ;;
			esac
			shown="$shown $a"
		done
		info "[dry-run] would run: <temporary folder>/vaultsync$shown"
		return 0
	fi
	installer_dir=$(mktemp -d)
	# The download is gone however this ends — also on Ctrl-C or a kill.
	trap 'rm -rf "$installer_dir"' EXIT
	trap 'rm -rf "$installer_dir"; exit 129' HUP
	trap 'rm -rf "$installer_dir"; exit 130' INT
	trap 'rm -rf "$installer_dir"; exit 143' TERM
	mkdir "$installer_dir/download"
	download_binary "$asset" "$tag" "$installer_dir/vaultsync" "$installer_dir/download"
	info ""
	"$installer_dir/vaultsync" "$@"
}

case "$CHOICE" in
	1) setup_device "$@" ;;
	2)
		# A pairing code and agent arguments belong to the device setup; the
		# Hub prints its own code. Silently ignoring them would hide a mix-up.
		[ -z "$CODE" ] || fail "--code belongs to the device setup (1) Obsidian device); the Hub prints its own code."
		[ $# -eq 0 ] || fail "Arguments after -- belong to the device setup (1) Obsidian device)."
		setup_hub
		;;
esac

if [ "$DRY_RUN" = 1 ]; then
	info ""
	info "Dry run complete — nothing was changed."
fi

#!/usr/bin/env sh
# VaultSync setup — one link for every device.
#
#   curl -fsSL https://vaultsync.eu/setup.sh | sh
#
# It asks one question:
#   1) Obsidian device   this computer edits notes and syncs them with your Hub
#   2) Hub               a server or NAS that keeps every vault (set up once)
#
# Skeptical of curl|sh? Append `-s -- --dry-run` to see every action without
# changing anything, or read this file: hub/scripts/setup.sh in the repo.
#
# Flags:            --hub | --device   skip the menu
#                   --code WORD-WORD-NN pairing code (device)
#                   --dry-run          print actions only
# Environment:      VAULTSYNC_HUB_DIR  where the Hub stack lives (default /srv/vaultsync)
#                   VAULTSYNC_HUB_NAME how the Hub introduces itself
#                   VAULTSYNC_HUB_IMAGE / RELAY_URL   development overrides
set -eu

REPO="psimaker/vaultsync"
HUB_DIR="${VAULTSYNC_HUB_DIR:-/srv/vaultsync}"
HUB_NAME="${VAULTSYNC_HUB_NAME:-VaultSync Hub}"
HUB_IMAGE="${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.1.0}"
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

detect_asset() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
		Linux) goos="linux" ;;
		Darwin) goos="darwin" ;;
		*) fail "Unsupported OS: $os (Linux and macOS are supported today; Windows follows with the desktop app)." ;;
	esac
	case "$arch" in
		x86_64 | amd64) goarch="amd64" ;;
		aarch64 | arm64) goarch="arm64" ;;
		*) fail "Unsupported CPU architecture: $arch (prebuilt binaries cover amd64 and arm64)." ;;
	esac
	printf 'vaultsync-hub_%s_%s\n' "$goos" "$goarch"
}

latest_hub_tag() {
	curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" |
		grep -o '"tag_name": *"hub-v[^"]*"' |
		head -1 |
		sed 's/.*"\(hub-v[^"]*\)"/\1/'
}

# Download a release binary and verify it against the release's SHA256SUMS.
download_binary() {
	asset="$1"
	tag="$2"
	dest="$3"
	base="https://github.com/$REPO/releases/download/$tag"
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would download: $base/$asset -> $dest (verified against $base/SHA256SUMS)"
		return 0
	fi
	tmpdir=$(mktemp -d)
	trap 'rm -rf "$tmpdir"' EXIT
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
    image: ${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.1.0}
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

find_syncthing_config() {
	if [ -n "${SYNCTHING_CONFIG:-}" ]; then
		[ -r "$SYNCTHING_CONFIG" ] && { printf '%s\n' "$SYNCTHING_CONFIG"; return 0; }
		return 1
	fi
	for c in \
		"${XDG_STATE_HOME:-$HOME/.local/state}/syncthing/config.xml" \
		"${XDG_CONFIG_HOME:-$HOME/.config}/syncthing/config.xml" \
		"$HOME/Library/Application Support/Syncthing/config.xml"; do
		[ -r "$c" ] && { printf '%s\n' "$c"; return 0; }
	done
	return 1
}

setup_device() {
	if config=$(find_syncthing_config); then
		info "✓ Syncthing found ($config)"
	else
		info ""
		info "This computer has no Syncthing yet. The VaultSync desktop app (which brings"
		info "its own) is on the way; until then install Syncthing and re-run this setup:"
		case "$(uname -s)" in
			Darwin) info "    brew install syncthing && brew services start syncthing" ;;
			*) info "    your package manager, e.g.  sudo apt install syncthing  then  systemctl --user enable --now syncthing" ;;
		esac
		info ""
		exit 0
	fi
	command -v curl >/dev/null 2>&1 || fail "curl is required."
	asset=$(detect_asset)
	tag=$(latest_hub_tag) || tag=""
	[ -n "$tag" ] || fail "Could not find a Hub release on GitHub ($REPO). Check your network."
	bin="${VAULTSYNC_BIN_DIR:-$HOME/.local/bin}/vaultsync-hub"
	download_binary "$asset" "$tag" "$bin"

	if [ -z "$CODE" ]; then
		CODE=$(ask "  Pairing code from your Hub (WORD-WORD-NN): " "") ||
			fail "No terminal to ask on. Re-run with --code WORD-WORD-NN."
	fi
	[ -n "$CODE" ] || fail "A pairing code is required. Get one on the Hub: vaultsync-hub code"
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would run: $bin pair --code $CODE"
		return 0
	fi
	"$bin" pair --code "$CODE"
}

case "$CHOICE" in
	1) setup_device ;;
	2) setup_hub ;;
esac

if [ "$DRY_RUN" = 1 ]; then
	info ""
	info "Dry run complete — nothing was changed."
fi

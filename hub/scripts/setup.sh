#!/usr/bin/env sh
# VaultSync setup — one link for every device.
#
#   curl -fsSL https://vaultsync.eu/setup.sh | sh
#
# It asks one question:
#   1) Obsidian device   this computer edits notes and syncs them with your Hub:
#                        installs VaultSync for Mac and Linux (`vaultsync`, it
#                        brings its own Syncthing) and runs its setup — on a
#                        Mac as the app VaultSync for Mac, in Applications
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
#                   VAULTSYNC_APPLICATIONS_DIR  the Mac's /Applications (tests)
set -eu

REPO="psimaker/vaultsync"
HUB_DIR="${VAULTSYNC_HUB_DIR:-/srv/vaultsync}"
HUB_NAME="${VAULTSYNC_HUB_NAME:-VaultSync Hub}"
HUB_IMAGE="${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.3.0}"
# Captured: an exported value outranks .env in Compose, so left in the
# environment it would decide the image of an existing Hub over its .env —
# and over a "no" to the offer below (#216).
unset VAULTSYNC_HUB_IMAGE
RELAY_URL="${RELAY_URL:-https://relay.vaultsync.eu}"
# VaultSync for Mac (decision 053): the app holds the agent and the menu-bar
# app; the agent's setup, run from the app in Applications, makes the
# background service run from it and the menu-bar app open at login.
APP_NAME="VaultSync for Mac.app"
APP_ID="eu.vaultsync.desktop"
SYSTEM_APPLICATIONS="${VAULTSYNC_APPLICATIONS_DIR:-/Applications}"
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
	case "$1" in "" | *[!0-9.]*) return 1 ;; esac # one line of digits and dots, nothing else
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

# plain_env_file FILE: a regular file of blank lines, comments and
# NAME=value lines without quotes — the shape setup writes — with
# VAULTSYNC_HUB_IMAGE exactly once. Anything else (a quoted value may span
# lines, a link may lead elsewhere) is not edited.
plain_env_file() {
	[ -f "$1" ] && [ ! -L "$1" ] &&
		! grep -Eqv '^([[:space:]]*(#.*)?|[A-Za-z_][A-Za-z0-9_]*=[^"'"'"']*)$' "$1" &&
		[ "$(grep -c '^VAULTSYNC_HUB_IMAGE=' "$1")" = 1 ]
}

# The edit of .env holds a lock from the first read to the rename, so two
# setups cannot read the same old line and write in turns. release_env_edit
# removes the lock and the temporary files; the traps run it on exit and on
# an interruption, which then ends the setup (129/130/143).
ENV_EDIT_LOCK=""
ENV_EDIT_TMP=""
ENV_EDIT_OUT=""
release_env_edit() {
	for f in "$ENV_EDIT_TMP" "$ENV_EDIT_OUT"; do
		[ -z "$f" ] || rm -f "$f" 2>/dev/null || true
		[ -z "$f" ] || [ ! -e "$f" ] || warn "Could not remove the temporary file $f."
	done
	[ -z "$ENV_EDIT_LOCK" ] || rmdir "$ENV_EDIT_LOCK" 2>/dev/null || true
	[ -z "$ENV_EDIT_LOCK" ] || [ ! -d "$ENV_EDIT_LOCK" ] || warn "Could not remove the lock $ENV_EDIT_LOCK."
	ENV_EDIT_TMP=""
	ENV_EDIT_OUT=""
	ENV_EDIT_LOCK=""
}

env_image_line() {
	sed -n 's/^VAULTSYNC_HUB_IMAGE=//p' "$1"
}

# replace_env_image FILE EXPECTED: the VAULTSYNC_HUB_IMAGE line becomes
# $HUB_IMAGE. A snapshot of FILE (mktemp reserves the names; cp -p keeps the
# mode) is checked as a whole — plain shape, the image line still EXPECTED —
# and transformed into a second file whose image line is checked in turn;
# that file takes FILE's place in one rename, so FILE is whole at every
# instant. Returns 2 when the snapshot is not what was offered, 1 when a
# step fails; what it created is removed by release_env_edit, which reports
# anything it could not remove.
replace_env_image() {
	file="$1"
	expected="$2"
	ENV_EDIT_TMP=$(mktemp "$file.setup.XXXXXX") || return 1
	ENV_EDIT_OUT=$(mktemp "$file.setup.XXXXXX") || return 1
	cp -p "$file" "$ENV_EDIT_TMP" || return 1
	plain_env_file "$ENV_EDIT_TMP" || return 2
	[ "$(env_image_line "$ENV_EDIT_TMP")" = "$expected" ] || return 2
	# EXPECTED is an official image name (checked above): only its dots are
	# special in a pattern.
	literal=$(printf '%s' "$expected" | sed 's/\./\\./g')
	cp -p "$ENV_EDIT_TMP" "$ENV_EDIT_OUT" || return 1
	sed "s|^VAULTSYNC_HUB_IMAGE=$literal\$|VAULTSYNC_HUB_IMAGE=$HUB_IMAGE|" "$ENV_EDIT_TMP" >"$ENV_EDIT_OUT" || return 1
	[ "$(env_image_line "$ENV_EDIT_OUT")" = "$HUB_IMAGE" ] || return 1
	mv -f "$ENV_EDIT_OUT" "$file" || return 1
	ENV_EDIT_OUT=""
	# The snapshot stays tracked: release_env_edit removes it and reports
	# a removal that fails.
	return 0
}

# The hint names the shipped release as a target only on condition — the
# callers that could not read .env do not know what it names today.
manual_image_hint() {
	info "  To move by hand: if $HUB_DIR/.env names an older vaultsync-hub release than $new_ver,"
	info "  change its VAULTSYNC_HUB_IMAGE line to VAULTSYNC_HUB_IMAGE=$HUB_IMAGE (that one line),"
	info "  then run docker compose pull && docker compose up -d there (docs/hub.md → Update the Hub)."
}

# An existing Hub stays on the image its .env names — re-running the setup
# never moves it by itself. When that is an older official release than the
# one this setup ships, the move is offered: only on a terminal, only with
# consent, never for a custom image, never a downgrade, and only on an .env
# in the plain shape setup writes, so the one line changed is the one that
# was read — the whole file checked again after the answer (#216). Declined,
# without a terminal or on any other .env, the manual step is named instead.
offer_newer_hub_image() {
	env_file="$1"
	official="ghcr.io/psimaker/vaultsync-hub:"
	case "$HUB_IMAGE" in
		"$official"*) ;;
		*) return 0 ;; # this setup ships a custom image: nothing to offer
	esac
	new_ver=${HUB_IMAGE#"$official"}
	is_release "$new_ver" || return 0
	if [ "$DRY_RUN" = 1 ]; then
		offer_newer_hub_image_locked "$env_file"
		return 0
	fi
	lock="$env_file.setup-lock"
	if ! mkdir "$lock" 2>/dev/null; then
		if [ -d "$lock" ]; then
			info "Another setup may be editing $env_file: the lock $lock exists. This one leaves .env as it is."
			info "  If no other setup is running, remove that directory and run the setup again."
			manual_image_hint
			return 0
		fi
		fail "Could not create the lock $lock next to $env_file; nothing was changed."
	fi
	ENV_EDIT_LOCK="$lock"
	trap 'release_env_edit' EXIT
	trap 'release_env_edit; exit 129' HUP
	trap 'release_env_edit; exit 130' INT
	trap 'release_env_edit; exit 143' TERM
	offer_newer_hub_image_locked "$env_file"
	release_env_edit
	trap - EXIT HUP INT TERM
	return 0
}

offer_newer_hub_image_locked() {
	env_file="$1"
	if ! plain_env_file "$env_file"; then
		info "Your Hub's .env has a shape setup does not edit (a quoted value, a symbolic link, or VAULTSYNC_HUB_IMAGE more than once)."
		manual_image_hint
		return 0
	fi
	current=$(env_image_line "$env_file")
	case "$current" in
		"" | "$HUB_IMAGE") return 0 ;;
		"$official"*) ;;
		*) info "Your Hub runs a custom image ($current); setup leaves it as it is."; return 0 ;;
	esac
	cur_ver=${current#"$official"}
	if ! is_release "$cur_ver"; then
		info "Your Hub's VAULTSYNC_HUB_IMAGE line has a shape setup does not read (a comment, spaces or Windows line endings after the image?); setup leaves it as it is."
		manual_image_hint
		return 0
	fi
	version_newer "$new_ver" "$cur_ver" || return 0
	info "Your Hub runs vaultsync-hub $cur_ver; this setup ships $new_ver."
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would offer to set VAULTSYNC_HUB_IMAGE=$HUB_IMAGE in $env_file"
		return 0
	fi
	if answer=$(ask "  Move it to $new_ver now? Vaults, devices, ports and names carry over; a new pairing code is printed at the end, as always. [y/N] " "n"); then
		case "$answer" in
			[yY]*)
				# As a condition, so that set -e does not end the script on a
				# failed step before it is reported.
				if replace_env_image "$env_file" "$current"; then
					info "  .env now names $HUB_IMAGE; the stack restarts on it below."
					return 0
				elif [ $? -eq 2 ]; then
					info "  $env_file changed while setup was asking; nothing was changed. Check its VAULTSYNC_HUB_IMAGE line, then run the setup again."
					return 0
				fi
				fail "Could not update $env_file; nothing was changed. Change its VAULTSYNC_HUB_IMAGE line by hand, then run docker compose pull && docker compose up -d."
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

# bundle_id APP prints the bundle identifier at the top of APP's
# Info.plist — nothing when there is none or it cannot be read: such a
# bundle is never taken for VaultSync for Mac. plutil reads any plist, and
# only its top level (a nested CFBundleIdentifier — a document type's — is
# not the bundle's); without plutil (a Linux test run) the same is read
# from an XML plist, counting dictionary levels.
bundle_id() {
	plist="$1/Contents/Info.plist"
	[ -f "$plist" ] || return 0
	if command -v plutil >/dev/null 2>&1; then
		plutil -extract CFBundleIdentifier raw -expect string -o - "$plist" 2>/dev/null || true
		return 0
	fi
	awk '
		{
			line = $0
			while (match(line, /<[^>]*>/)) {
				tag = substr(line, RSTART, RLENGTH)
				line = substr(line, RSTART + RLENGTH)
				if (tag == "<dict>") { depth++; want = 0 }
				else if (tag == "</dict>") { depth--; want = 0 }
				else if (tag == "<key>") {
					end = index(line, "</key>")
					want = (end > 0 && depth == 1 && substr(line, 1, end - 1) == "CFBundleIdentifier")
					if (end > 0) line = substr(line, end + 6)
				} else if (tag == "<string>" && want) {
					end = index(line, "</string>")
					if (end > 0) print substr(line, 1, end - 1)
					exit
				} else want = 0
			}
		}
	' "$plist"
}

# replaceable APP: this account can put another VaultSync for Mac in APP's
# place and remove APP — APP's folder and the bundle's own folders are
# writable to it (another account's app is not, nor one root installed).
replaceable() {
	[ -w "$(dirname -- "$1")" ] || return 1
	# (sh has no local variables: app_dir's loop runs around this one.)
	for part in "$1" "$1/Contents" "$1/Contents/MacOS" "$1/Contents/_CodeSignature"; do
		[ ! -e "$part" ] || [ -w "$part" ] || return 1
	done
}

# app_dir prints the folder VaultSync for Mac goes into: the one it already
# is in (Applications first) when this account can replace it there, else
# Applications when this account may write there and the name is free,
# else ~/Applications.
app_dir() {
	for d in "$SYSTEM_APPLICATIONS" "$HOME/Applications"; do
		if [ -e "$d/$APP_NAME" ] || [ -L "$d/$APP_NAME" ]; then
			if [ -L "$d/$APP_NAME" ] || replaceable "$d/$APP_NAME"; then
				printf '%s\n' "$d"
				return 0
			fi
		fi
	done
	if [ -d "$SYSTEM_APPLICATIONS" ] && [ -w "$SYSTEM_APPLICATIONS" ] &&
		[ ! -e "$SYSTEM_APPLICATIONS/$APP_NAME" ] && [ ! -L "$SYSTEM_APPLICATIONS/$APP_NAME" ]; then
		printf '%s\n' "$SYSTEM_APPLICATIONS"
	else
		printf '%s\n' "$HOME/Applications"
	fi
}

# check_app_place APP stops the setup unless APP is free or holds VaultSync
# for Mac: a bundle there with another identifier (or none that can be
# read), or a link, is someone else's and is never replaced (doctrine 002).
check_app_place() {
	if [ -L "$1" ]; then
		fail "$1 is a link, not VaultSync for Mac — it was not replaced. Move it elsewhere, then run the setup again."
	fi
	[ -e "$1" ] || return 0
	id=$(bundle_id "$1")
	[ "$id" = "$APP_ID" ] ||
		fail "$1 is not VaultSync for Mac (its bundle identifier is ${id:-not readable}) — it was not replaced. Move it elsewhere, then run the setup again."
}

# The app being replaced, set aside beside it until the new one is in
# place (REPLACED), and whether the new one is on its way there (PLACING):
# a move across disks copies, and a full disk, an I/O error, Ctrl-C, a
# closed Terminal or a logout can stop it halfway. restore_app — run by
# the traps and by a failed move — then removes whatever the move left at
# the app's place (while PLACING is set, nothing else can be there: the
# installed app was set aside, or there was none) and puts the set-aside
# app back, so the background service and the menu-bar app never lose
# their program to an interrupted update. Once the new app is in place,
# it removes what was set aside.
REPLACED=""
PLACING=""
restore_app() {
	if [ -n "$PLACING" ]; then
		rm -rf "$APP" 2>/dev/null || true
		if [ -n "$REPLACED" ] && [ -e "$REPLACED" ]; then
			mv "$REPLACED" "$APP" 2>/dev/null || true
		fi
		PLACING=""
		return 0
	fi
	if [ -n "$REPLACED" ] && [ -e "$REPLACED" ]; then
		rm -rf "$REPLACED" 2>/dev/null || true
	fi
}

# same_app A B: the two bundles hold the same programs, byte for byte.
same_app() {
	for prog in vaultsync vaultsync-tray; do
		cmp -s "$1/Contents/MacOS/$prog" "$2/Contents/MacOS/$prog" || return 1
	done
}

# install_app ZIP DIR WORKDIR unpacks VaultSync for Mac from the verified
# ZIP into WORKDIR, runs its agent once (one that cannot run on this Mac
# never takes the place of one that can), and moves it into DIR — where it
# replaces only VaultSync for Mac (check_app_place), and not at all when
# that one is the same build. Sets APP to the app the setup then runs.
install_app() {
	command -v unzip >/dev/null 2>&1 || fail "unzip is required to install VaultSync for Mac — nothing was installed."
	mkdir "$3/unpacked"
	unzip -q "$1" -d "$3/unpacked" || fail "Could not unpack $(basename -- "$1") — nothing was installed."
	new="$3/unpacked/$APP_NAME"
	if [ -L "$new" ] || [ ! -d "$new" ] || [ "$(bundle_id "$new")" != "$APP_ID" ] || [ ! -x "$new/Contents/MacOS/vaultsync" ]; then
		fail "The download does not hold VaultSync for Mac — nothing was installed."
	fi
	APP="$2/$APP_NAME"
	check_app_place "$APP"
	"$new/Contents/MacOS/vaultsync" version >/dev/null 2>&1 ||
		fail "The downloaded VaultSync for Mac does not run on this Mac — nothing was changed; a VaultSync already installed keeps running."
	if [ -e "$APP" ] && same_app "$APP" "$new"; then
		return 0
	fi
	if [ -e "$APP" ]; then
		REPLACED="$2/.$APP_NAME.replaced.$$"
		mv "$APP" "$REPLACED" || {
			REPLACED=""
			fail "Could not move the installed VaultSync for Mac aside — nothing was changed."
		}
	fi
	mkdir -p "$2"
	PLACING=1
	if ! mv "$new" "$APP"; then
		restore_app
		if [ -n "$REPLACED" ]; then
			fail "Could not install VaultSync for Mac into $2 — the one installed before stays."
		fi
		fail "Could not install VaultSync for Mac into $2 — nothing was installed."
	fi
	PLACING=""
	if [ -n "$REPLACED" ]; then
		rm -rf "$REPLACED" || warn "The replaced VaultSync for Mac stays at $REPLACED — remove it by hand."
		REPLACED=""
	fi
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
    image: ${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.3.0}
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
# ~/.local/bin/vaultsync. On a Mac whose release has the app
# (VaultSync_darwin_<arch>.zip, from desktop-v0.2.0 on), VaultSync for Mac
# goes into Applications and the setup runs from there — the background
# service then runs from the app, and its menu-bar app opens at login.
# Otherwise the downloaded agent is only the installer and is removed
# afterwards. Arguments after -- go to `vaultsync setup`.
setup_device() {
	command -v curl >/dev/null 2>&1 || fail "curl is required."
	asset=$(detect_asset vaultsync)
	tag=$(latest_stable_tag desktop-v) || tag=""
	[ -n "$tag" ] || fail "Could not find a VaultSync release for Mac and Linux on GitHub ($REPO). Check your network."
	app_asset=""
	case "$asset" in
		vaultsync_darwin_*)
			sums=$(curl -fsSL "https://github.com/$REPO/releases/download/$tag/SHA256SUMS") ||
				fail "Could not fetch SHA256SUMS for $tag — nothing was installed."
			candidate="VaultSync_darwin_${asset##*_}.zip"
			if printf '%s\n' "$sums" | awk -v a="$candidate" '$2 == a { f = 1 } END { exit !f }'; then
				app_asset="$candidate"
			fi
			;;
	esac
	info "VaultSync for Mac and Linux ${tag#desktop-v} (${app_asset:-$asset})"
	if find_syncthing_config; then
		info "  Syncthing is already set up on this computer. VaultSync runs its own and never"
		info "  touches yours. To pair your own Syncthing with your Hub instead, see"
		info "  https://github.com/$REPO/blob/main/docs/hub.md (vaultsync-hub pair)."
	fi
	set -- setup ${CODE:+--code "$CODE"} "$@"
	if [ "$DRY_RUN" = 1 ]; then
		program="<temporary folder>/vaultsync"
		if [ -n "$app_asset" ]; then
			dir=$(app_dir)
			check_app_place "$dir/$APP_NAME"
			download_binary "$app_asset" "$tag" "<temporary folder>/$app_asset" ""
			if [ -e "$dir/$APP_NAME" ]; then
				info "[dry-run] would replace VaultSync for Mac in $dir"
			else
				info "[dry-run] would install VaultSync for Mac into $dir"
			fi
			program="$dir/$APP_NAME/Contents/MacOS/vaultsync"
		else
			download_binary "$asset" "$tag" "$program" ""
		fi
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
		info "[dry-run] would run: $program$shown"
		return 0
	fi
	installer_dir=$(mktemp -d "${TMPDIR:-/tmp}/vaultsync-setup.XXXXXXXX")
	# The download is gone however this ends — also on Ctrl-C or a kill —
	# and an app set aside for its update is back in its place unless the
	# new one took it.
	trap 'restore_app; rm -rf "$installer_dir"' EXIT
	trap 'restore_app; rm -rf "$installer_dir"; exit 129' HUP
	trap 'restore_app; rm -rf "$installer_dir"; exit 130' INT
	trap 'restore_app; rm -rf "$installer_dir"; exit 143' TERM
	mkdir "$installer_dir/download"
	if [ -n "$app_asset" ]; then
		download_binary "$app_asset" "$tag" "$installer_dir/$app_asset" "$installer_dir/download"
		install_app "$installer_dir/$app_asset" "$(app_dir)" "$installer_dir"
		info "VaultSync for Mac is in ${APP%/*}."
		info ""
		"$APP/Contents/MacOS/vaultsync" "$@"
		return
	fi
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

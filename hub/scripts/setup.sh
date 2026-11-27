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
HUB_IMAGE="${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.3.1}"
# Captured: an exported value outranks .env in Compose, so left in the
# environment it would decide the image of an existing Hub over its .env —
# and over a "no" to the offer below (#216).
unset VAULTSYNC_HUB_IMAGE
# The same for the line of Syncthing a Hub follows (#301): .env decides it.
unset VAULTSYNC_HUB_SYNCTHING_IMAGE
SYNCTHING_IMAGE_NEW="syncthing/syncthing:2"
SYNCTHING_IMAGE_KEPT="syncthing/syncthing:1"
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
# (Never fatal: a warning a closed Terminal cannot show must not stop the
# cleanup that comes after it under set -e.)
warn() { printf 'WARN: %s\n' "$*" >&2 || true; }
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
# setups cannot read the same old line and write in turns. On an existing
# Hub the new .env and the new compose file are staged beside the files in
# place and take their places only after the images they name are pulled
# (#368): a pull that fails or is interrupted leaves both as they were.
# release_env_edit removes the lock and every temporary and staged file; the
# traps run it on exit and on an interruption, which then ends the setup
# (129/130/143).
ENV_EDIT_LOCK=""
ENV_EDIT_TMP=""
ENV_EDIT_OUT=""
# Set only once stage_env_image has finished: ENV_EDIT_OUT alone may name a
# file a failed or refused staging left half made.
ENV_STAGED=""
COMPOSE_STAGED=""
COMPOSE_OVERRIDE=""
HUB_EXISTED=""
release_env_edit() {
	for f in "$ENV_EDIT_TMP" "$ENV_EDIT_OUT" "$COMPOSE_STAGED"; do
		[ -z "$f" ] || rm -f "$f" 2>/dev/null || true
		[ -z "$f" ] || [ ! -e "$f" ] || warn "Could not remove the temporary file $f."
	done
	[ -z "$ENV_EDIT_LOCK" ] || rmdir "$ENV_EDIT_LOCK" 2>/dev/null || true
	[ -z "$ENV_EDIT_LOCK" ] || [ ! -d "$ENV_EDIT_LOCK" ] || warn "Could not remove the lock $ENV_EDIT_LOCK."
	ENV_EDIT_TMP=""
	ENV_EDIT_OUT=""
	ENV_STAGED=""
	COMPOSE_STAGED=""
	ENV_EDIT_LOCK=""
}

# compose_files_nameable DIR: setup can name the files Compose reads for
# the Hub in DIR — docker-compose.yml and at most one override, which it
# leaves in COMPOSE_OVERRIDE — so a pull given them with -f reads what the
# later `up -d` reads (#368). Not when COMPOSE_FILE lists the files (in the
# environment, or any line of .env that names it — an .env setup cannot
# read counts, Compose may read it through sudo), when COMPOSE_ENV_FILES or
# COMPOSE_DISABLE_ENV_FILE has Compose read other env files than .env, when
# another base file lies beside it (Compose may prefer it), or when more
# than one override does.
compose_files_nameable() {
	COMPOSE_OVERRIDE=""
	[ -z "${COMPOSE_FILE:-}" ] || return 1
	[ -z "${COMPOSE_ENV_FILES:-}" ] || return 1
	[ -z "${COMPOSE_DISABLE_ENV_FILE:-}" ] || return 1
	env_grep=0
	grep -qs 'COMPOSE_FILE' "$1/.env" || env_grep=$?
	[ "$env_grep" -eq 1 ] || return 1
	for f in compose.yaml compose.yml docker-compose.yaml; do
		if [ -e "$1/$f" ] || [ -L "$1/$f" ]; then
			return 1
		fi
	done
	# An override Compose loads is a file. A link to nothing it skips, and
	# so does setup; anything else under the name setup cannot name.
	for f in compose.override.yml compose.override.yaml docker-compose.override.yml docker-compose.override.yaml; do
		[ -e "$1/$f" ] || continue
		[ -f "$1/$f" ] || return 1
		[ -z "$COMPOSE_OVERRIDE" ] || return 1
		COMPOSE_OVERRIDE="$1/$f"
	done
	return 0
}

# same_text A B: both files can be read and hold the same text. cat only: a
# NAS's shell may have no cmp.
same_text() {
	same_text_a=$(cat "$1" && echo .) || return 1
	same_text_b=$(cat "$2" && echo .) || return 1
	[ "$same_text_a" = "$same_text_b" ]
}

hold_hub_edits() {
	trap 'release_env_edit' EXIT
	trap 'interrupted_hub_edits 129' HUP
	trap 'interrupted_hub_edits 130' INT
	trap 'interrupted_hub_edits 143' TERM
}

# An interruption ends the setup: what was staged goes with the lock, and
# setup says what that leaves — after the cleanup and never fatally, since a
# closed terminal cannot show it. Interrupted before anything was staged
# (while asking), there is nothing to say.
interrupted_hub_edits() {
	if [ -n "$COMPOSE_STAGED" ]; then
		interrupted_note="Setup was interrupted. Nothing was changed: $HUB_DIR/.env and docker-compose.yml are as they were."
	elif [ -n "$ENV_STAGED" ]; then
		interrupted_note="Setup was interrupted. $HUB_DIR/.env is as it was; docker-compose.yml was written before the pull."
	else
		interrupted_note=""
	fi
	release_env_edit
	if [ -n "$interrupted_note" ]; then
		printf '%s\n  Run the setup again: it brings the whole stack (docs/hub.md → Update the Hub).\n' "$interrupted_note" >&2 || true
	fi
	exit "$1"
}

end_hub_edits() {
	release_env_edit
	trap - EXIT HUP INT TERM
}

env_image_line() {
	sed -n 's/^VAULTSYNC_HUB_IMAGE=//p' "$1"
}

# stage_env_image FILE EXPECTED: a second file whose VAULTSYNC_HUB_IMAGE
# line is $HUB_IMAGE, left in ENV_EDIT_OUT. A snapshot of FILE (mktemp
# reserves the names; cp -p keeps the mode) is checked as a whole — plain
# shape, the image line still EXPECTED — and transformed into that file,
# whose image line is checked in turn. It takes FILE's place in one rename
# once its images are pulled (install_staged_hub_files), so FILE is whole
# at every instant. Returns 2 when the snapshot is not what was offered, 1
# when a step fails; what it created is removed by release_env_edit, which
# reports anything it could not remove.
stage_env_image() {
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
	# Both stay tracked: the snapshot to check FILE against before the
	# rename, the staged file to rename; release_env_edit removes what is
	# left and reports a removal that fails.
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
	# Compose told to read other env files reads neither the .env setup
	# would edit nor the staged one the pull would read (#368).
	if [ -n "${COMPOSE_ENV_FILES:-}" ] || [ -n "${COMPOSE_DISABLE_ENV_FILE:-}" ]; then
		info "Compose reads other env files than $env_file here (COMPOSE_ENV_FILES or COMPOSE_DISABLE_ENV_FILE), so setup does not move the Hub's image."
		info "  If your Hub runs an older vaultsync-hub release than $new_ver, set VAULTSYNC_HUB_IMAGE=$HUB_IMAGE where Compose reads it,"
		info "  then run docker compose pull && docker compose up -d there (docs/hub.md → Update the Hub)."
		return 0
	fi
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
	hold_hub_edits
	offer_newer_hub_image_locked "$env_file"
	# A staged .env keeps the lock and the traps until it takes its place
	# after the pull, or is removed with the lock.
	[ -n "$ENV_STAGED" ] || end_hub_edits
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
				if stage_env_image "$env_file" "$current"; then
					ENV_STAGED=1
					info "  .env will name $HUB_IMAGE once its images are pulled below."
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

# check_macos stops on a Mac older than macOS 12, before anything is looked up
# or downloaded: VaultSync for Mac (LSMinimumSystemVersion 12.0) and the bare
# agent (Go's floor) do not run there, and macOS 11's plutil cannot read the
# app's identifier, so the setup would call a valid download not VaultSync
# for Mac. It refuses only on proof: a major version of 0 to 11. No sw_vers,
# a failed one or a version it cannot read lets the setup go on — the agent
# and macOS still refuse what they cannot run. SYSTEM_VERSION_COMPAT=0
# overrides an exported 1, with which macOS 11 and later answer 10.16; that
# answer is no version of its own and lets the setup go on, whatever turned
# compatibility mode on. Nothing is compared as a number, so no answer can
# make a comparison fail.
check_macos() {
	command -v sw_vers >/dev/null 2>&1 || return 0
	macos=$(SYSTEM_VERSION_COMPAT=0 sw_vers -productVersion 2>/dev/null) || macos=""
	case "$macos" in
		10.16 | 10.16.*) return 0 ;;
	esac
	case "${macos%%.*}" in
		[0-9] | 1[01])
			fail "VaultSync for Mac needs macOS 12 or newer; this Mac has macOS $macos. Nothing was installed."
			;;
	esac
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
# bundle is never taken for VaultSync for Mac. plutil (on every Mac, the
# one place this runs for real) reads any plist and only its top level: a
# nested CFBundleIdentifier — a document type's — is not the bundle's.
# Without plutil nothing is read (the Linux test run brings its own).
bundle_id() {
	plist="$1/Contents/Info.plist"
	[ -f "$plist" ] || return 0
	command -v plutil >/dev/null 2>&1 || return 0
	plutil -extract CFBundleIdentifier raw -expect string -o - "$plist" 2>/dev/null || true
}

# own_app APP: APP is this account's — its owner (another account's app is
# not, nor one root installed; one locked in the Finder still is): the
# agent's rule too (checkApp). find -user takes the account's number on BSD
# and GNU alike.
own_app() {
	[ -n "$(find "$1" -maxdepth 0 -user "$(id -u)" 2>/dev/null)" ]
}

# unlocked APP: this account may change the bundle's own folders — one
# locked in the Finder (Get Info → Locked) it may not.
unlocked() {
	# (sh has no local variables: app_dir's loop runs around this one.)
	for part in "$1" "$1/Contents" "$1/Contents/MacOS" "$1/Contents/_CodeSignature"; do
		[ ! -e "$part" ] || [ -w "$part" ] || return 1
	done
}

# replaceable APP: this account can put another VaultSync for Mac in APP's
# place and remove APP — its own, unlocked, in a folder it may change.
replaceable() {
	[ -w "$(dirname -- "$1")" ] && own_app "$1" && unlocked "$1"
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
			# This account's app that cannot be replaced where it is: the
			# background service runs from it, and a second app elsewhere
			# would take the service away from the one the person knows
			# (decision 053).
			if own_app "$d/$APP_NAME"; then
				unlocked "$d/$APP_NAME" ||
					fail "VaultSync for Mac in $d is locked (Finder → Get Info → Locked). Unlock it, then run the setup again."
				[ "$d" != "$HOME/Applications" ] ||
					fail "VaultSync for Mac in $d cannot be replaced: this account may not change that folder. Give it write access again (Finder → Get Info → Sharing & Permissions), then run the setup again."
				fail "VaultSync for Mac in $d is yours, but only an administrator may change that folder, so the setup link cannot update it there. Move it to $HOME/Applications in the Finder (it asks for an administrator's password), then run the setup again."
			fi
		fi
	done
	if [ -d "$SYSTEM_APPLICATIONS" ] && [ -w "$SYSTEM_APPLICATIONS" ] &&
		[ ! -e "$SYSTEM_APPLICATIONS/$APP_NAME" ] && [ ! -L "$SYSTEM_APPLICATIONS/$APP_NAME" ]; then
		printf '%s\n' "$SYSTEM_APPLICATIONS"
	elif [ -e "$HOME/Applications/$APP_NAME" ]; then
		# There, and not this account's to replace (the loop above): the app
		# the agent would pass over too (checkApp).
		fail "VaultSync for Mac in $HOME/Applications cannot be replaced by this account — it may be locked (Finder → Get Info → Locked) or belong to another account. Unlock it, or move it to the Trash, then run the setup again."
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

# inode_of PATH prints the inode number of PATH itself (a link's own, not
# what it leads to): ls -di, which BSD and GNU share (find -printf and stat
# do not). The number leads the first line, whatever the name holds. It
# fails, printing nothing, when that cannot be read as a number.
inode_of() {
	# shellcheck disable=SC2012 # ls -di is the one inode reading BSD and GNU share
	inode=$(ls -di -- "$1" 2>/dev/null | awk 'NR == 1 { print $1 }') || return 1
	case "$inode" in
		"" | *[!0-9]*) return 1 ;;
	esac
	printf '%s\n' "$inode"
}

# own_app_at_place: what stands at APP is the app this run unpacked. Moving
# it into place is one rename, never a copy (it is unpacked beside its
# place), and a rename keeps the inode, which install_app reads before the
# move (NEW_INODE). An inode that cannot be read, there or here, is not
# this run's: such an app is never removed (#268).
own_app_at_place() {
	{ [ -n "$NEW_INODE" ] && [ -d "$APP" ] && [ ! -L "$APP" ]; } || return 1
	at_place=$(inode_of "$APP") || return 1
	[ "$at_place" = "$NEW_INODE" ]
}

# Where an update of the app stands (STAGE), set before each step: aside
# — the installed app is being set aside as REPLACED, in a folder of its
# own beside it (ASIDE, new for this run, so the name is free); placing —
# the new app is being moved into its place; restoring — the set-aside app
# is being moved back; placed — the new one is in place. Each step is one
# rename; a failed one, Ctrl-C, a closed Terminal or a logout can stop the
# update between any two of them — restore_app too, which a second signal
# then enters again. restore_app — run by the traps and by a failed move —
# undoes what the stage leaves, deciding from what is on disk. Before
# placed it removes the app at the place only when it is this run's own
# (own_app_at_place): what another setup at the same time, or the person,
# put there stays, and is named (decision 053 (6), #268). The set-aside app
# goes back only into an empty place; where it cannot, it stays where it
# was set aside, and is named. A first install leaves nothing. restoring
# only finishes that move, so it never removes the app it put back; once
# placed, the set-aside app goes. It clears STAGE, so the EXIT trap after
# another trap undoes nothing twice. The background service and the
# menu-bar app never lose their program to an interrupted update.
STAGE=""
ASIDE=""
REPLACED=""
UNPACKED=""
NEW_INODE=""
restore_app() {
	case "$STAGE" in
		aside | placing)
			if [ -n "$REPLACED" ] && { [ -e "$REPLACED" ] || [ -L "$REPLACED" ]; }; then
				set_aside=1
			else
				set_aside=""
			fi
			if [ "$STAGE" = aside ] && [ -z "$set_aside" ]; then
				# The set-aside did not happen (it failed, or the signal came
				# first): what stands at the place is the app installed before,
				# as it was. Nothing to undo, nothing to say.
				:
			elif own_app_at_place; then
				rm -rf "$APP" 2>/dev/null || true
				if [ -e "$APP" ] || [ -L "$APP" ]; then
					warn "The VaultSync for Mac this setup was installing could not be removed from $APP — move it to the Trash."
				fi
			elif ! inode_of "$APP" >/dev/null && { [ -e "$APP" ] || [ -L "$APP" ]; }; then
				warn "Whether what is at $APP now is the VaultSync for Mac this setup was installing cannot be told — it was left as it is."
			elif [ -e "$APP" ] || [ -L "$APP" ]; then
				warn "Something else is at $APP now — another setup at the same time? It was left as it is."
				in_it=$(inode_of "$APP/$APP_NAME") || in_it=""
				if [ -n "$NEW_INODE" ] && [ "$in_it" = "$NEW_INODE" ]; then
					warn "The VaultSync for Mac this setup was installing is inside it, at $APP/$APP_NAME — move it to the Trash."
				fi
			fi
			if [ -n "$set_aside" ]; then
				STAGE=restoring
				put_back
			fi
			;;
		restoring)
			put_back
			;;
		placed)
			if [ -n "$ASIDE" ] && ! rm -rf "$ASIDE" 2>/dev/null; then
				warn "The replaced VaultSync for Mac stays at $ASIDE — move it to the Trash."
			fi
			;;
	esac
	[ -z "$ASIDE" ] || rmdir "$ASIDE" 2>/dev/null || true
	STAGE=""
}

# put_back moves what was set aside back to its place — only into an empty
# one: mv would move it into a folder standing there — and says where it is
# when it cannot, as what it is: the VaultSync for Mac installed before, or
# something else that took the place in the instant before the set-aside.
put_back() {
	{ [ -e "$REPLACED" ] || [ -L "$REPLACED" ]; } || return 0
	if [ ! -L "$REPLACED" ] && [ "$(bundle_id "$REPLACED")" = "$APP_ID" ]; then
		what="The VaultSync for Mac installed before"
	else
		what="What was set aside from $APP (it is not VaultSync for Mac)"
	fi
	if [ -e "$APP" ] || [ -L "$APP" ]; then
		warn "$what could not be put back, because $APP is not free. It is at $REPLACED — once $APP is free, move it there."
	elif ! mv "$REPLACED" "$APP" 2>/dev/null; then
		warn "$what could not be put back: it is at $REPLACED — move it to $APP."
	fi
}

# app_complete APP: APP holds what the agent requires before the service
# runs from it (checkApp in desktop/appbundle.go) — an XML Info.plist, the
# one kind the agent reads, and both programs as plain executable files — so
# the setup never installs an app the agent then passes over in silence.
app_complete() {
	[ "$(head -c 5 "$1/Contents/Info.plist" 2>/dev/null)" = "<?xml" ] || return 1
	for prog in vaultsync vaultsync-tray; do
		prog_path="$1/Contents/MacOS/$prog"
		{ [ -f "$prog_path" ] && [ ! -L "$prog_path" ] && [ -x "$prog_path" ]; } || return 1
	done
}

# same_app A B: the two bundles hold the same programs, byte for byte.
same_app() {
	for prog in vaultsync vaultsync-tray; do
		cmp -s "$1/Contents/MacOS/$prog" "$2/Contents/MacOS/$prog" || return 1
	done
}

# sh_quote WORD prints WORD as one word for sh: as it is when it holds only
# characters no shell treats specially, else in single quotes, a single
# quote in it as '\''. No sed: BSD sed ends its output with a newline the
# input did not have.
sh_quote() {
	case "$1" in
		"" | *[!A-Za-z0-9_./:=@%+,-]*) ;;
		*)
			printf '%s' "$1"
			return 0
			;;
	esac
	quote_rest=$1
	printf "'"
	while :; do
		case "$quote_rest" in
			*\'*)
				quote_part=${quote_rest%%\'*}
				printf "%s'\\\\''" "$quote_part"
				quote_rest=${quote_rest#*\'}
				;;
			*)
				printf "%s'" "$quote_rest"
				return 0
				;;
		esac
	done
}

# release_of LINE prints the release a `vaultsync version` line names —
# 0.3.0 for "vaultsync 0.3.0 (Syncthing 2.1.7)", 0.3.0-rc.1 for a release
# candidate's — and fails for any other line: a build of no release says
# "dev" or "dev-<commit>". The line is the agent's (desktop/main.go), and a
# test there holds it to this form (decision 080). That test carries this
# rule as a copy of its own (setupLinkRelease in desktop/issue277_test.go):
# the two move together.
release_of() {
	case "$1" in
		"vaultsync "*) ;;
		*) return 1 ;;
	esac
	rel=${1#vaultsync }
	rel=${rel%% *}
	case "$rel" in
		*-) return 1 ;;
		*-*)
			case "${rel#*-}" in *[!0-9A-Za-z.-]*) return 1 ;; esac
			;;
	esac
	is_release "${rel%%-*}" || return 1
	printf '%s\n' "$rel"
}

# keep_newer_app DIR LINE stops the setup, before anything is set aside,
# when the VaultSync for Mac at APP may be newer than the download, whose
# agent said LINE when asked its version (#277, decision 080). A release
# withdrawn after it reached this Mac would otherwise be replaced by the
# older one, and removed with the set-aside, though it cannot be downloaded
# again. The older agent would then refuse the engine the newer one set up
# (decision 054), and nothing would sync. Both apps are asked the same
# question; their x.y.z compare as three numbers, a suffix such as -rc.1
# aside, so another build of the same release is replaced. An app whose
# line names no release (a development build), or a download whose line
# names none, has no place in that order: nothing is replaced. An installed
# app whose agent is no plain executable file or is reached through a link
# — the app, Contents, Contents/MacOS or the agent itself (never run: a link
# may lead anywhere) — or whose agent does not run, is replaced as before:
# nothing runs from it, and the agent's checkApp passes over such an app.
# The command named for a newer app's own setup carries this run's
# arguments (SETUP_ARGS_QUOTED).
keep_newer_app() {
	have_agent="$APP/Contents/MacOS/vaultsync"
	for link_part in "$APP" "$APP/Contents" "$APP/Contents/MacOS" "$have_agent"; do
		[ ! -L "$link_part" ] || return 0
	done
	{ [ -f "$have_agent" ] && [ -x "$have_agent" ]; } || return 0
	have_line=$("$have_agent" version 2>/dev/null) || return 0
	have=$(release_of "$have_line") ||
		fail "VaultSync for Mac in $1 does not say which release it is (a development build, say), so the setup link cannot tell whether this release is newer — it was not replaced, and nothing was changed. To install this release, move it to the Trash, then run the setup link again."
	offered=$(release_of "$2") ||
		fail "The downloaded VaultSync for Mac does not say which release it is, so the setup link cannot tell whether it is newer than the one in $1 — that one was not replaced, and nothing was changed."
	if version_newer "${have%%-*}" "${offered%%-*}"; then
		fail "VaultSync for Mac in $1 is $have, newer than this release ($offered), so it was not replaced — nothing was changed. To set up a vault with it, run: $(sh_quote "$have_agent")$SETUP_ARGS_QUOTED"
	fi
}

# install_app ZIP DIR unpacks VaultSync for Mac from the verified ZIP into a
# folder of its own in DIR (UNPACKED, so that putting it in place is a
# rename, never a copy that can stop halfway), runs its agent once (one that
# cannot run on this Mac never takes the place of one that can), and moves
# it into DIR — where it replaces only VaultSync for Mac (check_app_place),
# and not at all when that one is the same build, a newer release or one
# whose release cannot be told (keep_newer_app). Sets APP to the app the
# setup then runs.
install_app() {
	command -v unzip >/dev/null 2>&1 || fail "unzip is required to install VaultSync for Mac — nothing was installed."
	mkdir -p "$2"
	UNPACKED=$(mktemp -d "$2/.$APP_NAME.new.XXXXXXXX") ||
		fail "Could not unpack VaultSync for Mac in $2 — nothing was installed."
	mkdir "$UNPACKED/unpacked"
	unzip -q "$1" -d "$UNPACKED/unpacked" || fail "Could not unpack $(basename -- "$1") — nothing was installed."
	new="$UNPACKED/unpacked/$APP_NAME"
	if [ -L "$new" ] || [ ! -d "$new" ] || [ "$(bundle_id "$new")" != "$APP_ID" ] || ! app_complete "$new"; then
		fail "The download does not hold VaultSync for Mac — nothing was installed."
	fi
	APP="$2/$APP_NAME"
	check_app_place "$APP"
	new_line=$("$new/Contents/MacOS/vaultsync" version 2>/dev/null) ||
		fail "The downloaded VaultSync for Mac does not run on this Mac — nothing was changed; a VaultSync already installed keeps running."
	if [ -e "$APP" ] && same_app "$APP" "$new"; then
		rm -rf "$UNPACKED"
		UNPACKED=""
		return 0
	fi
	if [ -e "$APP" ] || [ -L "$APP" ]; then
		# Again before the installed agent is asked its version: a bundle
		# that took the place while the download's agent ran is refused
		# before its own program runs.
		check_app_place "$APP"
		keep_newer_app "$2" "$new_line"
	fi
	# Read before anything moves: without it no app at the place is known to
	# be this run's, and none would be removed (own_app_at_place).
	NEW_INODE=$(inode_of "$new") ||
		fail "Could not read the downloaded VaultSync for Mac in $2 — nothing was changed; a VaultSync already installed keeps running."
	if [ -e "$APP" ]; then
		ASIDE=$(mktemp -d "$2/.$APP_NAME.replaced.XXXXXXXX") ||
			fail "Could not move the installed VaultSync for Mac aside — nothing was changed."
		# Again: something else may have taken the place since the look
		# above, while the installed agent ran (#268).
		check_app_place "$APP"
		REPLACED="$ASIDE/$APP_NAME"
		STAGE=aside
		mv "$APP" "$REPLACED" || {
			restore_app
			fail "Could not move the installed VaultSync for Mac aside — nothing was changed."
		}
		# And what was set aside: something else that took the place in the
		# instant after that look goes back (only into an empty place), and
		# is never replaced.
		if [ -L "$REPLACED" ] || [ "$(bundle_id "$REPLACED")" != "$APP_ID" ]; then
			restore_app
			fail "Something else was put at $APP while VaultSync for Mac was being installed — it was not replaced. Run the setup again once that is done."
		fi
	fi
	STAGE=placing
	if ! mv "$new" "$APP"; then
		restore_app
		if [ -n "$REPLACED" ] && { [ -e "$REPLACED" ] || [ -L "$REPLACED" ]; }; then
			fail "Could not install VaultSync for Mac into $2."
		elif [ -n "$REPLACED" ]; then
			fail "Could not install VaultSync for Mac into $2 — the one installed before stays."
		elif [ -e "$APP" ] || [ -L "$APP" ]; then
			fail "Could not install VaultSync for Mac into $2."
		fi
		fail "Could not install VaultSync for Mac into $2 — nothing was installed."
	fi
	if [ -e "$APP/$APP_NAME" ] || [ -L "$APP/$APP_NAME" ]; then
		# VaultSync for Mac holds no entry of its own name at its top, so
		# one there says the move went inside something that took the
		# app's place after the checks above — another setup at the same
		# time, say. The update stops, and the app set aside stays where
		# it is. Taken back out only when it carries this run's inode; one
		# that cannot be told to be this run's is left as it is. Nothing at that name: the move put
		# this app at the place, as it reported, and the update goes on —
		# also when the look at the place cannot tell whose the app there
		# is: one not known to be this run's is only never removed.
		nested=$(inode_of "$APP/$APP_NAME") || nested=""
		if [ -n "$nested" ] && [ "$nested" = "$NEW_INODE" ]; then
			# Taken out into a folder of its own beside the place — never
			# into the unpack folder, which the exit removes — and removed
			# only once it carries this run's inode there: what another hand
			# put at that name in the instant since the look stays there,
			# and is named.
			held=$(mktemp -d "$2/.$APP_NAME.held.XXXXXXXX") || held=""
			if [ -z "$held" ] || ! mv "$APP/$APP_NAME" "$held/$APP_NAME" 2>/dev/null; then
				[ -z "$held" ] || rmdir "$held" 2>/dev/null || true
				warn "The new VaultSync for Mac could not be taken back out of $APP — it is at $APP/$APP_NAME."
			elif [ "$(inode_of "$held/$APP_NAME" || echo none)" = "$NEW_INODE" ]; then
				rm -rf "$held"
			else
				warn "What was at $APP/$APP_NAME when this setup took its app back out is not known to be that app — it was kept at $held/$APP_NAME."
			fi
		else
			warn "What is at $APP/$APP_NAME is not known to be the VaultSync for Mac this setup was installing — it was left as it is."
		fi
		STAGE=""
		if [ -n "$REPLACED" ] && { [ -e "$REPLACED" ] || [ -L "$REPLACED" ]; }; then
			warn "The VaultSync for Mac installed before is at $REPLACED."
		fi
		fail "Something else was put at $APP while VaultSync for Mac was being installed — another setup running at the same time? It was left as it is; run the setup again once that one is done."
	fi
	STAGE=placed
	restore_app
	rm -rf "$UNPACKED"
	UNPACKED=""
	ASIDE=""
	REPLACED=""
	NEW_INODE=""
}

# --- 2) Hub ------------------------------------------------------------------

# syncthing_image_for_new_env: the Syncthing image a new .env names (#301,
# decision 068). 2.x converts a 1.x database on its first start, and 1.x
# then refuses to start on it — so 2.x only where no Syncthing has run in
# this Hub directory yet, or where 2.x already has. Anything setup cannot
# see into (Syncthing keeps its folder 0700, readable by its own user only)
# keeps 1.x: a wrong 1.x stops the Hub with a clear log line, a wrong 2.x
# converts a database for good.
syncthing_image_for_new_env() {
	st_dir="$HUB_DIR/syncthing"
	if [ -d "$st_dir/config/index-v2" ]; then
		echo "$SYNCTHING_IMAGE_NEW"
	elif [ -e "$st_dir/config" ] || [ -L "$st_dir/config" ]; then
		echo "$SYNCTHING_IMAGE_KEPT"
	elif { [ ! -e "$st_dir" ] && [ ! -L "$st_dir" ]; } ||
		{ [ -d "$st_dir" ] && [ ! -L "$st_dir" ] && [ -x "$st_dir" ]; }; then
		# No config under a folder setup can search (or none at all, in a
		# dry run, which creates nothing): no Syncthing has run here.
		echo "$SYNCTHING_IMAGE_NEW"
	else
		echo "$SYNCTHING_IMAGE_KEPT"
	fi
}

# The page section that explains the two lines, for every message about them.
SYNCTHING_LINE_DOC="https://github.com/psimaker/vaultsync/blob/main/docs/hub.md#which-syncthing-your-hub-runs"

kept_syncthing_note() {
	info "This Hub directory already holds a Syncthing that setup cannot tell to be 2.x: the new .env keeps it on $SYNCTHING_IMAGE_KEPT."
	info "  Why, and what to do if it runs 2.x: $SYNCTHING_LINE_DOC"
}

# A wrong 1.x on a 2.x home is the one way this goes wrong: 1.x refuses to
# start. The log shows it; this says what it means.
syncthing_start_hint() {
	info "  If the Syncthing log above says the config file version is newer than supported,"
	info "  this Hub's Syncthing is on 2.x: set VAULTSYNC_HUB_SYNCTHING_IMAGE=$SYNCTHING_IMAGE_NEW"
	info "  in $HUB_DIR/.env and run the setup again ($SYNCTHING_LINE_DOC)."
}

# The page section on moving a Hub to 2.x, for the messages about a
# conversion.
SYNCTHING_MOVE_DOC="https://github.com/psimaker/vaultsync/blob/main/docs/hub.md#move-your-hub-to-syncthing-2x"

# show_log SERVICE: the last 30 lines a container of the stack wrote, on
# stderr, without control characters — an escape sequence in a log line
# must not act on the terminal. Tabs and line ends stay.
show_log() {
	$DOCKER compose --project-directory "$HUB_DIR" logs --no-color --tail 30 "$1" 2>&1 |
		LC_ALL=C tr -d '\000-\010\013-\037\177' >&2 || true
}

# syncthing_health: the first line of the Hub's Syncthing's answer to its
# health endpoint, in HEALTH_FIRST; fails when it cannot be asked (stopped,
# restarting, an image without curl). Asked inside the container with the
# curl its healthcheck uses. While Syncthing 2.x converts a 1.x database it
# answers every request with HTTP 200 and a body whose first line is
# SYNCTHING_CONVERTING, its log after it; 1.30.0 has no such state, and both
# lines answer with JSON otherwise (decision 090). Nothing else of the
# answer is kept.
SYNCTHING_CONVERTING="*** Database migration in progress ***"
syncthing_health() {
	HEALTH_FIRST=""
	health=$($DOCKER compose --project-directory "$HUB_DIR" exec -T syncthing \
		curl -fsS -m 10 http://127.0.0.1:8384/rest/noauth/health 2>/dev/null) || return 1
	HEALTH_FIRST=$(printf '%s\n' "$health" | sed -n 1p)
}

# syncthing_started: whether the Hub's Syncthing container runs, and since
# when — one line that changes when it stops or starts again.
syncthing_started() {
	st_id=$($DOCKER compose --project-directory "$HUB_DIR" ps -q syncthing 2>/dev/null) || return 1
	[ -n "$st_id" ] || return 1
	$DOCKER inspect -f '{{.State.Running}} {{.State.StartedAt}}' "$st_id" 2>/dev/null
}

# conversion_progress: the file count in Syncthing's latest "Still migrating
# folder" line — digits only, nothing else of its log reaches the terminal.
conversion_progress() {
	$DOCKER compose --project-directory "$HUB_DIR" logs --no-color --tail 20 syncthing 2>/dev/null |
		sed -n 's/.*Still migrating folder (.* files=\([0-9][0-9]*\) .*/\1/p' | tail -1
}

# epoch_now: seconds since 1970, or nothing where date cannot say.
epoch_now() {
	epoch=$(date +%s 2>/dev/null) || epoch=""
	case "$epoch" in "" | *[!0-9]*) epoch="" ;; esac
	printf '%s' "$epoch"
}

# How long the wait follows a conversion: an hour. The slowest conversions
# measured took 11 to 12 1/2 minutes (about 50,000 files, Syncthing held to
# a fiftieth of a CPU); an hour is about five times that, and still an end
# for a run nobody watches (a script, a dropped session).
CONVERSION_WAIT_LIMIT=3600

# The ways a wait during a conversion ends without the Hub answering. None
# names the 1.x-on-2.x hint: Syncthing runs 2.x here.
conversion_stopped() {
	trap - HUP INT TERM
	show_log syncthing
	info "Syncthing stopped or started again while it converted this Hub's database to 2.x. The log above says why; a full disk reads \"database or disk is full\"."
	info "  Syncthing keeps the 1.x database until the conversion is complete: once the cause is fixed, run the setup again, and the conversion starts over."
	info "  What else you can do, going back to 1.x among it: $SYNCTHING_MOVE_DOC"
	fail "The Hub did not become ready."
}

conversion_wait_ended() {
	trap - HUP INT TERM
	info "Syncthing is still converting this Hub's database after $((CONVERSION_WAIT_LIMIT / 60)) minutes. Nothing has gone wrong so far: it goes on in its container, and the Hub answers once it is done."
	info "  Where it stands:  cd $HUB_DIR && $DOCKER compose logs --tail 3 syncthing"
	info "  Run the setup again later: it waits for the conversion again, or finds the Hub ready and prints a pairing code."
	fail "Setup stopped waiting for the conversion."
}

conversion_wait_interrupted() {
	trap - HUP INT TERM
	printf '%s\n' \
		"Setup stopped waiting. Syncthing goes on converting this Hub's database in its container, and the Hub answers once it is done." \
		"  Where it stands:  cd $HUB_DIR && $DOCKER compose logs --tail 3 syncthing" \
		"  Run the setup again later for a pairing code." >&2 || true
	exit "$1"
}

# wait_for_hub: until the Hub answers `vaultsync-hub status`, 45 tries.
# While Syncthing converts a 1.x database (decision 090) the Hub cannot
# answer, for minutes on a slow machine; those looks are not tries. The
# conversion begins with the first answer that is SYNCTHING_CONVERTING and
# lasts until an answer that is not (one that cannot be asked meanwhile
# changes nothing). Meanwhile setup says what happens, shows how far it is
# every third look, and stops waiting when Syncthing's container stops or
# starts again, or after CONVERSION_WAIT_LIMIT; Ctrl-C ends the wait, not
# the conversion. Any other answer, or none, is a try as before. A Hub that
# never answers is explained by the hub's own log: Syncthing passed its
# healthcheck before the hub started, and the 1.x-on-2.x hint belongs to a
# failed start (#364).
wait_for_hub() {
	tries=0
	conv_looks=0 # looks since this conversion was seen; 0: none going on
	conv_seen=""
	conv_started=""
	conv_t0=""
	until $DOCKER compose --project-directory "$HUB_DIR" exec -T hub vaultsync-hub status >/dev/null 2>&1; do
		if [ "$conv_looks" -gt 0 ] && [ -n "$conv_started" ] &&
			[ "$(syncthing_started || echo gone)" != "$conv_started" ]; then
			conversion_stopped
		fi
		if syncthing_health; then
			answered=1
		else
			answered=""
		fi
		if [ "$HEALTH_FIRST" = "$SYNCTHING_CONVERTING" ] || { [ "$conv_looks" -gt 0 ] && [ -z "$answered" ]; }; then
			conv_looks=$((conv_looks + 1))
			if [ "$conv_looks" = 1 ]; then
				conv_seen=1
				conv_started=$(syncthing_started) || conv_started=""
				conv_t0=$(epoch_now)
				trap 'conversion_wait_interrupted 129' HUP
				trap 'conversion_wait_interrupted 130' INT
				trap 'conversion_wait_interrupted 143' TERM
				info "Syncthing 2.x is converting this Hub's database from 1.x. The Hub answers once that is done, and setup waits for it."
				info "  On a large vault or a slow machine this takes minutes. Ctrl-C ends the wait, not the conversion."
			else
				# Without a clock, each look counts as the 2 s it sleeps at
				# least, so the bound errs on the long side.
				conv_now=$(epoch_now)
				if [ -n "$conv_t0" ] && [ -n "$conv_now" ]; then
					conv_waited=$((conv_now - conv_t0))
				else
					conv_waited=$((conv_looks * 2))
				fi
				[ "$conv_waited" -lt "$CONVERSION_WAIT_LIMIT" ] || conversion_wait_ended
				if [ $((conv_looks % 3)) = 0 ]; then
					conv_files=$(conversion_progress) || conv_files=""
					info "  Still converting${conv_files:+: $conv_files files so far in the vault it is on}."
				fi
			fi
		else
			if [ "$conv_looks" -gt 0 ]; then
				conv_looks=0
				trap - HUP INT TERM
			fi
			tries=$((tries + 1))
			if [ "$tries" -ge 45 ]; then
				show_log hub
				fail "The Hub did not become ready. The log above explains why; fix it and re-run."
			fi
		fi
		sleep 2
	done
	trap - HUP INT TERM
	# The Hub answers only once Syncthing's own API does, so the conversion
	# is over.
	[ -z "$conv_seen" ] || info "Syncthing has finished converting."
}

write_hub_files() {
	compose="$HUB_DIR/docker-compose.yml"
	env_file="$HUB_DIR/.env"
	if [ "$DRY_RUN" = 1 ]; then
		info "[dry-run] would write: $compose and $env_file (PUID=$PUID PGID=$PGID SYNC_PORT=$SYNC_PORT GUI_PORT=$GUI_PORT)"
		if [ -f "$env_file" ] && [ -f "$compose" ]; then
			if compose_files_nameable "$HUB_DIR"; then
				info "[dry-run] the stack would be written beside $compose, pulled from there and put in its place only after a pull that worked"
			else
				info "[dry-run] $compose would be written before the pull: setup cannot tell which files Compose reads here"
			fi
		fi
		if [ ! -f "$env_file" ]; then
			syncthing_image=$(syncthing_image_for_new_env)
			info "[dry-run] the new $env_file would name VAULTSYNC_HUB_SYNCTHING_IMAGE=$syncthing_image"
			[ "$syncthing_image" != "$SYNCTHING_IMAGE_KEPT" ] || kept_syncthing_note
		fi
		return 0
	fi
	# On an existing Hub the stack is written beside the compose file in
	# place, with that file's mode, and takes its place only after the pull
	# (#368) — when setup can name the files Compose reads; otherwise, and on
	# a new Hub, it is written in place before the pull.
	if [ -f "$env_file" ] && [ -f "$compose" ] && ! compose_files_nameable "$HUB_DIR"; then
		info "docker-compose.yml is written before the pull: setup cannot tell which files Compose reads here (COMPOSE_FILE, another compose file or more than one override beside it)."
	elif [ -f "$env_file" ] && [ -f "$compose" ]; then
		hold_hub_edits
		if ! COMPOSE_STAGED=$(mktemp "$compose.setup.XXXXXX") || ! cp -p "$compose" "$COMPOSE_STAGED"; then
			fail "Could not update $compose; nothing was changed. Run the setup again: it brings the whole stack (docs/hub.md → Update the Hub)."
		fi
		compose="$COMPOSE_STAGED"
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
    # A new Hub's .env names Syncthing's 2.x line. Without that line the stack
    # stays on 1.x: a Hub set up before 2.x was its line keeps the database
    # it has, because 2.x converts it on its first start and 1.x then refuses
    # to start on it (decision 068).
    image: ${VAULTSYNC_HUB_SYNCTHING_IMAGE:-syncthing/syncthing:1}
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
    image: ${VAULTSYNC_HUB_IMAGE:-ghcr.io/psimaker/vaultsync-hub:0.3.1}
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
		syncthing_image=$(syncthing_image_for_new_env)
		cat >"$env_file" <<ENV
# Written by setup.sh — edit and run \`docker compose up -d\` to apply.
PUID=$PUID
PGID=$PGID
SYNC_PORT=$SYNC_PORT
GUI_PORT=$GUI_PORT
HUB_PORT=8390
VAULTSYNC_HUB_NAME=$HUB_NAME
VAULTSYNC_HUB_IMAGE=$HUB_IMAGE
VAULTSYNC_HUB_SYNCTHING_IMAGE=$syncthing_image
RELAY_URL=$RELAY_URL
ENV
		[ "$syncthing_image" != "$SYNCTHING_IMAGE_KEPT" ] || kept_syncthing_note
	fi
}

# pull_hub_images: pulls what the stack will run — on an existing Hub what
# the staged files name, before they take the places of the files in place.
# With a staged compose file the pull names it and the override Compose
# would load beside it; without one, Compose finds the files as the later
# `up -d` does. A staged .env is the env file either way; without a staged
# compose file Compose also reads COMPOSE_FILE from it, as `up -d` will from
# .env (with -f, COMPOSE_FILE is not read, and the staged path is taken only
# when no COMPOSE_FILE is set).
pull_hub_images() {
	set -- --project-directory "$HUB_DIR"
	if [ -n "$COMPOSE_STAGED" ]; then
		set -- "$@" -f "$COMPOSE_STAGED"
		[ -z "$COMPOSE_OVERRIDE" ] || set -- "$@" -f "$COMPOSE_OVERRIDE"
	fi
	[ -z "$ENV_STAGED" ] || set -- "$@" --env-file "$ENV_EDIT_OUT"
	$DOCKER compose "$@" pull --quiet
}

# install_staged_hub_files: after the pull, the staged files take their
# places. .env is checked against the snapshot the staged one was made from
# first: changed meanwhile, neither file moves. A compose file that is a
# symbolic link is written through, as setup always has; any other moves in
# one rename.
install_staged_hub_files() {
	env_file="$HUB_DIR/.env"
	compose="$HUB_DIR/docker-compose.yml"
	if [ -n "$ENV_STAGED" ] && ! same_text "$env_file" "$ENV_EDIT_TMP"; then
		fail "$env_file changed while setup was pulling; nothing was changed. Check its VAULTSYNC_HUB_IMAGE line, then run the setup again."
	fi
	if [ -n "$COMPOSE_STAGED" ]; then
		if [ -L "$compose" ]; then
			cat "$COMPOSE_STAGED" >"$compose" ||
				fail "Could not update $compose through its link; the images are pulled, .env is unchanged. Run the setup again once the link can be written (docs/hub.md → Update the Hub)."
		else
			mv -f "$COMPOSE_STAGED" "$compose" ||
				fail "Could not update $compose; nothing was changed. Run the setup again: it brings the whole stack (docs/hub.md → Update the Hub)."
			COMPOSE_STAGED=""
		fi
	fi
	if [ -n "$ENV_STAGED" ]; then
		mv -f "$ENV_EDIT_OUT" "$env_file" ||
			fail "Could not update $env_file; its old image line stays. Change its VAULTSYNC_HUB_IMAGE line by hand, then run docker compose pull && docker compose up -d."
		ENV_EDIT_OUT=""
		ENV_STAGED=""
		info "  .env now names $HUB_IMAGE; the stack restarts on it below."
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
		HUB_EXISTED=1
		info "Existing Hub found in $HUB_DIR — updating it (your .env is kept)."
		# The hints below must name the ports this Hub actually uses.
		existing_sync=$(sed -n 's/^SYNC_PORT=//p' "$HUB_DIR/.env" | tail -1)
		existing_gui=$(sed -n 's/^GUI_PORT=//p' "$HUB_DIR/.env" | tail -1)
		[ -z "$existing_sync" ] || SYNC_PORT="$existing_sync"
		[ -z "$existing_gui" ] || GUI_PORT="$existing_gui"
		offer_newer_hub_image "$HUB_DIR/.env"
		if ! grep -qs '^VAULTSYNC_HUB_SYNCTHING_IMAGE=' "$HUB_DIR/.env"; then
			info "Your Hub's Syncthing follows the 1.x line, which gets no more fixes. Setup leaves it there:"
			info "  the move to 2.x cannot be undone. Which line it runs and why: $SYNCTHING_LINE_DOC"
		fi
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
		if [ -f "$HUB_DIR/.env" ] && [ -f "$HUB_DIR/docker-compose.yml" ] && compose_files_nameable "$HUB_DIR"; then
			info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR -f <the staged docker-compose.yml>${COMPOSE_OVERRIDE:+ -f $COMPOSE_OVERRIDE} pull (with --env-file <the staged .env> if the move is taken)"
		else
			info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR pull"
		fi
		info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR up -d"
		info "[dry-run] would run: $DOCKER compose --project-directory $HUB_DIR exec -T hub vaultsync-hub code"
		return 0
	fi
	if ! pull_hub_images; then
		# What the message says follows what was staged: a staged file is
		# unchanged, a compose file written in place may name images this
		# host has not downloaded.
		compose_was_staged="$COMPOSE_STAGED"
		end_hub_edits
		if [ -n "$HUB_EXISTED" ]; then
			info "The images could not be pulled (the message above says why)."
			if [ -n "$compose_was_staged" ]; then
				info "  Nothing was changed: $HUB_DIR/.env and docker-compose.yml are as they were, and so are the Hub's containers."
			else
				info "  $HUB_DIR/.env is as it was, and so are the Hub's containers. docker-compose.yml was written before the pull and may name images this host has not downloaded."
			fi
			info "  Run the setup again once the registry can be reached: it brings the whole stack (docs/hub.md → Update the Hub)."
			fail "The update stopped before the stack was restarted."
		fi
		fail "The images could not be pulled (the message above says why). Run the setup again once the registry can be reached."
	fi
	install_staged_hub_files
	end_hub_edits
	if ! $DOCKER compose --project-directory "$HUB_DIR" up -d; then
		show_log syncthing
		syncthing_start_hint
		fail "The Hub did not start. The log above explains why; fix it and re-run."
	fi
	wait_for_hub

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
# cleanup_device undoes what an app install left (restore_app) and removes
# what this run unpacked and downloaded.
cleanup_device() {
	restore_app
	[ -z "$UNPACKED" ] || rm -rf "$UNPACKED"
	rm -rf "$installer_dir"
}

setup_device() {
	# The service belongs to the account whose vaults it syncs; as root every
	# check of whose an app is passes, and another account's app would be
	# replaced.
	[ "$(id -u)" != 0 ] || fail "Run the setup link as the account that uses Obsidian, without sudo — VaultSync runs as that account."
	command -v curl >/dev/null 2>&1 || fail "curl is required."
	asset=$(detect_asset vaultsync)
	case "$asset" in
		vaultsync_darwin_*) check_macos ;;
	esac
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
	# The same arguments, quoted for sh: the command keep_newer_app names.
	SETUP_ARGS_QUOTED=""
	for a in "$@"; do
		SETUP_ARGS_QUOTED="$SETUP_ARGS_QUOTED $(sh_quote "$a")"
	done
	if [ "$DRY_RUN" = 1 ]; then
		program="<temporary folder>/vaultsync"
		if [ -n "$app_asset" ]; then
			dir=$(app_dir)
			check_app_place "$dir/$APP_NAME"
			download_binary "$app_asset" "$tag" "<temporary folder>/$app_asset" ""
			if [ -e "$dir/$APP_NAME" ]; then
				info "[dry-run] would replace VaultSync for Mac in $dir, unless it is already this build — or stop and change nothing where the one there is a newer release, or does not say which release it is (a development build)"
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
	# TMPDIR first; one this account cannot use (another account's, after
	# su without -) or that is gone: /tmp (the folder mktemp makes is this
	# account's alone either way).
	installer_dir=$(mktemp -d "${TMPDIR:-/tmp}/vaultsync-setup.XXXXXXXX" 2>/dev/null) ||
		installer_dir=$(mktemp -d "/tmp/vaultsync-setup.XXXXXXXX") ||
		fail "Could not make a temporary folder — set TMPDIR to a folder you can write to, then run the setup again."
	# The download is gone however this ends — also on Ctrl-C or a kill —
	# and an app set aside for its update is back in its place unless the
	# new one took it.
	trap 'cleanup_device' EXIT
	trap 'cleanup_device; exit 129' HUP
	trap 'cleanup_device; exit 130' INT
	trap 'cleanup_device; exit 143' TERM
	mkdir "$installer_dir/download"
	if [ -n "$app_asset" ]; then
		download_binary "$app_asset" "$tag" "$installer_dir/$app_asset" "$installer_dir/download"
		dir=$(app_dir)
		install_app "$installer_dir/$app_asset" "$dir"
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

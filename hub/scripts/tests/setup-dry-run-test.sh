#!/usr/bin/env sh
# Hermetic smoke test for setup.sh --dry-run.
#
# Runs the one-link setup for both menu choices with every external command
# (docker, curl, sudo, chown) replaced by PATH shims and asserts:
#   1. --dry-run exits 0 and creates nothing under the Hub directory.
#   2. The Hub path prints the compose/env plan with the caller's uid:gid and
#      the discovered port fallback.
#   3. The device path plans VaultSync for Mac and Linux from the newest stable
#      desktop-v* release (versions compared as numbers; drafts, pre-releases
#      and -rc tags skipped), never prints the pairing code, passes arguments
#      after -- to `vaultsync setup`, and never executes a privileged or
#      network-changing command (sudo/chown shims are tripwires; curl only
#      answers the release lookup).
#   4. The embedded compose text in setup.sh is byte-identical to
#      hub/docker-compose.yml — the one link must ship exactly the reviewed stack.
#   5. On a Mac (uname pinned to Darwin) the device path installs VaultSync for
#      Mac into Applications when the release has it and runs the setup from
#      there, falls back to the bare agent when it has not, and never replaces
#      a bundle that is not VaultSync for Mac.
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
SETUP_SH="$REPO_ROOT/hub/scripts/setup.sh"
COMPOSE="$REPO_ROOT/hub/docker-compose.yml"

SANDBOX=$(mktemp -d)
# chmod first: a case that takes a folder's modes away and fails before it
# gives them back must not leave a sandbox rm cannot remove.
trap 'chmod -R u+rwx "$SANDBOX" 2>/dev/null; rm -rf "$SANDBOX"' EXIT

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}
pass() {
	printf 'ok: %s\n' "$*"
}

mkdir -p "$SANDBOX/bin" "$SANDBOX/home/.local/state/syncthing" "$SANDBOX/results"
VIOLATIONS="$SANDBOX/results/violations.log"
: >"$VIOLATIONS"

# --- PATH shims --------------------------------------------------------------

# With ALLOW_COMPOSE set (the existing-Hub cases), the compose calls succeed
# and are logged with the image variable they would see — Compose reads the
# shell environment before .env.
cat >"$SANDBOX/bin/docker" <<'SHIM'
#!/usr/bin/env sh
case "$*" in
	"info") exit 0 ;;
	"compose version") echo "Docker Compose version v2.0.0"; exit 0 ;;
esac
if [ -n "${ALLOW_COMPOSE:-}" ]; then
	printf 'docker %s (VAULTSYNC_HUB_IMAGE=%s) (VAULTSYNC_HUB_SYNCTHING_IMAGE=%s)\n' "$*" "${VAULTSYNC_HUB_IMAGE-unset}" "${VAULTSYNC_HUB_SYNCTHING_IMAGE-unset}" >>"$COMPOSE_LOG"
	case "$*" in
		"compose --project-directory "*" pull --quiet")
			[ -z "${FAIL_COMPOSE_PULL:-}" ] || { echo "Error response from daemon: test pull failure" >&2; exit 1; }
			# The setup's trap runs once this command is done: the sleep
			# keeps it waiting until the signal has arrived.
			[ -z "${TERM_COMPOSE_PULL:-}" ] || { kill -TERM "$PPID"; sleep 1; exit 1; }
			# Another hand editing .env while the images download.
			[ -z "${MUTATE_DURING_PULL:-}" ] || sh -c "$MUTATE_DURING_PULL"
			exit 0 ;;
		"compose --project-directory "*" up -d") [ -z "${FAIL_COMPOSE_UP:-}" ] || exit 1; exit 0 ;;
		"compose --project-directory "*" logs --tail 30 "*) echo "syncthing log: test"; exit 0 ;;
		"compose --project-directory "*" exec -T hub vaultsync-hub status") printf 'VaultSync Hub test\nDevice ID:   TEST\n'; exit 0 ;;
		"compose --project-directory "*" exec -T hub vaultsync-hub code") printf 'Pairing code: TULIP-ANCHOR-42\n'; exit 0 ;;
	esac
fi
printf 'docker %s\n' "$*" >>"$VIOLATIONS"
exit 1
SHIM
# with-tty.py ANSWER CMD…: runs CMD on a pseudo-terminal and types ANSWER
# when the "[y/N]" prompt appears — the only way `ask` (which reads /dev/tty)
# can be answered from a test.
cat >"$SANDBOX/bin/with-tty.py" <<'PY'
import os, pty, select, signal, sys
answer, cmd = sys.argv[1].encode(), sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
out, sent = b"", False
while True:
    ready, _, _ = select.select([fd], [], [], 60)
    if not ready:
        os.kill(pid, signal.SIGKILL)
        sys.stdout.buffer.write(out + b"\n[with-tty: no output for 60 s, killed]\n")
        sys.exit(124)
    try:
        data = os.read(fd, 4096)
    except OSError:
        break
    if not data:
        break
    out += data
    if not sent and b"[y/N]" in out:
        if os.environ.get("MUTATE_BEFORE_ANSWER"):
            os.environ["CHILD_PID"] = str(pid)
            os.system(os.environ["MUTATE_BEFORE_ANSWER"])
        try:
            os.write(fd, answer + b"\n")
        except OSError:
            pass
        sent = True
_, status = os.waitpid(pid, 0)
sys.stdout.buffer.write(out)
sys.exit(os.waitstatus_to_exitcode(status))
PY
# The release list as GitHub sends it: pretty-printed, 100 per page, ordered
# by commit date (not by version), "draft" and "prerelease" after "tag_name".
# The newest stable desktop release is desktop-v0.10.0 and sits on page two:
# 0.13.0 is a draft, 0.11.0 a pre-release, 0.12.0-rc.1 carries a suffix, and
# 0.10.0 > 0.9.1 only as numbers. CURL_FAIL_API makes the lookup fail after
# printing a partial page. With FAKE_RELEASE set, downloads are served from
# that folder; otherwise any other request is a violation.
cat >"$SANDBOX/bin/curl" <<'SHIM'
#!/usr/bin/env sh
release() {
	printf '  {\n    "tag_name": "%s",\n    "name": "x",\n    "draft": %s,\n    "prerelease": %s,\n    "assets": [\n      { "name": "a" }\n    ]\n  },\n' "$1" "$2" "$3"
}
case "$*" in
	*api.github.com*page=1)
		printf '[\n'
		release hub-v0.2.0 false false
		release desktop-v0.9.1 false false
		release desktop-v0.13.0 true false
		release desktop-v0.12.0-rc.1 false false
		if [ -n "${CURL_FAIL_API:-}" ]; then
			release desktop-v0.20.0 false false
			exit 18
		fi
		release desktop-v0.11.0 false true
		exit 0
		;;
	*api.github.com*page=2)
		printf '[\n'
		release desktop-v0.10.0 false false
		release v2.1.0 false false
		release desktop-v0.1.0 false false
		exit 0
		;;
	*api.github.com*) printf '[]\n'; exit 0 ;;
esac
if [ -n "${FAKE_RELEASE:-}" ]; then
	out=""
	url=""
	while [ $# -gt 0 ]; do
		case "$1" in
			-o) out="$2"; shift ;;
			-*) ;;
			*) url="$1" ;;
		esac
		shift
	done
	[ -f "$FAKE_RELEASE/${url##*/}" ] || exit 22
	if [ -n "$out" ]; then
		cp "$FAKE_RELEASE/${url##*/}" "$out" && exit 0
	else
		cat "$FAKE_RELEASE/${url##*/}" && exit 0
	fi
	exit 22
fi
printf 'curl %s\n' "$*" >>"$VIOLATIONS"
exit 1
SHIM
# The Hub path is Linux-only; pin uname so the test is the same on a macOS
# developer machine and on the Linux CI runner. FAKE_OS / FAKE_ARCH make it
# a Mac for the VaultSync for Mac cases.
cat >"$SANDBOX/bin/uname" <<'SHIM'
#!/usr/bin/env sh
case "$*" in
	-s) echo "${FAKE_OS:-Linux}" ;;
	-m) echo "${FAKE_ARCH:-x86_64}" ;;
	*) echo "${FAKE_OS:-Linux}" ;;
esac
SHIM
# id -u answers FAKE_UID when it is set (the device path refuses root); a
# test run as root (in a container) pins it to an ordinary account's, so the
# device cases still run.
REAL_ID=$(command -v id)
[ "$("$REAL_ID" -u)" != 0 ] || FAKE_UID=${FAKE_UID:-4242}
export FAKE_UID
cat >"$SANDBOX/bin/id" <<SHIM
#!/usr/bin/env sh
if [ "\$1" = -u ] && [ -n "\${FAKE_UID:-}" ]; then
	echo "\$FAKE_UID"
	exit 0
fi
exec "$REAL_ID" "\$@"
SHIM
# Port probes are read-only: answer "nothing listening".
for probe in ss netstat; do
	printf '#!/usr/bin/env sh\nexit 0\n' >"$SANDBOX/bin/$probe"
done
for tripwire in sudo chown; do
	cat >"$SANDBOX/bin/$tripwire" <<SHIM
#!/usr/bin/env sh
printf '$tripwire %s\n' "\$*" >>"\$VIOLATIONS"
exit 1
SHIM
done
chmod +x "$SANDBOX/bin/"*
# sed and mv pass through to the real ones — unless a test asks for the
# write of .env to fail midway (FAIL_SED_WRITE: partial output, then
# failure) or the rename to fail (FAIL_MV).
REAL_SED=$(command -v sed)
REAL_MV=$(command -v mv)
# NOOP_SED_WRITE: the write expression "succeeds" without replacing anything
# — the output is the input, status zero.
cat >"$SANDBOX/bin/sed" <<SHIM
#!/usr/bin/env sh
case "\$*" in
	*"s|^VAULTSYNC_HUB_IMAGE="*)
		if [ -n "\${FAIL_SED_WRITE:-}" ]; then printf 'PUID=1000\\n'; exit 1; fi
		if [ -n "\${NOOP_SED_WRITE:-}" ]; then for a in "\$@"; do :; done; cat "\$a"; exit 0; fi
		;;
esac
exec "$REAL_SED" "\$@"
SHIM
cat >"$SANDBOX/bin/mv" <<SHIM
#!/usr/bin/env sh
[ -z "\${FAIL_MV:-}" ] || exit 1
# FAIL_MV_ENV: only the rename onto an .env fails (the compose file moved);
# the destination is the last argument (mv -f SOURCE DEST).
for mv_last in "\$@"; do :; done
case "\$mv_last" in */.env) [ -z "\${FAIL_MV_ENV:-}" ] || exit 1 ;; esac
if [ -n "\${FAIL_MV_UNPACKED:-}" ]; then
	case "\$1" in */unpacked/*) exit 1 ;; esac
fi
# The signals below reach the setup while it still waits for this mv (the
# sleep): a shell runs the trap once the command it waits for is done, and
# without the wait it may see the exit status first.
# INT_AFTER_ASIDE: Ctrl-C right after the installed app was set aside, before
# the new one moves. FAIL_MV_BACK: putting the set-aside app back fails.
# HUP_AFTER_BACK: a second signal (Terminal closed) right after the
# set-aside app went back, while the first one's restore is still running.
case "\$2" in
	*/.VaultSync\ for\ Mac.app.replaced.*)
		if [ -n "\${INT_AFTER_ASIDE:-}" ]; then
			"$REAL_MV" "\$@" || exit 1
			kill -INT \$PPID
			sleep 1
			exit 0
		fi
		;;
esac
case "\$1" in
	*/.VaultSync\ for\ Mac.app.replaced.*)
		[ -z "\${FAIL_MV_BACK:-}" ] || exit 1
		if [ -n "\${HUP_AFTER_BACK:-}" ]; then
			"$REAL_MV" "\$@" || exit 1
			kill -HUP \$PPID
			sleep 1
			exit 0
		fi
		;;
esac
# A move of the unpacked app that copies across disks and stops halfway:
# part of the bundle is at the destination, then the copy fails
# (FAIL_MV_PARTIAL) or the setup is interrupted (INT_MV_PARTIAL, Ctrl-C).
# STUCK_PARTIAL: that part cannot be removed (a folder in it this account
# may not change). APPEAR_AT_APP: something else takes the app's place just
# before the move (another setup at the same time). MV_PLACE_LOG: where the
# move into place took the app from is written there.
case "\$1" in
	*/unpacked/*)
		[ -z "\${MV_PLACE_LOG:-}" ] || printf '%s\n' "\$1" >>"\$MV_PLACE_LOG"
		if [ -n "\${APPEAR_AT_APP:-}" ]; then
			mkdir -p "\$2"
			printf 'someone else\\n' >"\$2/marker"
		fi
		;;
esac
case "\$1" in
	*/unpacked/*)
		if [ -n "\${FAIL_MV_PARTIAL:-}\${INT_MV_PARTIAL:-}" ]; then
			mkdir -p "\$2/Contents/MacOS"
			printf 'half\\n' >"\$2/Contents/MacOS/vaultsync"
			[ -z "\${STUCK_PARTIAL:-}" ] || chmod 555 "\$2/Contents/MacOS"
			[ -z "\${INT_MV_PARTIAL:-}" ] || { kill -INT \$PPID; sleep 1; }
			exit 1
		fi
		;;
esac
exec "$REAL_MV" "\$@"
SHIM
REAL_MKTEMP=$(command -v mktemp)
cat >"$SANDBOX/bin/mktemp" <<SHIM
#!/usr/bin/env sh
[ -z "\${FAIL_MKTEMP:-}" ] || exit 1
exec "$REAL_MKTEMP" "\$@"
SHIM
# FAIL_RM_TMP: removing a .env.setup.* file fails (the file stays).
REAL_RM=$(command -v rm)
cat >"$SANDBOX/bin/rm" <<SHIM
#!/usr/bin/env sh
if [ -n "\${FAIL_RM_TMP:-}" ]; then
	case "\$*" in *.env.setup.*) exit 1 ;; esac
fi
exec "$REAL_RM" "\$@"
SHIM
chmod +x "$SANDBOX/bin/sed" "$SANDBOX/bin/mv" "$SANDBOX/bin/mktemp" "$SANDBOX/bin/rm"
# setup.sh reads a bundle's identifier with plutil, which every Mac has. On
# a host without it (the Linux CI run) a stand-in answers the one question
# setup.sh asks — `plutil -extract CFBundleIdentifier raw -expect string -o
# - FILE` — from an XML plist, top level only, a string value only.
REAL_PLUTIL=$(command -v plutil || true)
if [ -z "$REAL_PLUTIL" ]; then
	cat >"$SANDBOX/bin/plutil" <<'SHIM'
#!/usr/bin/env sh
[ "$1 $2 $3 $4 $5 $6 $7" = "-extract CFBundleIdentifier raw -expect string -o -" ] || exit 2
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
				if (end > 0) { print substr(line, 1, end - 1); found = 1 }
				exit
			} else want = 0
		}
	}
	END { exit !found }
' "$8"
SHIM
	chmod +x "$SANDBOX/bin/plutil"
fi
export VIOLATIONS
export PATH="$SANDBOX/bin:$PATH"
export HOME="$SANDBOX/home"

# --- 1+2. Hub path -----------------------------------------------------------

HUB_DIR="$SANDBOX/hub-dir"
out=$(VAULTSYNC_HUB_DIR="$HUB_DIR" sh "$SETUP_SH" --hub --dry-run 2>&1) ||
	fail "hub dry-run exited non-zero:
$out"
[ ! -e "$HUB_DIR" ] || fail "hub dry-run created $HUB_DIR"
pass "hub dry-run exits 0 and creates nothing"

expected_owner="$(id -u):$(id -g)"
printf '%s\n' "$out" | grep -q "PUID=$(id -u) PGID=$(id -g)" ||
	fail "hub dry-run does not print the caller's uid:gid ($expected_owner):
$out"
printf '%s\n' "$out" | grep -q "would run: docker compose --project-directory $HUB_DIR up -d" ||
	fail "hub dry-run does not plan 'docker compose up -d':
$out"
printf '%s\n' "$out" | grep -q "would run: docker compose --project-directory $HUB_DIR exec -T hub vaultsync-hub code" ||
	fail "hub dry-run does not plan to print a pairing code:
$out"
pass "hub dry-run plans compose up, init and pairing code with the right owner"

# --- The line of Syncthing a Hub follows (#301, decision 068) ----------------
# A new Hub's .env names 2.x; a Hub directory where Syncthing already ran
# keeps 1.x unless setup sees 2.x's own database there, because 2.x converts
# a 1.x database for good; an .env setup wrote before names no line, is never
# edited, and the compose file falls back to 1.x for it.
printf '%s\n' "$out" | grep -q "the new $HUB_DIR/.env would name VAULTSYNC_HUB_SYNCTHING_IMAGE=syncthing/syncthing:2$" ||
	fail "a new Hub's dry run does not name Syncthing 2.x:
$out"
COMPOSE_LOG="$SANDBOX/results/compose.log"
export COMPOSE_LOG
ST_HUB="$SANDBOX/hub-301"
# shellcheck disable=SC2016 # the braces are literal text in the compose file
ST_FALLBACK='    image: ${VAULTSYNC_HUB_SYNCTHING_IMAGE:-syncthing/syncthing:1}'
new_env_run() { # a non-dry-run Hub setup in $ST_HUB, Docker stubbed, no terminal
	: >"$COMPOSE_LOG"
	VAULTSYNC_HUB_DIR="$ST_HUB" VIOLATIONS="$SANDBOX/results/violations-301.log" ALLOW_COMPOSE=1 sh "$SETUP_SH" --hub </dev/null 2>&1
}
st_line() { sed -n 's/^VAULTSYNC_HUB_SYNCTHING_IMAGE=//p' "$ST_HUB/.env"; }
rm -rf "$ST_HUB"
out=$(VAULTSYNC_HUB_SYNCTHING_IMAGE=syncthing/syncthing:9 new_env_run) || fail "new-Hub setup exited non-zero:
$out"
[ "$(st_line)" = "syncthing/syncthing:2" ] || fail "a new Hub's .env does not name Syncthing 2.x (an exported value must not decide it):
$(cat "$ST_HUB/.env")"
[ "$(grep -c '^VAULTSYNC_HUB_SYNCTHING_IMAGE=' "$ST_HUB/.env")" = 1 ] || fail "a new Hub's .env names the Syncthing image more than once"
if printf '%s\n' "$out" | grep -q "keeps it on"; then
	fail "a new Hub is told it stays on 1.x:
$out"
fi
grep -q " up -d " "$COMPOSE_LOG" || fail "a new Hub was not started:
$(cat "$COMPOSE_LOG")"
if grep -v "(VAULTSYNC_HUB_SYNCTHING_IMAGE=unset)" "$COMPOSE_LOG" | grep -q .; then
	fail "Compose would see an exported Syncthing image over .env:
$(cat "$COMPOSE_LOG")"
fi
grep -qx "$ST_FALLBACK" "$ST_HUB/docker-compose.yml" ||
	fail "the compose file does not take the Syncthing image from .env with 1.x as its fallback"
pass "a new Hub's .env names Syncthing 2.x, and only .env decides it"
out=$(new_env_run) || fail "a second setup run on the new Hub exited non-zero:
$out"
if printf '%s\n' "$out" | grep -q "follows the 1.x line"; then
	fail "a Hub whose .env names 2.x is told it follows 1.x:
$out"
fi
pass "a Hub whose .env names 2.x hears nothing about 1.x"

# A start that fails shows Syncthing's log and what a refused 2.x home means.
rm -rf "$ST_HUB"
if out=$(FAIL_COMPOSE_UP=1 new_env_run); then
	fail "a failed start must stop the setup:
$out"
fi
grep -q "logs --tail 30 syncthing" "$COMPOSE_LOG" || fail "a failed start does not show Syncthing's log:
$(cat "$COMPOSE_LOG")"
printf '%s\n' "$out" | grep -q "set VAULTSYNC_HUB_SYNCTHING_IMAGE=syncthing/syncthing:2" || fail "a failed start does not name the .env line:
$out"
pass "a failed start shows Syncthing's log and names the .env line"

lost_env() { # NAME EXPECTED-IMAGE: a Hub directory without .env, as prepared
	out=$(new_env_run) || fail "$1: setup exited non-zero:
$out"
	[ "$(st_line)" = "$2" ] || fail "$1: the new .env names $(st_line), not $2"
	if [ "$2" = "syncthing/syncthing:1" ]; then
		printf '%s\n' "$out" | grep -q "the new .env keeps it on syncthing/syncthing:1" || fail "$1: the kept line is not reported:
$out"
	fi
	pass "$1: the new .env names $2"
}
rm -rf "$ST_HUB"; mkdir -p "$ST_HUB/syncthing/config/index-v0.14.0.db"
out=$(VAULTSYNC_HUB_DIR="$ST_HUB" sh "$SETUP_SH" --hub --dry-run 2>&1) || fail "dry run on a Hub directory without .env exited non-zero:
$out"
if ! printf '%s\n' "$out" | grep -q "would name VAULTSYNC_HUB_SYNCTHING_IMAGE=syncthing/syncthing:1$" ||
	! printf '%s\n' "$out" | grep -q "cannot tell to be 2.x"; then
	fail "a dry run on a Hub directory with a 1.x database does not say it keeps 1.x and why:
$out"
fi
[ ! -e "$ST_HUB/.env" ] || fail "a dry run wrote .env"
pass "a dry run on a Hub directory with a 1.x database says it keeps 1.x and why"
lost_env "a Hub directory whose Syncthing has a 1.x database" "syncthing/syncthing:1"
rm -rf "$ST_HUB"; mkdir -p "$ST_HUB/syncthing/config/index-v0.14.0.db-migrated" "$ST_HUB/syncthing/config/index-v2"
lost_env "a Hub directory whose Syncthing is on 2.x already" "syncthing/syncthing:2"
rm -rf "$ST_HUB"; mkdir -p "$ST_HUB/syncthing/config"; : >"$ST_HUB/syncthing/config/config.xml"
lost_env "a Hub directory whose Syncthing has no database setup can read" "syncthing/syncthing:1"
rm -rf "$ST_HUB"; mkdir -p "$ST_HUB/syncthing/config/index-v2"; chmod 000 "$ST_HUB/syncthing/config"
# Root sees through the mode; every other account sees nothing to tell 2.x by.
if [ "$("$REAL_ID" -u)" = 0 ]; then
	lost_env "a Hub directory whose Syncthing folder setup cannot open (run as root)" "syncthing/syncthing:2"
else
	lost_env "a Hub directory whose Syncthing folder setup cannot open" "syncthing/syncthing:1"
fi
chmod 700 "$ST_HUB/syncthing/config"
rm -rf "$ST_HUB"; mkdir -p "$ST_HUB/syncthing/config"; chmod 000 "$ST_HUB/syncthing"
lost_env "a Hub directory whose syncthing folder setup cannot search" "syncthing/syncthing:1"
chmod 755 "$ST_HUB/syncthing"

# An .env written before #301 names no Syncthing line: it stays as it is,
# and Compose falls back to 1.x for it.
rm -rf "$ST_HUB"; mkdir -p "$ST_HUB/syncthing/config/index-v0.14.0.db"
# shellcheck disable=SC2016 # the braces are literal text in setup.sh
printf '# Written by setup.sh\nPUID=1000\nPGID=1000\nSYNC_PORT=22000\nGUI_PORT=8384\nHUB_PORT=8390\nVAULTSYNC_HUB_NAME=My Hub\nVAULTSYNC_HUB_IMAGE=%s\nRELAY_URL=https://relay.example\n' \
	"$(sed -n 's/^HUB_IMAGE="${VAULTSYNC_HUB_IMAGE:-\(.*\)}"$/\1/p' "$SETUP_SH")" >"$ST_HUB/.env"
cp "$ST_HUB/.env" "$SANDBOX/results/env-301-before"
out=$(VAULTSYNC_HUB_SYNCTHING_IMAGE=syncthing/syncthing:2 new_env_run) || fail "existing-Hub setup (#301) exited non-zero:
$out"
cmp -s "$ST_HUB/.env" "$SANDBOX/results/env-301-before" || fail "an existing Hub's .env was edited:
$(diff -u "$SANDBOX/results/env-301-before" "$ST_HUB/.env" || true)"
grep -qx "$ST_FALLBACK" "$ST_HUB/docker-compose.yml" ||
	fail "an existing Hub's compose file does not fall back to 1.x"
grep -q " up -d " "$COMPOSE_LOG" || fail "the existing Hub was not restarted:
$(cat "$COMPOSE_LOG")"
if grep -v "(VAULTSYNC_HUB_SYNCTHING_IMAGE=unset)" "$COMPOSE_LOG" | grep -q .; then
	fail "an exported Syncthing image would move an existing Hub:
$(cat "$COMPOSE_LOG")"
fi
[ -d "$ST_HUB/syncthing/config/index-v0.14.0.db" ] || fail "an existing Hub's database was touched"
printf '%s\n' "$out" | grep -q "Your Hub's Syncthing follows the 1.x line" || fail "an existing Hub on 1.x is not told so:
$out"
pass "an existing Hub's .env stays as it is and its Syncthing stays on 1.x"
grep -v '^chown ' "$SANDBOX/results/violations-301.log" 2>/dev/null | grep -q . && fail "a #301 Hub setup ran a privileged or network-changing command:
$(cat "$SANDBOX/results/violations-301.log")"
rm -rf "$ST_HUB"

# An existing Hub keeps its .env; setup offers only a newer official image —
# never a custom one, never a downgrade — and in a dry run only on paper (#216).
# shellcheck disable=SC2016 # the braces are literal text in setup.sh
SHIPPED=$(sed -n 's/^HUB_IMAGE="${VAULTSYNC_HUB_IMAGE:-\(.*\)}"$/\1/p' "$SETUP_SH")
[ -n "$SHIPPED" ] || fail "cannot read the shipped Hub image from setup.sh"
EXISTING="$SANDBOX/existing-hub"
existing_hub_dry_run() {
	rm -rf "$EXISTING"
	mkdir -p "$EXISTING"
	printf 'PUID=1000\nPGID=1000\nSYNC_PORT=22001\nGUI_PORT=8385\nVAULTSYNC_HUB_IMAGE=%s\n' "$1" >"$EXISTING/.env"
	VAULTSYNC_HUB_DIR="$EXISTING" sh "$SETUP_SH" --hub --dry-run 2>&1
}
out=$(existing_hub_dry_run "ghcr.io/psimaker/vaultsync-hub:0.1.0") || fail "existing-hub dry-run exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "would offer to set VAULTSYNC_HUB_IMAGE=$SHIPPED in $EXISTING/.env" ||
	fail "an older official image is not offered the shipped one:
$out"
printf '%s\n' "$out" | grep -q "SYNC_PORT=22001 GUI_PORT=8385" ||
	fail "the existing Hub's ports are not kept:
$out"
grep -q "^VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0$" "$EXISTING/.env" || fail "a dry run changed .env"
out=$(existing_hub_dry_run "ghcr.io/psimaker/vaultsync-hub:9.9.9") || fail "existing-hub dry-run exited non-zero:
$out"
if printf '%s\n' "$out" | grep -q "would offer"; then
	fail "a newer official image must not be offered a downgrade:
$out"
fi
out=$(existing_hub_dry_run "registry.example/me/hub:dev") || fail "existing-hub dry-run exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "custom image" || fail "a custom image is not left alone:
$out"
if printf '%s\n' "$out" | grep -q "would offer"; then
	fail "a custom image must not be offered anything:
$out"
fi
out=$(existing_hub_dry_run "$SHIPPED") || fail "existing-hub dry-run exited non-zero:
$out"
if printf '%s\n' "$out" | grep -q -e "would offer" -e "custom image"; then
	fail "the shipped image needs no word:
$out"
fi
# A shipped image whose tag is not one line of x.y.z is never offered —
# whatever an exported override smuggles in.
out=$(VAULTSYNC_HUB_IMAGE="ghcr.io/psimaker/vaultsync-hub:0.9.0
bad" existing_hub_dry_run "ghcr.io/psimaker/vaultsync-hub:0.1.0") || fail "existing-hub dry-run (multi-line override) exited non-zero:
$out"
if printf '%s\n' "$out" | grep -q "would offer"; then
	fail "a multi-line shipped image was offered:
$out"
fi
pass "an existing Hub is offered only a newer official image, on paper in a dry run"

# The real thing, with Docker stubbed: the move is one line of .env, on a
# yes only; a no, no terminal, or an .env with a quoted value changes
# nothing; and an exported image override never reaches Compose.
COMPOSE_LOG="$SANDBOX/results/compose.log"
export COMPOSE_LOG
plain_env() {
	printf '# Written by setup.sh\nPUID=1000\nPGID=1000\nSYNC_PORT=22001\nGUI_PORT=8385\nHUB_PORT=8390\nVAULTSYNC_HUB_NAME=My Hub\nVAULTSYNC_HUB_IMAGE=%s\nRELAY_URL=https://relay.example\n' "$1"
}
existing_hub_run() { # ANSWER|none: a non-dry-run Hub setup on the existing Hub
	: >"$COMPOSE_LOG"
	if [ "$1" = none ]; then
		VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 sh "$SETUP_SH" --hub </dev/null 2>&1
	else
		VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 python3 -I "$SANDBOX/bin/with-tty.py" "$1" sh "$SETUP_SH" --hub 2>&1
	fi
}
if command -v python3 >/dev/null 2>&1; then
	rm -rf "$EXISTING"; mkdir -p "$EXISTING"
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	plain_env "$SHIPPED" >"$SANDBOX/results/env-expected"
	# An existing Hub has the compose file an earlier setup wrote.
	sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
	out=$(existing_hub_run y) || fail "existing-hub setup (yes) exited non-zero:
$out"
	cmp -s "$EXISTING/docker-compose.yml" "$COMPOSE" || fail "a yes did not bring the compose file to the stack setup ships:
$(diff -u "$COMPOSE" "$EXISTING/docker-compose.yml" || true)"
	printf '%s\n' "$out" | grep -q ".env now names $SHIPPED" || fail "a yes is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-expected" || fail "a yes changed more than the image line:
$(diff -u "$SANDBOX/results/env-expected" "$EXISTING/.env" || true)"
	if ! grep -q "pull --quiet" "$COMPOSE_LOG" || ! grep -q " up -d" "$COMPOSE_LOG"; then
		fail "the stack was not pulled and restarted after a yes:
$(cat "$COMPOSE_LOG")"
	fi
	# The pull reads the staged files, the start reads the files in place
	# (#368).
	grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "-f $EXISTING/docker-compose.yml.setup.* --env-file $EXISTING/.env.setup." || fail "the pull after a yes did not read the staged compose file and .env:
$(cat "$COMPOSE_LOG")"
	if grep " up -d" "$COMPOSE_LOG" | grep -q -e " -f " -e "--env-file"; then
		fail "the start after a yes read a staged file:
$(cat "$COMPOSE_LOG")"
	fi
	pass "a yes moves exactly the image line of .env and restarts the stack"

	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	out=$(existing_hub_run n) || fail "existing-hub setup (no) exited non-zero:
$out"
	if ! printf '%s\n' "$out" | grep -q "Kept 0.1.0" || ! printf '%s\n' "$out" | grep -q "To move by hand"; then
		fail "a no is not reported with the manual step:
$out"
	fi
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a no changed .env"
	pass "a no keeps .env and names the manual step"

	# A quoted value may span lines, a key may repeat, a link may lead
	# elsewhere: such an .env is never edited — each shape on its own.
	never_edited() { # NAME FIXTURE-CONTENT [WORD]: a yes changes nothing
		printf '%s' "$2" >"$EXISTING/.env"
		cp "$EXISTING/.env" "$SANDBOX/results/env-before"
		out=$(existing_hub_run y) || fail "existing-hub setup ($1) exited non-zero:
$out"
		printf '%s\n' "$out" | grep -q "${3:-shape setup does not edit}" || fail "$1: not left alone with a word:
$out"
		if printf '%s\n' "$out" | grep -q "Move it to"; then
			fail "$1: the move was offered:
$out"
		fi
		cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "$1: .env was edited"
		pass "$1 is never edited"
	}
	never_edited "an .env with a multi-line quoted value" "PUID=1000
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0
NOTES='before:
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.0.1
'
"
	never_edited "an .env with a single-line quoted value" "PUID=1000
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0
VAULTSYNC_HUB_NAME=\"My Hub\"
"
	never_edited "an .env naming the image twice" "VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0
"
	never_edited "an .env whose image line ends in a comment" "PUID=1000
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0 # pinned
" "shape setup does not read"
	never_edited "an .env whose image line ends in a space" "PUID=1000
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0 
" "shape setup does not read"
	never_edited "an .env with Windows line endings" "$(printf 'PUID=1000\r\nVAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0\r\n')" "shape setup does not read"
	never_edited "an .env naming a version with a leading zero" "VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.01.0
" "shape setup does not read"
	rm -f "$EXISTING/.env.real"
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env.real"
	rm -f "$EXISTING/.env"; ln -s "$EXISTING/.env.real" "$EXISTING/.env"
	cp "$EXISTING/.env.real" "$SANDBOX/results/env-before"
	out=$(existing_hub_run y) || fail "existing-hub setup (linked .env) exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q "shape setup does not edit" || fail "a linked .env is not left alone with a word:
$out"
	cmp -s "$EXISTING/.env.real" "$SANDBOX/results/env-before" || fail "a linked .env was edited through the link"
	rm -f "$EXISTING/.env"
	pass "an .env that is a link is never edited"

	# A setup that ships a custom or malformed image offers nothing.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	out=$(VAULTSYNC_HUB_IMAGE="registry.example/me/hub:dev" existing_hub_run y) || fail "existing-hub setup (custom shipped image) exited non-zero:
$out"
	if printf '%s\n' "$out" | grep -q "Move it to"; then
		fail "a custom shipped image was offered:
$out"
	fi
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a custom shipped image edited .env"
	pass "a custom shipped image offers nothing"

	# A write that fails stops the setup with .env untouched.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	chmod a-w "$EXISTING"
	if out=$(existing_hub_run y); then
		chmod u+w "$EXISTING"
		fail "a write that cannot happen must stop the setup:
$out"
	fi
	chmod u+w "$EXISTING"
	printf '%s\n' "$out" | grep -q -e "Could not update" -e "Could not create the lock" || fail "a failed write is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a failed write changed .env"
	for leftover in "$EXISTING"/.env.setup.*; do
		[ ! -e "$leftover" ] || fail "a failed write left a temporary file: $leftover"
	done
	pass "a write that fails stops the setup and leaves .env whole"

	# A failure midway — sed with partial output, or the rename — stops the
	# setup the same way: original bytes, no success line, no Compose, no
	# temporary file.
	failed_midway() { # NAME ENV-VAR
		plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
		cp "$EXISTING/.env" "$SANDBOX/results/env-before"
		if out=$(export "$2=1"; existing_hub_run y); then
			fail "$1 must stop the setup:
$out"
		fi
		printf '%s\n' "$out" | grep -q "Could not update" || fail "$1 is not reported:
$out"
		if printf '%s\n' "$out" | grep -q "now names"; then
			fail "$1 was reported as success:
$out"
		fi
		cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "$1 changed .env"
		# The files move into place only after their images are pulled
		# (#368): a rename that fails comes after the pull, never before an
		# up -d; every other step fails before Compose is called at all.
		if [ "$2" = FAIL_MV ]; then
			if grep -q " up -d" "$COMPOSE_LOG"; then
				fail "$1 still restarted the stack:
$(cat "$COMPOSE_LOG")"
			fi
		else
			[ ! -s "$COMPOSE_LOG" ] || fail "$1 still called Compose:
$(cat "$COMPOSE_LOG")"
		fi
		for leftover in "$EXISTING"/.env.setup.* "$EXISTING"/docker-compose.yml.setup.*; do
			[ ! -e "$leftover" ] || fail "$1 left a temporary file: $leftover"
		done
		[ ! -d "$EXISTING/.env.setup-lock" ] || fail "$1 left the lock behind"
		pass "$1 stops the setup and leaves .env whole"
	}
	failed_midway "a sed that fails with partial output" FAIL_SED_WRITE
	failed_midway "a sed that replaces nothing and reports success" NOOP_SED_WRITE
	failed_midway "a rename that fails" FAIL_MV
	failed_midway "a temporary file that cannot be reserved" FAIL_MKTEMP

	# A snapshot that cannot be removed afterwards is reported, and the lock
	# is released all the same.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	out=$(FAIL_RM_TMP=1 existing_hub_run y) || fail "existing-hub setup (snapshot removal fails) exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q "Could not remove the temporary file" || fail "a snapshot that cannot be removed is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-expected" || fail "the move did not happen although only the snapshot removal failed"
	[ ! -d "$EXISTING/.env.setup-lock" ] || fail "a failed snapshot removal left the lock behind"
	"$REAL_RM" -f "$EXISTING"/.env.setup.*
	pass "a snapshot that cannot be removed is reported, the lock released"

	# The temporary names are reserved: a link planted under the name a
	# predictable scheme would use (.env.setup.<pid of the setup>) catches
	# nothing.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	printf 'untouched\n' >"$SANDBOX/results/sentinel"
	out=$(MUTATE_BEFORE_ANSWER="ln -s '$SANDBOX/results/sentinel' '$EXISTING/.env.setup.'\$CHILD_PID" existing_hub_run y) || fail "existing-hub setup (planted link) exited non-zero:
$out"
	[ "$(cat "$SANDBOX/results/sentinel")" = "untouched" ] || fail "a planted link under a predictable temporary name was written through:
$(cat "$SANDBOX/results/sentinel")"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-expected" || fail "the move did not happen beside a planted link"
	rm -f "$EXISTING"/.env.setup.*
	pass "a link planted under a predictable temporary name catches nothing"

	# The whole file is checked again after the answer, not just the line:
	# a quoted value grown around the very line that was offered — still the
	# only image line, still reading the same — stops the edit.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	printf 'PUID=1000\nNOTES=%s\n' "'pinned:
VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0
'" >"$SANDBOX/results/env-reshaped"
	out=$(MUTATE_BEFORE_ANSWER="cp '$SANDBOX/results/env-reshaped' '$EXISTING/.env'" existing_hub_run y) || fail "existing-hub setup (reshaped meanwhile) exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q "changed while setup was asking" || fail "a reshape during the question is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-reshaped" || fail "a reshape during the question was edited:
$(cat "$EXISTING/.env")"
	pass "a file reshaped while setup was asking is not edited"

	# An interruption while setup is asking ends it — lock and temporary
	# files gone, .env untouched.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	# shellcheck disable=SC2016 # the driver's shell expands CHILD_PID
	if out=$(MUTATE_BEFORE_ANSWER='kill -TERM $CHILD_PID' existing_hub_run y); then
		fail "a TERM while setup was asking did not end it:
$out"
	fi
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a TERM while setup was asking changed .env"
	[ ! -d "$EXISTING/.env.setup-lock" ] || fail "a TERM while setup was asking left the lock behind"
	for leftover in "$EXISTING"/.env.setup.*; do
		[ ! -e "$leftover" ] || fail "a TERM while setup was asking left a temporary file: $leftover"
	done
	pass "an interruption while setup is asking ends it and leaves nothing behind"

	# The line is read again after the answer: changed meanwhile, nothing
	# is written — the newer pin stays.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	plain_env "ghcr.io/psimaker/vaultsync-hub:9.9.9" >"$SANDBOX/results/env-newer"
	out=$(MUTATE_BEFORE_ANSWER="cp '$SANDBOX/results/env-newer' '$EXISTING/.env'" existing_hub_run y) || fail "existing-hub setup (changed meanwhile) exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q "changed while setup was asking" || fail "a change during the question is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-newer" || fail "a change during the question was overwritten:
$(cat "$EXISTING/.env")"
	pass "a line changed while setup was asking is not overwritten"

	# A lock another setup holds: nothing is read or written.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	mkdir "$EXISTING/.env.setup-lock"
	out=$(existing_hub_run y) || { rmdir "$EXISTING/.env.setup-lock" 2>/dev/null || true; fail "existing-hub setup (locked) exited non-zero:
$out"; }
	[ -d "$EXISTING/.env.setup-lock" ] || fail "a lock another setup holds was removed"
	rmdir "$EXISTING/.env.setup-lock"
	printf '%s\n' "$out" | grep -q "Another setup may be editing" || fail "a held lock is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a held lock did not stop the edit"
	pass "a lock another setup holds leaves .env alone"

	# An exported override is captured and then unset: Compose sees .env.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	out=$(VAULTSYNC_HUB_IMAGE="$SHIPPED" existing_hub_run n) || fail "existing-hub setup (override) exited non-zero:
$out"
	grep -q "(VAULTSYNC_HUB_IMAGE=unset)" "$COMPOSE_LOG" || fail "Compose would see the exported override over .env:
$(cat "$COMPOSE_LOG")"
	if grep -v "(VAULTSYNC_HUB_IMAGE=unset)" "$COMPOSE_LOG" | grep -q .; then
		fail "a Compose call saw the override:
$(cat "$COMPOSE_LOG")"
	fi
	grep -q "^VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0$" "$EXISTING/.env" || fail "the override edited .env"
	pass "an exported image override never reaches Compose for an existing Hub"

	# A yes, then a pull that fails (#368): the Hub stays where it was —
	# .env names the image it named before, the stack is not restarted —
	# and the setup says so and names the manual step.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	if out=$(FAIL_COMPOSE_PULL=1 existing_hub_run y); then
		fail "a failed pull after a yes did not stop the setup:
$out"
	fi
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a failed pull after a yes left .env naming an image this host did not pull:
$(diff -u "$SANDBOX/results/env-before" "$EXISTING/.env" || true)"
	if grep -q " up -d" "$COMPOSE_LOG"; then
		fail "the stack was restarted after a failed pull:
$(cat "$COMPOSE_LOG")"
	fi
	printf '%s\n' "$out" | grep -q "docs/hub.md" || fail "a failed pull after a yes does not name the manual step:
$out"
	[ ! -d "$EXISTING/.env.setup-lock" ] || fail "a failed pull after a yes left the lock behind"
	for leftover in "$EXISTING"/.env.setup.* "$EXISTING"/docker-compose.yml.setup.*; do
		[ ! -e "$leftover" ] || fail "a failed pull after a yes left a staged file: $leftover"
	done
	pass "a failed pull after a yes leaves .env as it was and names the manual step (#368)"

	# A yes, then a pull that is interrupted (TERM, as when the session
	# ends): the Hub stays where it was and nothing is left behind.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	if out=$(TERM_COMPOSE_PULL=1 existing_hub_run y); then
		fail "a TERM during the pull did not end the setup:
$out"
	fi
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a TERM during the pull changed .env"
	if grep -q " up -d" "$COMPOSE_LOG"; then
		fail "the stack was restarted after an interrupted pull:
$(cat "$COMPOSE_LOG")"
	fi
	[ ! -d "$EXISTING/.env.setup-lock" ] || fail "a TERM during the pull left the lock behind"
	for leftover in "$EXISTING"/.env.setup.* "$EXISTING"/docker-compose.yml.setup.*; do
		[ ! -e "$leftover" ] || fail "a TERM during the pull left a staged file: $leftover"
	done
	pass "an interrupted pull after a yes leaves .env as it was and nothing behind (#368)"

	# .env changed by another hand while the images downloaded: the staged
	# files were made from what it held before, so neither moves.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
	cp "$EXISTING/docker-compose.yml" "$SANDBOX/results/compose-before"
	if out=$(MUTATE_DURING_PULL="printf 'GUI_PORT=9999\\n' >>'$EXISTING/.env'" existing_hub_run y); then
		fail "a change of .env during the pull did not stop the setup:
$out"
	fi
	printf '%s\n' "$out" | grep -q "changed while setup was pulling" || fail "a change of .env during the pull is not reported:
$out"
	grep -q '^GUI_PORT=9999$' "$EXISTING/.env" || fail "the change made during the pull was overwritten:
$(cat "$EXISTING/.env")"
	grep -q '^VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.1.0$' "$EXISTING/.env" || fail "the image line moved although .env changed during the pull"
	cmp -s "$EXISTING/docker-compose.yml" "$SANDBOX/results/compose-before" || fail "the compose file moved although .env changed during the pull"
	if grep -q " up -d" "$COMPOSE_LOG"; then
		fail "the stack was restarted although .env changed during the pull:
$(cat "$COMPOSE_LOG")"
	fi
	[ ! -d "$EXISTING/.env.setup-lock" ] || fail "a change during the pull left the lock behind"
	for leftover in "$EXISTING"/.env.setup.* "$EXISTING"/docker-compose.yml.setup.*; do
		[ ! -e "$leftover" ] || fail "a change during the pull left a staged file: $leftover"
	done
	pass "a change of .env during the pull moves neither file (#368)"

	# The compose file moved, then the rename onto .env fails: .env keeps its
	# old line, which names an image this host has, and the stack is not
	# restarted.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
	if out=$(FAIL_MV_ENV=1 existing_hub_run y); then
		fail "a failed rename onto .env did not stop the setup:
$out"
	fi
	printf '%s\n' "$out" | grep -q "Could not update $EXISTING/.env" || fail "a failed rename onto .env is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a failed rename onto .env changed .env"
	cmp -s "$EXISTING/docker-compose.yml" "$COMPOSE" || fail "the compose file did not move before the failed rename onto .env"
	if grep -q " up -d" "$COMPOSE_LOG"; then
		fail "the stack was restarted after a failed rename onto .env:
$(cat "$COMPOSE_LOG")"
	fi
	[ ! -d "$EXISTING/.env.setup-lock" ] || fail "a failed rename onto .env left the lock behind"
	for leftover in "$EXISTING"/.env.setup.* "$EXISTING"/docker-compose.yml.setup.*; do
		[ ! -e "$leftover" ] || fail "a failed rename onto .env left a staged file: $leftover"
	done
	pass "a failed rename onto .env after the compose file moved keeps .env and restarts nothing (#368)"

	# A yes with one override: one pull names the staged compose file, the
	# override and the staged .env.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
	printf 'services: {}\n' >"$EXISTING/docker-compose.override.yml"
	out=$(existing_hub_run y) || fail "existing-hub setup (yes, one override) exited non-zero:
$out"
	grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "-f $EXISTING/docker-compose.yml.setup.* -f $EXISTING/docker-compose.override.yml --env-file $EXISTING/.env.setup." || fail "a yes with one override did not pull the staged compose file, the override and the staged .env at once:
$(cat "$COMPOSE_LOG")"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-expected" || fail "a yes with one override did not move the image line"
	pass "a yes with one override pulls the staged files and the override in one pull (#368)"

	# A yes where setup cannot name the files: the compose file is written
	# before the pull, the staged .env still waits for it, without -f.
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	printf 'services: {}\n' >"$EXISTING/compose.override.yml"
	out=$(existing_hub_run y) || fail "existing-hub setup (yes, two overrides) exited non-zero:
$out"
	grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "--env-file $EXISTING/.env.setup." || fail "a yes with two overrides did not pull with the staged .env:
$(cat "$COMPOSE_LOG")"
	if grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- " -f "; then
		fail "a yes with two overrides named files Compose may not read:
$(cat "$COMPOSE_LOG")"
	fi
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-expected" || fail "a yes with two overrides did not move the image line after the pull"
	rm -f "$EXISTING/docker-compose.override.yml" "$EXISTING/compose.override.yml"
	pass "a yes where setup cannot name the files pulls with the staged .env, without -f (#368)"

	grep -v '^chown ' "$SANDBOX/results/violations-existing.log" | grep -q . && fail "an existing-Hub setup ran a privileged or network-changing command:
$(cat "$SANDBOX/results/violations-existing.log")"
else
	pass "existing-Hub yes/no cases skipped (no python3 for a pseudo-terminal)"
fi
if command -v setsid >/dev/null 2>&1; then
	plain_env "ghcr.io/psimaker/vaultsync-hub:0.1.0" >"$EXISTING/.env"
	cp "$EXISTING/.env" "$SANDBOX/results/env-before"
	out=$(VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 setsid sh "$SETUP_SH" --hub </dev/null 2>&1) || fail "existing-hub setup without a terminal exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q "To move by hand" || fail "without a terminal the manual step is not named:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "without a terminal .env was changed"
	pass "without a terminal .env is kept and the manual step named"
else
	pass "existing-Hub no-terminal case skipped (no setsid on this platform)"
fi

# The same through the compose file (#368): setup writes docker-compose.yml
# anew on every run, and the helper's image is named there, so a release of
# the helper moves every existing Hub that runs the link again. A pull that
# fails leaves the compose file as it was, so the next `docker compose up -d`
# still finds the images this host has, and the setup says so.
mkdir -p "$EXISTING"
plain_env "$SHIPPED" >"$EXISTING/.env"
sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
grep -q 'vaultsync-notify:0.0.1' "$EXISTING/docker-compose.yml" || fail "test setup: the older helper tag was not written"
cp "$EXISTING/docker-compose.yml" "$SANDBOX/results/compose-before"
cp "$EXISTING/.env" "$SANDBOX/results/env-before"
: >"$COMPOSE_LOG"
if out=$(VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 FAIL_COMPOSE_PULL=1 sh "$SETUP_SH" --hub </dev/null 2>&1); then
	fail "a failed pull did not stop the setup:
$out"
fi
cmp -s "$EXISTING/docker-compose.yml" "$SANDBOX/results/compose-before" || fail "a failed pull left docker-compose.yml naming images this host did not pull:
$(diff -u "$SANDBOX/results/compose-before" "$EXISTING/docker-compose.yml" || true)"
cmp -s "$EXISTING/.env" "$SANDBOX/results/env-before" || fail "a failed pull changed .env:
$(diff -u "$SANDBOX/results/env-before" "$EXISTING/.env" || true)"
if grep -q " up -d" "$COMPOSE_LOG"; then
	fail "the stack was restarted after a failed pull:
$(cat "$COMPOSE_LOG")"
fi
printf '%s\n' "$out" | grep -q "docs/hub.md" || fail "a failed pull does not name the manual step:
$out"
for leftover in "$EXISTING"/.env.setup.* "$EXISTING"/docker-compose.yml.setup.*; do
	[ ! -e "$leftover" ] || fail "a failed pull left a staged file: $leftover"
done
pass "a failed pull leaves docker-compose.yml as it was and names the manual step (#368)"

# The pull reads the staged compose file; after it the file takes the old
# one's place with the mode that one had (a temporary file starts at 0600).
sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
chmod 640 "$EXISTING/docker-compose.yml"
: >"$COMPOSE_LOG"
out=$(VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 sh "$SETUP_SH" --hub </dev/null 2>&1) || fail "an existing-Hub update exited non-zero:
$out"
cmp -s "$EXISTING/docker-compose.yml" "$COMPOSE" || fail "an existing Hub's compose file is not the stack setup ships after a pull that worked:
$(diff -u "$COMPOSE" "$EXISTING/docker-compose.yml" || true)"
[ -n "$(find "$EXISTING/docker-compose.yml" -perm 0640)" ] || fail "the compose file did not keep its mode 0640"
grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "-f $EXISTING/docker-compose.yml.setup." || fail "the pull did not read the staged compose file:
$(cat "$COMPOSE_LOG")"
if grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "--env-file"; then
	fail "the pull read a staged .env although nothing was moved:
$(cat "$COMPOSE_LOG")"
fi
if grep " up -d" "$COMPOSE_LOG" | grep -q -e " -f " -e "--env-file"; then
	fail "the start read a staged file:
$(cat "$COMPOSE_LOG")"
fi
for leftover in "$EXISTING"/docker-compose.yml.setup.*; do
	[ ! -e "$leftover" ] || fail "an update left a staged file: $leftover"
done
pass "an existing Hub's compose file moves only after the pull, with its mode (#368)"

# A compose file that is a symbolic link stays one: setup writes through it,
# as it always has — only after a pull that worked.
mkdir -p "$SANDBOX/results/linked"
sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$SANDBOX/results/linked/compose.yml"
cp "$SANDBOX/results/linked/compose.yml" "$SANDBOX/results/compose-before"
rm -f "$EXISTING/docker-compose.yml"
ln -s "$SANDBOX/results/linked/compose.yml" "$EXISTING/docker-compose.yml"
if out=$(VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 FAIL_COMPOSE_PULL=1 sh "$SETUP_SH" --hub </dev/null 2>&1); then
	fail "a failed pull did not stop the setup (linked compose file):
$out"
fi
[ -L "$EXISTING/docker-compose.yml" ] || fail "a failed pull replaced the linked compose file"
cmp -s "$SANDBOX/results/linked/compose.yml" "$SANDBOX/results/compose-before" || fail "a failed pull wrote through the linked compose file"
out=$(VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 sh "$SETUP_SH" --hub </dev/null 2>&1) || fail "an existing-Hub update exited non-zero (linked compose file):
$out"
[ -L "$EXISTING/docker-compose.yml" ] || fail "an update replaced the linked compose file with a file of its own"
cmp -s "$SANDBOX/results/linked/compose.yml" "$COMPOSE" || fail "an update did not write the stack through the linked compose file"
for leftover in "$EXISTING"/docker-compose.yml.setup.*; do
	[ ! -e "$leftover" ] || fail "an update left a staged file beside the link: $leftover"
done
rm -f "$EXISTING/docker-compose.yml"
pass "a linked compose file stays a link, written through only after a pull that worked (#368)"

# One override beside the compose file: the pull names it after the staged
# file, as Compose would load it for the later `up -d`; setup never writes it.
existing_compose_fixture() { # the Hub as an earlier setup left it, no move offered
	rm -rf "$EXISTING"; mkdir -p "$EXISTING"
	plain_env "$SHIPPED" >"$EXISTING/.env"
	sed 's|vaultsync-notify:[0-9][0-9.]*|vaultsync-notify:0.0.1|' "$COMPOSE" >"$EXISTING/docker-compose.yml"
}
existing_update() { # [ENV=VALUE…]: a non-dry-run update without a terminal
	: >"$COMPOSE_LOG"
	env VAULTSYNC_HUB_DIR="$EXISTING" VIOLATIONS="$SANDBOX/results/violations-existing.log" ALLOW_COMPOSE=1 "$@" sh "$SETUP_SH" --hub </dev/null 2>&1
}
existing_compose_fixture
printf 'services:\n  notify:\n    environment:\n      EXTRA: "1"\n' >"$EXISTING/docker-compose.override.yml"
cp "$EXISTING/docker-compose.override.yml" "$SANDBOX/results/override-before"
out=$(existing_update) || fail "an update with one override exited non-zero:
$out"
grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "-f $EXISTING/docker-compose.yml.setup.* -f $EXISTING/docker-compose.override.yml " || fail "the pull did not name the override after the staged compose file:
$(cat "$COMPOSE_LOG")"
cmp -s "$EXISTING/docker-compose.override.yml" "$SANDBOX/results/override-before" || fail "setup changed the override"
cmp -s "$EXISTING/docker-compose.yml" "$COMPOSE" || fail "the compose file did not move after a pull that worked (one override)"
pass "one override is pulled with the staged compose file and left as it is (#368)"

# An override that is a link to nothing: Compose skips it, and so does setup.
existing_compose_fixture
ln -s "$SANDBOX/results/no-such-override.yml" "$EXISTING/docker-compose.override.yml"
out=$(existing_update) || fail "an update with a dangling override link exited non-zero:
$out"
grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "-f $EXISTING/docker-compose.yml.setup." || fail "the compose file was not staged beside a dangling override link:
$(cat "$COMPOSE_LOG")"
if grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- "docker-compose.override.yml"; then
	fail "the pull named an override that is a link to nothing:
$(cat "$COMPOSE_LOG")"
fi
[ -L "$EXISTING/docker-compose.override.yml" ] || fail "setup touched the dangling override link"
rm -f "$EXISTING/docker-compose.override.yml"
pass "an override that is a link to nothing is skipped, as Compose skips it (#368)"

# Where setup cannot name the files Compose reads, the compose file is
# written before the pull, as before, and setup says so; a failed pull then
# does not claim the compose file is unchanged.
unnameable() { # NAME SETUP-STEP [ENV=VALUE…]
	name=$1; step=$2; shift 2
	existing_compose_fixture
	sh -c "$step"
	out=$(existing_update "$@") || fail "$name: an update exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q "written before the pull" || fail "$name: setup does not say the compose file is written before the pull:
$out"
	if grep " pull --quiet" "$COMPOSE_LOG" | grep -q -- " -f "; then
		fail "$name: the pull named files Compose may not read:
$(cat "$COMPOSE_LOG")"
	fi
	cmp -s "$EXISTING/docker-compose.yml" "$COMPOSE" || fail "$name: the compose file was not written"
	existing_compose_fixture
	sh -c "$step"
	if out=$(existing_update FAIL_COMPOSE_PULL=1 "$@"); then
		fail "$name: a failed pull did not stop the setup:
$out"
	fi
	if printf '%s\n' "$out" | grep -q "Nothing was changed"; then
		fail "$name: a failed pull claims nothing was changed although the compose file was written:
$out"
	fi
	printf '%s\n' "$out" | grep -q "may name images this host has not downloaded" || fail "$name: a failed pull does not say what the compose file may name:
$out"
	for leftover in "$EXISTING"/docker-compose.yml.setup.*; do
		[ ! -e "$leftover" ] || fail "$name: a staged file was left: $leftover"
	done
	pass "$name: the compose file is written before the pull, and said so (#368)"
}
unnameable "two overrides" "printf 'services: {}\\n' >'$EXISTING/docker-compose.override.yml'; printf 'services: {}\\n' >'$EXISTING/compose.override.yml'"
unnameable "a compose.yaml beside it" "printf 'services: {}\\n' >'$EXISTING/compose.yaml'"
unnameable "COMPOSE_FILE in .env" "printf 'COMPOSE_FILE=docker-compose.yml:extra.yml\\n' >>'$EXISTING/.env'"
unnameable "COMPOSE_FILE in the environment" ":" COMPOSE_FILE=docker-compose.yml
unnameable "COMPOSE_ENV_FILES in the environment" ":" COMPOSE_ENV_FILES=/elsewhere/hub.env

# An .env setup cannot read may name COMPOSE_FILE for a Compose run through
# sudo: setup cannot name the files. (Root reads any file: non-root only.)
if [ "$(id -u)" != 0 ]; then
	existing_compose_fixture
	chmod 000 "$EXISTING/.env"
	out=$(existing_update) || { chmod 644 "$EXISTING/.env"; fail "an update with an unreadable .env exited non-zero:
$out"; }
	chmod 644 "$EXISTING/.env"
	printf '%s\n' "$out" | grep -q "written before the pull" || fail "an unreadable .env was taken for one without COMPOSE_FILE:
$out"
	pass "an .env setup cannot read leaves the compose file unstaged (#368)"
else
	pass "unreadable-.env case skipped (root reads every file)"
fi

# --- 3. Device path ----------------------------------------------------------

release="https://github.com/psimaker/vaultsync/releases/download/desktop-v0.10.0"
out=$(sh "$SETUP_SH" --device --code "tulip-anchor-07" --dry-run 2>&1) ||
	fail "device dry-run exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "would download: $release/vaultsync_linux_amd64 " ||
	fail "device dry-run does not resolve the newest stable desktop-v* asset:
$out"
printf '%s\n' "$out" | grep -q "would run: <temporary folder>/vaultsync setup --code <code>\$" ||
	fail "device dry-run does not plan vaultsync setup with the code:
$out"
if printf '%s\n' "$out" | grep -qi "tulip-anchor-07"; then
	fail "device dry-run printed the pairing code:
$out"
fi
if printf '%s\n' "$out" | grep -q "already set up"; then
	fail "device dry-run mentions a Syncthing this computer does not have:
$out"
fi
pass "device dry-run plans the newest stable agent release and hides the code"

out=$(sh "$SETUP_SH" --device --dry-run -- --vault Notes --path /tmp/Notes 2>&1) ||
	fail "device dry-run with agent arguments exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "would run: <temporary folder>/vaultsync setup --vault Notes --path /tmp/Notes\$" ||
	fail "arguments after -- do not reach vaultsync setup:
$out"
pass "arguments after -- reach vaultsync setup"

# The agent takes the code as --code X, -code X, --code=X or -code=X.
for spelling in "--code=FAKE-CODE-42" "-code=FAKE-CODE-42" "-code FAKE-CODE-42"; do
	# shellcheck disable=SC2086 # the unquoted spelling splits on purpose
	out=$(sh "$SETUP_SH" --device --dry-run -- $spelling 2>&1) ||
		fail "device dry-run with $spelling exited non-zero:
$out"
	if printf '%s\n' "$out" | grep -q "FAKE-CODE-42"; then
		fail "device dry-run printed the code passed as $spelling:
$out"
	fi
done
pass "a dry run hides the code in every spelling the agent accepts"

# A release list that breaks off must not choose from what arrived.
if out=$(CURL_FAIL_API=1 sh "$SETUP_SH" --device --dry-run 2>&1); then
	fail "a failed release lookup must stop the setup:
$out"
fi
printf '%s\n' "$out" | grep -q "Could not find a VaultSync release" || fail "a failed release lookup does not say so:
$out"
pass "a failed release lookup stops before anything is chosen"

# A Syncthing of the person's own is never touched; the setup only names the
# other route (pairing that Syncthing itself).
printf '<configuration><gui><apikey>x</apikey></gui></configuration>\n' >"$HOME/.local/state/syncthing/config.xml"
out=$(sh "$SETUP_SH" --device --dry-run 2>&1) || fail "device dry-run with Syncthing exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "vaultsync-hub pair" || fail "device path does not name the route for an existing Syncthing:
$out"
printf '%s\n' "$out" | grep -q "would run: <temporary folder>/vaultsync setup\$" ||
	fail "device path with an existing Syncthing does not plan the agent:
$out"
rm "$HOME/.local/state/syncthing/config.xml"
pass "an existing Syncthing gets a word, the agent setup runs anyway"

if out=$(sh "$SETUP_SH" --hub --dry-run -- --vault Notes 2>&1); then
	fail "the Hub path must refuse arguments meant for the device setup:
$out"
fi
if out=$(sh "$SETUP_SH" --hub --dry-run --code tulip-anchor-07 2>&1); then
	fail "the Hub path must refuse a pairing code instead of ignoring it:
$out"
fi
pass "the Hub path refuses device arguments and a pairing code"

if [ -s "$VIOLATIONS" ]; then
	fail "privileged or network command executed under --dry-run:
$(cat "$VIOLATIONS")"
fi
pass "no privileged or network-changing command ran"

# --- 3b. Device path for real, with a stand-in agent ------------------------

# The "release" holds a stand-in for vaultsync that records its arguments
# and where it ran from; SHA256SUMS is computed like the release workflow does.
FAKE_RELEASE="$SANDBOX/release"
mkdir -p "$FAKE_RELEASE" "$SANDBOX/tmp"
cat >"$FAKE_RELEASE/vaultsync_linux_amd64" <<'AGENT'
#!/usr/bin/env sh
: >"$AGENT_ARGS"
for a in "$@"; do printf '[%s]\n' "$a" >>"$AGENT_ARGS"; done
printf '%s\n' "$0" >"$AGENT_SELF"
AGENT
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$FAKE_RELEASE" && sha256sum vaultsync_linux_amd64 >SHA256SUMS)
else
	(cd "$FAKE_RELEASE" && shasum -a 256 vaultsync_linux_amd64 >SHA256SUMS)
fi
export FAKE_RELEASE
AGENT_ARGS="$SANDBOX/results/agent-args"
AGENT_SELF="$SANDBOX/results/agent-self"
export AGENT_ARGS AGENT_SELF

TMPDIR="$SANDBOX/tmp" sh "$SETUP_SH" --device --code tulip-anchor-07 -- --vault "My Notes" --path "/tmp/a b" >"$SANDBOX/results/run.log" 2>&1 ||
	fail "device setup with the stand-in agent failed:
$(cat "$SANDBOX/results/run.log")"
expected='[setup]
[--code]
[tulip-anchor-07]
[--vault]
[My Notes]
[--path]
[/tmp/a b]'
[ "$(cat "$AGENT_ARGS")" = "$expected" ] || fail "the agent got other arguments:
$(cat "$AGENT_ARGS")"
[ ! -e "$(cat "$AGENT_SELF")" ] || fail "the downloaded installer is still there: $(cat "$AGENT_SELF")"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "the setup left temporary files: $(ls -A "$SANDBOX/tmp")"
pass "the verified agent runs with exactly the given arguments and is removed afterwards"

# A checksum that does not match stops before anything runs, and cleans up.
cp "$FAKE_RELEASE/SHA256SUMS" "$SANDBOX/results/sums"
sed 's/^./0/' "$SANDBOX/results/sums" >"$FAKE_RELEASE/SHA256SUMS"
rm -f "$AGENT_ARGS" "$AGENT_SELF"
if TMPDIR="$SANDBOX/tmp" sh "$SETUP_SH" --device --code tulip-anchor-07 >"$SANDBOX/results/run.log" 2>&1; then
	fail "a checksum mismatch must stop the setup"
fi
grep -q "Checksum mismatch" "$SANDBOX/results/run.log" || fail "a checksum mismatch is not reported:
$(cat "$SANDBOX/results/run.log")"
[ ! -e "$AGENT_ARGS" ] || fail "the agent ran although its checksum did not match"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "a failed download left temporary files: $(ls -A "$SANDBOX/tmp")"
cp "$SANDBOX/results/sums" "$FAKE_RELEASE/SHA256SUMS"
unset FAKE_RELEASE
pass "a checksum mismatch runs nothing and leaves nothing behind"

# --- 5. On a Mac: VaultSync for Mac ----------------------------------------

# The release has VaultSync for Mac zipped the way the release job zips it
# (zip -qry -X), beside the bare agent; the app's agent is a stand-in that
# records its arguments, where it ran from and which build it is. APPS
# stands in for /Applications.
command -v zip >/dev/null 2>&1 || fail "the VaultSync for Mac cases need zip"
MAC_RELEASE="$SANDBOX/mac-release"
APPS="$SANDBOX/Applications"
AGENT_MARK="$SANDBOX/results/agent-mark"
export AGENT_MARK
mkdir -p "$MAC_RELEASE" "$APPS" "$SANDBOX/zipwork"
# make_app DIR ID MARK puts a VaultSync for Mac.app with bundle identifier ID
# into DIR, its agent saying MARK.
make_app() {
	app="$1/VaultSync for Mac.app"
	mkdir -p "$app/Contents/MacOS"
	sed -e "s/@VERSION@/0.10.0/" -e "s/eu\.vaultsync\.desktop/$2/" "$REPO_ROOT/desktop/macos/Info.plist" >"$app/Contents/Info.plist"
	cat >"$app/Contents/MacOS/vaultsync" <<AGENT
#!/usr/bin/env sh
if [ "\$1" = version ]; then
	[ -z "\${AGENT_CANNOT_RUN:-}" ] || exit 1
	echo "vaultsync $3"
	exit 0
fi
: >"\$AGENT_ARGS"
for a in "\$@"; do printf '[%s]\n' "\$a" >>"\$AGENT_ARGS"; done
printf '%s\n' "\$0" >"\$AGENT_SELF"
printf '%s\n' "$3" >"\$AGENT_MARK"
AGENT
	printf '#!/bin/sh\n' >"$app/Contents/MacOS/vaultsync-tray"
	chmod 755 "$app/Contents/MacOS/vaultsync" "$app/Contents/MacOS/vaultsync-tray"
}
# release_with MARK [ID] zips an app into the release and lists it.
release_with() {
	rm -rf "$SANDBOX/zipwork/VaultSync for Mac.app" "$MAC_RELEASE/VaultSync_darwin_arm64.zip"
	make_app "$SANDBOX/zipwork" "${2:-eu.vaultsync.desktop}" "$1"
	(cd "$SANDBOX/zipwork" && zip -qry -X "$MAC_RELEASE/VaultSync_darwin_arm64.zip" "VaultSync for Mac.app")
	cp "$FAKE_RELEASE_BARE" "$MAC_RELEASE/vaultsync_darwin_arm64"
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$MAC_RELEASE" && sha256sum VaultSync_darwin_arm64.zip vaultsync_darwin_arm64 >SHA256SUMS)
	else
		(cd "$MAC_RELEASE" && shasum -a 256 VaultSync_darwin_arm64.zip vaultsync_darwin_arm64 >SHA256SUMS)
	fi
}
FAKE_RELEASE_BARE="$SANDBOX/release/vaultsync_linux_amd64"
mac_setup() {
	FAKE_OS=Darwin FAKE_ARCH=arm64 FAKE_RELEASE="$MAC_RELEASE" VAULTSYNC_APPLICATIONS_DIR="$APPS" TMPDIR="$SANDBOX/tmp" \
		sh "$SETUP_SH" --device --code tulip-anchor-07 "$@"
}
installed="$APPS/VaultSync for Mac.app"
release_with first

# A dry run plans the app, the place and the setup from there — and makes
# nothing.
out=$(mac_setup --dry-run 2>&1) || fail "Mac dry-run exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "would download: $release/VaultSync_darwin_arm64.zip " ||
	fail "the Mac dry-run does not plan the app:
$out"
printf '%s\n' "$out" | grep -q "would install VaultSync for Mac into $APPS\$" ||
	fail "the Mac dry-run does not plan the place:
$out"
printf '%s\n' "$out" | grep -q "would run: $installed/Contents/MacOS/vaultsync setup --code <code>\$" ||
	fail "the Mac dry-run does not plan the setup from the app:
$out"
if printf '%s\n' "$out" | grep -qi "tulip-anchor-07"; then
	fail "the Mac dry-run printed the pairing code:
$out"
fi
[ -z "$(ls -A "$APPS")" ] || fail "the Mac dry-run installed something: $(ls -A "$APPS")"
pass "on a Mac a dry run plans VaultSync for Mac in Applications and the setup from there"

# For real: the app lands in Applications and the setup runs from it, with
# exactly the given arguments; the app stays, nothing else does.
rm -f "$AGENT_ARGS" "$AGENT_SELF" "$AGENT_MARK"
mac_setup -- --vault "My Notes" >"$SANDBOX/results/run.log" 2>&1 ||
	fail "the Mac setup failed:
$(cat "$SANDBOX/results/run.log")"
[ "$(cat "$AGENT_SELF")" = "$installed/Contents/MacOS/vaultsync" ] || fail "the setup did not run from the installed app: $(cat "$AGENT_SELF")"
[ "$(cat "$AGENT_ARGS")" = '[setup]
[--code]
[tulip-anchor-07]
[--vault]
[My Notes]' ] || fail "the app's agent got other arguments:
$(cat "$AGENT_ARGS")"
[ -x "$installed/Contents/MacOS/vaultsync-tray" ] || fail "the menu-bar app is missing from the installed app"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "the Mac setup left temporary files: $(ls -A "$SANDBOX/tmp")"
pass "on a Mac the setup installs VaultSync for Mac and runs from it"

# The same build again (another vault paired, say): the installed app is
# not replaced — processes running from it keep their files — and the
# setup runs from it.
# (Inodes only: an unzipped mtime can lie in the future, and ls then
# shows the year instead of the time.)
# shellcheck disable=SC2012 # ls -di is the one inode reading BSD and GNU share
inodes() { ls -di "$installed" "$installed/Contents/MacOS/vaultsync" | awk '{print $1}' | tr '\n' ' '; }
# tree DIR…: every path under the folders with its inode — what "left as it
# is" compares (ls -l is not stable here: it shows the year for an mtime in
# the future, which an unzipped file has until its 2-second DOS time passed).
tree() { find "$@" -exec ls -di {} + 2>/dev/null | awk '{ $1 = $1; print }' | LC_ALL=C sort; }
before=$(inodes)
mac_setup >"$SANDBOX/results/run.log" 2>&1 || fail "the same-build setup failed:
$(cat "$SANDBOX/results/run.log")"
[ "$before" = "$(inodes)" ] || fail "the same build replaced the installed app"
[ "$(cat "$AGENT_SELF")" = "$installed/Contents/MacOS/vaultsync" ] || fail "the same-build setup did not run from the installed app"
pass "the same build leaves the installed VaultSync for Mac in place"

# A newer release replaces VaultSync for Mac in place.
release_with second
mac_setup >"$SANDBOX/results/run.log" 2>&1 || fail "the Mac update failed:
$(cat "$SANDBOX/results/run.log")"
[ "$(cat "$AGENT_MARK")" = second ] || fail "the update ran the old app: $(cat "$AGENT_MARK")"
[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "the update left more than the app: $(ls -A "$APPS")"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "the update left temporary files: $(ls -A "$SANDBOX/tmp")"
pass "a newer VaultSync for Mac replaces the installed one"

# A newer build that does not run on this Mac (a newer macOS needed, say)
# never takes the place of the one that runs.
release_with too-new
# (A subshell: an assignment before a function call outlives the call.)
if (AGENT_CANNOT_RUN=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "an app that cannot run here must not be installed"
fi
grep -q "does not run on this Mac" "$SANDBOX/results/run.log" || fail "the refusal does not say why:
$(cat "$SANDBOX/results/run.log")"
grep -q "vaultsync second" "$installed/Contents/MacOS/vaultsync" || fail "the running app was replaced by one that cannot run"
if grep -q too-new "$installed/Contents/MacOS/vaultsync"; then
	fail "the build that cannot run is in the app's place"
fi
[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "a refused update left more than the app: $(ls -A "$APPS")"
pass "a build that cannot run here leaves the installed one in place"

# The new app cannot be moved into place: the one set aside is put back,
# and the setup says so.
release_with third-try
if (FAIL_MV_UNPACKED=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "a failed move into place must stop the setup"
fi
grep -q "the one installed before stays" "$SANDBOX/results/run.log" || fail "the failed move is not said:
$(cat "$SANDBOX/results/run.log")"
grep -q "vaultsync second" "$installed/Contents/MacOS/vaultsync" || fail "the app installed before is not back in its place"
if grep -q third-try "$installed/Contents/MacOS/vaultsync"; then
	fail "the build that could not be moved into place is in it"
fi
[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "a failed move left more than the app: $(ls -A "$APPS")"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "a failed move left temporary files: $(ls -A "$SANDBOX/tmp")"
pass "an app set aside for an update is back when the new one cannot take its place"

# A move into place that stops halfway — a copy across disks that fails, or
# the setup interrupted (Ctrl-C) — leaves part of the new app at the app's
# place: that part goes, and the app set aside is put back.
for how in FAIL_MV_PARTIAL INT_MV_PARTIAL; do
	release_with half-way
	# (A subshell: an assignment before a function call outlives the call.)
	if (export "$how=1"; mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
		fail "a move that stopped halfway ($how) must stop the setup"
	fi
	grep -q "vaultsync second" "$installed/Contents/MacOS/vaultsync" || fail "$how: the app installed before is not back:
$(find "$installed" 2>&1 | head -20)"
	[ -x "$installed/Contents/MacOS/vaultsync-tray" ] || fail "$how: what is in the app's place is not the whole app"
	[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "$how left more than the app: $(ls -A "$APPS")"
	[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "$how left temporary files: $(ls -A "$SANDBOX/tmp")"
done
# (Which words an interrupted run prints depends on when the shell runs the
# trap — bash may finish the failed move's branch first; what is on disk is
# the same, and that is what the loop above checks.)
pass "a move into place that stops halfway leaves the app installed before, whole"

# Ctrl-C right after the installed app was set aside, before the new one
# moves: the set-aside app is the only copy, and it goes back.
release_with interrupted-aside
if (INT_AFTER_ASIDE=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "an interrupted update must stop the setup"
fi
grep -q "vaultsync second" "$installed/Contents/MacOS/vaultsync" || fail "the app set aside before an interrupt is not back:
$(find "$APPS" 2>&1 | head -20)"
[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "an interrupt after the set-aside left more than the app: $(ls -A "$APPS")"
pass "an interrupt right after the set-aside puts the installed app back"

# A second signal while the first one's restore runs — the Terminal closed
# right after Ctrl-C — enters the restore again: the app it already put
# back stays.
release_with signalled-twice
if (INT_MV_PARTIAL=1 HUP_AFTER_BACK=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "an interrupted update must stop the setup"
fi
grep -q "vaultsync second" "$installed/Contents/MacOS/vaultsync" 2>/dev/null || fail "a second signal during the restore lost the app installed before:
$(find "$APPS" 2>&1 | head -20)
$(cat "$SANDBOX/results/run.log")"
[ -x "$installed/Contents/MacOS/vaultsync-tray" ] || fail "what is back after a second signal is not the whole app"
[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "a second signal left more than the app: $(ls -A "$APPS")"
pass "a second signal during the restore keeps the app it put back"

# Putting the set-aside app back fails too: it is kept where it is, never
# removed, and the setup says where.
release_with no-way-back
if (FAIL_MV_PARTIAL=1 FAIL_MV_BACK=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "a failed update must stop the setup"
fi
grep -q "could not be put back: it is at" "$SANDBOX/results/run.log" || fail "the setup does not say where the old app is:
$(cat "$SANDBOX/results/run.log")"
kept=$(find "$APPS" -maxdepth 1 -name '.VaultSync for Mac.app.replaced.*')
[ -n "$kept" ] || fail "the old app was not kept: $(ls -A "$APPS")"
grep -q "vaultsync second" "$kept/VaultSync for Mac.app/Contents/MacOS/vaultsync" || fail "what was kept is not the old app"
grep -q "it is at $kept/VaultSync for Mac.app" "$SANDBOX/results/run.log" || fail "the setup names another place than where the old app is:
$(cat "$SANDBOX/results/run.log")"
mv "$kept/VaultSync for Mac.app" "$installed" && rmdir "$kept"
pass "an old app that cannot be put back is kept and named"

# Part of the new app that cannot be removed stands in the app's place: the
# old app is not moved into it — it stays where it was set aside, whole,
# and the setup says where both are.
if [ "$("$REAL_ID" -u)" != 0 ]; then
	release_with stuck
	if (FAIL_MV_PARTIAL=1 STUCK_PARTIAL=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
		chmod -R u+w "$APPS"
		fail "a failed update must stop the setup"
	fi
	chmod -R u+w "$APPS"
	kept=$(find "$APPS" -maxdepth 1 -name '.VaultSync for Mac.app.replaced.*')
	{ [ -n "$kept" ] && grep -q "vaultsync second" "$kept/VaultSync for Mac.app/Contents/MacOS/vaultsync"; } ||
		fail "the old app is not kept where it was set aside:
$(find "$APPS" 2>&1 | head -20)"
	[ -z "$(find "$installed" -mindepth 1 -name 'VaultSync for Mac.app' 2>/dev/null)" ] || fail "the old app was moved into the part that stayed:
$(find "$APPS" 2>&1 | head -20)"
	grep -q "could not be removed. It is at $kept/VaultSync for Mac.app" "$SANDBOX/results/run.log" || fail "the setup does not say where the old app is:
$(cat "$SANDBOX/results/run.log")"
	rm -rf "$installed"
	mv "$kept/VaultSync for Mac.app" "$installed" && rmdir "$kept"
	pass "an old app is never moved into a part of the new one that stayed"
else
	pass "the part-that-stayed case skipped (root removes anything)"
fi

# Never replaced: a bundle of that name with another identifier, or a link —
# even one to a VaultSync for Mac kept elsewhere (another disk, say): the
# link is the person's, and replacing it would delete it.
for spoiler in foreign nested link; do
	rm -rf "$installed"
	if [ "$spoiler" = foreign ]; then
		make_app "$APPS" com.example.other someone-elses
	elif [ "$spoiler" = nested ]; then
		# Its own identifier is another; VaultSync's appears only inside a
		# document type, which is not the bundle's.
		make_app "$APPS" com.example.other someone-elses
		sed 's#<dict>#<dict>\n\t<key>CFBundleDocumentTypes</key>\n\t<array><dict><key>CFBundleIdentifier</key><string>eu.vaultsync.desktop</string></dict></array>#' \
			"$installed/Contents/Info.plist" >"$SANDBOX/nested.plist"
		mv "$SANDBOX/nested.plist" "$installed/Contents/Info.plist"
	else
		make_app "$SANDBOX/elsewhere" eu.vaultsync.desktop linked
		ln -s "$SANDBOX/elsewhere/VaultSync for Mac.app" "$installed"
	fi
	before=$(tree "$APPS" "$SANDBOX/elsewhere")
	rm -f "$AGENT_ARGS" "$AGENT_MARK"
	if out=$(mac_setup --dry-run 2>&1); then
		fail "a dry run must stop at a $spoiler bundle in the app's place:
$out"
	fi
	if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
		fail "the setup must stop at a $spoiler bundle in the app's place:
$(cat "$SANDBOX/results/run.log")"
	fi
	grep -q "it was not replaced" "$SANDBOX/results/run.log" || fail "the refusal does not say so:
$(cat "$SANDBOX/results/run.log")"
	[ "$before" = "$(tree "$APPS" "$SANDBOX/elsewhere")" ] || fail "the $spoiler bundle was touched"
	[ ! -e "$AGENT_ARGS" ] || fail "an agent ran beside a $spoiler bundle"
	[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "a refused setup left temporary files: $(ls -A "$SANDBOX/tmp")"
done
rm -rf "$installed" "$SANDBOX/elsewhere"
pass "a bundle that is not VaultSync for Mac, or a link, is never replaced"

# A download that does not hold VaultSync for Mac installs nothing.
release_with impostor com.example.impostor
if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
	fail "an app with another identifier must not be installed"
fi
[ -z "$(ls -A "$APPS")" ] || fail "an impostor was installed: $(ls -A "$APPS")"
# Nor one the agent would pass over in silence (desktop/appbundle.go's
# checkApp): no menu-bar app, or an Info.plist it cannot read.
for flaw in no-tray binary-plist; do
	release_with "$flaw"
	rm -rf "$SANDBOX/zipwork/flawed" && mkdir -p "$SANDBOX/zipwork/flawed"
	(cd "$SANDBOX/zipwork/flawed" && unzip -q "$MAC_RELEASE/VaultSync_darwin_arm64.zip")
	if [ "$flaw" = no-tray ]; then
		rm "$SANDBOX/zipwork/flawed/VaultSync for Mac.app/Contents/MacOS/vaultsync-tray"
	elif [ -n "$REAL_PLUTIL" ]; then
		# Still eu.vaultsync.desktop to plutil — in a form the agent does not read.
		"$REAL_PLUTIL" -convert binary1 "$SANDBOX/zipwork/flawed/VaultSync for Mac.app/Contents/Info.plist"
	else
		printf 'bplist00' >"$SANDBOX/zipwork/flawed/VaultSync for Mac.app/Contents/Info.plist"
	fi
	rm -f "$MAC_RELEASE/VaultSync_darwin_arm64.zip"
	(cd "$SANDBOX/zipwork/flawed" && zip -qry -X "$MAC_RELEASE/VaultSync_darwin_arm64.zip" "VaultSync for Mac.app")
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$MAC_RELEASE" && sha256sum VaultSync_darwin_arm64.zip vaultsync_darwin_arm64 >SHA256SUMS)
	else
		(cd "$MAC_RELEASE" && shasum -a 256 VaultSync_darwin_arm64.zip vaultsync_darwin_arm64 >SHA256SUMS)
	fi
	if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
		fail "an app with $flaw must not be installed"
	fi
	grep -q "does not hold VaultSync for Mac" "$SANDBOX/results/run.log" || fail "$flaw: the refusal does not say why:
$(cat "$SANDBOX/results/run.log")"
	[ -z "$(ls -A "$APPS")" ] || fail "an app with $flaw was installed: $(ls -A "$APPS")"
done
# Nor does one whose checksum does not match.
release_with third
cp "$MAC_RELEASE/SHA256SUMS" "$SANDBOX/results/sums"
# Every line's first digit changes, whatever it is (0 becomes 1, others 0).
sed -e 's/^0/x/' -e 's/^[1-9a-f]/0/' -e 's/^x/1/' "$SANDBOX/results/sums" >"$MAC_RELEASE/SHA256SUMS"
cmp -s "$SANDBOX/results/sums" "$MAC_RELEASE/SHA256SUMS" && fail "the checksums were not changed"
if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
	fail "a checksum mismatch must stop the Mac setup"
fi
grep -q "Checksum mismatch" "$SANDBOX/results/run.log" || fail "the Mac checksum mismatch is not reported:
$(cat "$SANDBOX/results/run.log")"
[ -z "$(ls -A "$APPS")" ] || fail "a mismatching app was installed: $(ls -A "$APPS")"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "a failed Mac download left temporary files: $(ls -A "$SANDBOX/tmp")"
pass "a download that is not VaultSync for Mac, or does not match, installs nothing"

# A first install whose move into place fails says that nothing was
# installed — there was nothing before to stay.
rm -rf "$installed"
release_with first-try
if (FAIL_MV_PARTIAL=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "a failed first install must stop the setup"
fi
grep -q "nothing was installed" "$SANDBOX/results/run.log" || fail "a failed first install does not say so:
$(cat "$SANDBOX/results/run.log")"
[ -z "$(ls -A "$APPS")" ] || fail "a failed first install left something: $(ls -A "$APPS")"
pass "a first install that cannot be moved into place leaves nothing and says so"

# An app another account installed (its owner another — the id stand-in
# makes this account another one): left alone, and this account's goes into
# ~/Applications.
if [ "$("$REAL_ID" -u)" != 0 ]; then
	release_with theirs
	make_app "$APPS" eu.vaultsync.desktop another-accounts
	chmod 555 "$installed/Contents/MacOS" "$installed/Contents" "$installed"
	before=$(tree "$APPS")
	(FAKE_UID=4243 mac_setup) >"$SANDBOX/results/run.log" 2>&1 || { chmod -R u+w "$APPS"; fail "the setup beside another account's app failed:
$(cat "$SANDBOX/results/run.log")"; }
	[ "$before" = "$(tree "$APPS")" ] || { chmod -R u+w "$APPS"; fail "another account's app was touched"; }
	chmod -R u+w "$APPS"
	[ "$(cat "$AGENT_SELF")" = "$HOME/Applications/VaultSync for Mac.app/Contents/MacOS/vaultsync" ] || fail "not run from ~/Applications beside another account's app: $(cat "$AGENT_SELF")"
	rm -rf "$installed" "$HOME/Applications"
	pass "an app this account cannot replace stays, and this account's goes into ~/Applications"
else
	pass "the other-account case skipped (root writes anywhere)"
fi

# Applications this account may not write to: ~/Applications. An app already
# in ~/Applications is updated there even when Applications is writable.
if [ "$("$REAL_ID" -u)" != 0 ]; then
	release_with fourth
	chmod 555 "$APPS"
	mac_setup >"$SANDBOX/results/run.log" 2>&1 || { chmod 755 "$APPS"; fail "the setup into ~/Applications failed:
$(cat "$SANDBOX/results/run.log")"; }
	chmod 755 "$APPS"
	[ "$(cat "$AGENT_SELF")" = "$HOME/Applications/VaultSync for Mac.app/Contents/MacOS/vaultsync" ] || fail "not run from ~/Applications: $(cat "$AGENT_SELF")"
	release_with fifth
	mac_setup >"$SANDBOX/results/run.log" 2>&1 || fail "the update in ~/Applications failed:
$(cat "$SANDBOX/results/run.log")"
	[ "$(cat "$AGENT_MARK")" = fifth ] || fail "the app in ~/Applications was not updated: $(cat "$AGENT_MARK")"
	[ -z "$(ls -A "$APPS")" ] || fail "the update went into Applications instead: $(ls -A "$APPS")"
	rm -rf "$HOME/Applications"
	pass "without write access to Applications the app goes into ~/Applications, and is updated there"
else
	pass "the ~/Applications case skipped (root writes anywhere)"
fi

# The new app is unpacked beside its place, so putting it there is a rename
# within one folder — never a copy across disks that could stop halfway —
# and nothing of the unpacking stays. A dry run with the app installed says
# that the same build stays as it is.
release_with sixth
rm -f "$SANDBOX/results/mv-place"
(MV_PLACE_LOG="$SANDBOX/results/mv-place" mac_setup) >"$SANDBOX/results/run.log" 2>&1 || fail "the update failed:
$(cat "$SANDBOX/results/run.log")"
case "$(cat "$SANDBOX/results/mv-place")" in
	"$APPS/.VaultSync for Mac.app.new."*/unpacked/*) ;;
	*) fail "the new app was moved into place from elsewhere: $(cat "$SANDBOX/results/mv-place")" ;;
esac
[ "$(ls -A "$APPS")" = "VaultSync for Mac.app" ] || fail "the unpacking left something beside the app: $(ls -A "$APPS")"
out=$(mac_setup --dry-run 2>&1) || fail "the dry run beside the installed app failed:
$out"
printf '%s\n' "$out" | grep -q "would replace VaultSync for Mac in $APPS, unless it is already this build" ||
	fail "the dry run does not say that the same build stays:
$out"
pass "the new app is unpacked beside its place and moved there in one rename"

# Something else takes the app's place in the instant before the move (another
# setup at the same time): the new app is not left inside it, what stands
# there is not touched, and the app set aside is kept and named.
release_with seventh
if (APPEAR_AT_APP=1 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "a setup whose place was taken meanwhile must stop"
fi
grep -q "Something else was put at" "$SANDBOX/results/run.log" || fail "the taken place is not said:
$(cat "$SANDBOX/results/run.log")"
[ "$(ls -A "$installed")" = marker ] || fail "what took the place was changed: $(ls -A "$installed")"
kept=$(find "$APPS" -maxdepth 1 -name '.VaultSync for Mac.app.replaced.*')
{ [ -n "$kept" ] && grep -q "vaultsync sixth" "$kept/VaultSync for Mac.app/Contents/MacOS/vaultsync"; } ||
	fail "the app set aside was not kept: $(ls -A "$APPS")"
grep -q "installed before is at $kept/VaultSync for Mac.app" "$SANDBOX/results/run.log" || fail "the kept app is not named:
$(cat "$SANDBOX/results/run.log")"
[ -z "$(find "$APPS" -maxdepth 1 -name '.VaultSync for Mac.app.new.*')" ] || fail "the new app was left unpacked: $(ls -A "$APPS")"
rm -rf "$installed"
mv "$kept/VaultSync for Mac.app" "$installed" && rmdir "$kept"
pass "a place taken meanwhile is left as it is, and the app set aside is kept"

# A TMPDIR this account cannot use (another account's, after su without -):
# the setup takes /tmp instead of stopping.
rm -f "$AGENT_SELF"
(TMPDIR="$SANDBOX/no-such-folder" FAKE_OS=Darwin FAKE_ARCH=arm64 FAKE_RELEASE="$MAC_RELEASE" VAULTSYNC_APPLICATIONS_DIR="$APPS" \
	sh "$SETUP_SH" --device --code tulip-anchor-07) >"$SANDBOX/results/run.log" 2>&1 || fail "a setup with an unusable TMPDIR failed:
$(cat "$SANDBOX/results/run.log")"
[ "$(cat "$AGENT_SELF")" = "$installed/Contents/MacOS/vaultsync" ] || fail "the setup with an unusable TMPDIR did not run the app"
pass "an unusable TMPDIR does not stop the setup"

# VaultSync for Mac in ~/Applications that this account cannot replace
# (locked), with Applications not writable: the setup stops and says why,
# instead of running an app the agent passes over.
if [ "$("$REAL_ID" -u)" != 0 ]; then
	release_with eighth
	mkdir -p "$HOME/Applications"
	mv "$installed" "$HOME/Applications/VaultSync for Mac.app"
	locked="$HOME/Applications/VaultSync for Mac.app"
	chmod 555 "$locked/Contents/MacOS" "$locked/Contents" "$locked"
	chmod 555 "$APPS"
	before=$(tree "$HOME/Applications")
	if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
		chmod 755 "$APPS"
		chmod -R u+w "$HOME/Applications"
		fail "a setup beside a locked app must stop"
	fi
	chmod 755 "$APPS"
	[ "$before" = "$(tree "$HOME/Applications")" ] || { chmod -R u+w "$HOME/Applications"; fail "the locked app was touched"; }
	chmod -R u+w "$HOME/Applications"
	grep -q "is locked (Finder → Get Info → Locked). Unlock it" "$SANDBOX/results/run.log" || fail "the refusal does not say why:
$(cat "$SANDBOX/results/run.log")"
	rm -rf "$HOME/Applications"
	pass "a locked app in ~/Applications stops the setup with the reason"

	# The same app locked in an Applications folder this account may change:
	# it stops too — never a second app in ~/Applications that would take the
	# service away from it.
	release_with tenth
	make_app "$APPS" eu.vaultsync.desktop locked-own
	chmod 555 "$installed/Contents/MacOS" "$installed/Contents" "$installed"
	before=$(tree "$APPS")
	if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
		chmod -R u+w "$APPS"
		fail "a setup beside a locked app of this account's must stop"
	fi
	[ "$before" = "$(tree "$APPS")" ] || { chmod -R u+w "$APPS"; fail "the locked app was touched"; }
	chmod -R u+w "$APPS"
	grep -q "is locked (Finder → Get Info → Locked). Unlock it" "$SANDBOX/results/run.log" || fail "the refusal does not say why:
$(cat "$SANDBOX/results/run.log")"
	[ ! -e "$HOME/Applications/VaultSync for Mac.app" ] || fail "a second app went into ~/Applications beside a locked one"
	rm -rf "$installed" "$HOME/Applications"
	pass "a locked app of this account's stops the setup in either Applications folder"
else
	pass "the locked-app case skipped (root writes anywhere)"
fi

# This account's app in an Applications folder only an administrator may
# change: the setup cannot replace it there and stops with the reason —
# never a second app in ~/Applications, which would take the service away
# from the one the person knows.
if [ "$("$REAL_ID" -u)" != 0 ]; then
	release_with ninth
	make_app "$APPS" eu.vaultsync.desktop own-in-admin-folder
	chmod 555 "$APPS"
	before=$(tree "$APPS")
	if mac_setup >"$SANDBOX/results/run.log" 2>&1; then
		chmod 755 "$APPS"
		fail "a setup beside its own app in an admin-only folder must stop"
	fi
	chmod 755 "$APPS"
	grep -q "is yours, but only an administrator may change that folder" "$SANDBOX/results/run.log" || fail "the refusal does not say why:
$(cat "$SANDBOX/results/run.log")"
	[ "$before" = "$(tree "$APPS")" ] || fail "the app in the admin-only folder was touched"
	[ ! -e "$HOME/Applications/VaultSync for Mac.app" ] || fail "a second app went into ~/Applications"
	rm -rf "$installed" "$HOME/Applications"
	pass "this account's app where only an administrator may replace it stops the setup with the reason"
else
	pass "the admin-only-folder case skipped (root writes anywhere)"
fi

# As root (sudo) the device path stops before anything: the service is the
# account's that uses Obsidian, and as root every check of whose an app is
# would pass.
for os in Darwin Linux; do
	if (FAKE_UID=0 FAKE_OS=$os FAKE_ARCH=arm64 FAKE_RELEASE="$MAC_RELEASE" VAULTSYNC_APPLICATIONS_DIR="$APPS" TMPDIR="$SANDBOX/tmp" \
		sh "$SETUP_SH" --device --code tulip-anchor-07) >"$SANDBOX/results/run.log" 2>&1; then
		fail "the device path ran as root on $os"
	fi
	grep -q "without sudo" "$SANDBOX/results/run.log" || fail "the root refusal on $os does not say why:
$(cat "$SANDBOX/results/run.log")"
done
[ -z "$(ls -A "$APPS")" ] || fail "a refused root setup installed something: $(ls -A "$APPS")"
pass "the device path refuses to run as root"

# A Mac older than macOS 12 (#269): neither VaultSync for Mac nor the bare
# agent runs there, and macOS 11's plutil called a valid download not
# VaultSync for Mac. The setup stops before the release lookup with the
# reason. The sw_vers stand-in sits in a folder of its own, used only here:
# every other case meets the real one on a Mac and none on Linux, where CI
# runs the branch without sw_vers. With SYSTEM_VERSION_COMPAT=1 it answers
# 10.16, as macOS does from 11 on.
SW_VERS_BIN="$SANDBOX/sw_vers-bin"
mkdir -p "$SW_VERS_BIN"
cat >"$SW_VERS_BIN/sw_vers" <<'SHIM'
#!/usr/bin/env sh
[ "$*" = "-productVersion" ] || exit 2
[ -z "${FAKE_MACOS_FAILS:-}" ] || exit 1
if [ "${SYSTEM_VERSION_COMPAT:-}" = 1 ]; then
	echo 10.16
	exit 0
fi
printf '%s\n' "$FAKE_MACOS"
SHIM
chmod +x "$SW_VERS_BIN/sw_vers"
for v in 11.7.10 10.15.7 11; do
	if out=$(PATH="$SW_VERS_BIN:$PATH" FAKE_MACOS=$v mac_setup --dry-run 2>&1); then
		fail "the setup went on on macOS $v:
$out"
	fi
	printf '%s\n' "$out" | grep -q "VaultSync for Mac needs macOS 12 or newer; this Mac has macOS $v\. Nothing was installed\.\$" ||
		fail "the refusal on macOS $v does not say why:
$out"
	if printf '%s\n' "$out" | grep -q -e "VaultSync for Mac and Linux" -e "would download"; then
		fail "the setup looked up or planned a release on macOS $v:
$out"
	fi
done
rm -f "$AGENT_SELF"
# A subshell: assignments before a shell function outlive the call in sh.
if (PATH="$SW_VERS_BIN:$PATH" FAKE_MACOS=11.7.10 mac_setup) >"$SANDBOX/results/run.log" 2>&1; then
	fail "a real setup went on on macOS 11"
fi
grep -q "needs macOS 12 or newer" "$SANDBOX/results/run.log" || fail "the real refusal on macOS 11 does not say why:
$(cat "$SANDBOX/results/run.log")"
[ ! -e "$AGENT_SELF" ] || fail "an agent ran on macOS 11"
[ -z "$(ls -A "$APPS")" ] || fail "a setup on macOS 11 installed something: $(ls -A "$APPS")"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "a setup on macOS 11 left temporary files: $(ls -A "$SANDBOX/tmp")"
# An exported SYSTEM_VERSION_COMPAT=1 does not hide macOS 11 behind 10.16.
if out=$(PATH="$SW_VERS_BIN:$PATH" SYSTEM_VERSION_COMPAT=1 FAKE_MACOS=11.7.10 mac_setup --dry-run 2>&1); then
	fail "SYSTEM_VERSION_COMPAT=1 hid macOS 11:
$out"
fi
printf '%s\n' "$out" | grep -q "this Mac has macOS 11.7.10\." || fail "SYSTEM_VERSION_COMPAT=1 changed the version named:
$out"
pass "on a Mac older than macOS 12 the setup stops before the release lookup and says why"

# It refuses only on proof: macOS 12 and later, a version it cannot read
# (10.16 is the compatibility answer of macOS 11 and later), a failed sw_vers, and an exported SYSTEM_VERSION_COMPAT=1 on a newer Mac all
# go on; on Linux sw_vers is never asked.
for v in 12.0 12 26.5 27.2.1 10.16 10.16.1 99999999999999999999.0 011.0 "" "unknown" "abc.1"; do
	out=$(PATH="$SW_VERS_BIN:$PATH" FAKE_MACOS=$v mac_setup --dry-run 2>&1) ||
		fail "the setup stopped on macOS '$v':
$out"
	printf '%s\n' "$out" | grep -q "would run: $installed/Contents/MacOS/vaultsync setup --code <code>\$" ||
		fail "the setup on macOS '$v' does not plan the setup:
$out"
done
out=$(PATH="$SW_VERS_BIN:$PATH" FAKE_MACOS_FAILS=1 mac_setup --dry-run 2>&1) ||
	fail "a failed sw_vers stopped the setup:
$out"
out=$(PATH="$SW_VERS_BIN:$PATH" SYSTEM_VERSION_COMPAT=1 FAKE_MACOS=14.6 mac_setup --dry-run 2>&1) ||
	fail "SYSTEM_VERSION_COMPAT=1 stopped the setup on macOS 14:
$out"
out=$(PATH="$SW_VERS_BIN:$PATH" FAKE_MACOS=11.0 sh "$SETUP_SH" --device --dry-run 2>&1) ||
	fail "a sw_vers saying 11 stopped the setup on Linux:
$out"
[ -z "$(ls -A "$APPS")" ] || fail "a dry run installed something: $(ls -A "$APPS")"
pass "macOS 12 and later, an unreadable version, a failed sw_vers and Linux go on"

# A release without the app (desktop-v0.1.0): the bare agent, as before.
rm -f "$MAC_RELEASE/VaultSync_darwin_arm64.zip"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$MAC_RELEASE" && sha256sum vaultsync_darwin_arm64 >SHA256SUMS)
else
	(cd "$MAC_RELEASE" && shasum -a 256 vaultsync_darwin_arm64 >SHA256SUMS)
fi
out=$(mac_setup --dry-run 2>&1) || fail "Mac dry-run without the app exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "would download: $release/vaultsync_darwin_arm64 " ||
	fail "without the app the Mac dry-run does not plan the bare agent:
$out"
printf '%s\n' "$out" | grep -q "would run: <temporary folder>/vaultsync setup --code <code>\$" ||
	fail "without the app the Mac dry-run does not plan the setup from the download:
$out"
rm -f "$AGENT_ARGS" "$AGENT_SELF"
mac_setup >"$SANDBOX/results/run.log" 2>&1 || fail "the bare-agent Mac setup failed:
$(cat "$SANDBOX/results/run.log")"
[ ! -e "$(cat "$AGENT_SELF")" ] || fail "the bare installer is still there: $(cat "$AGENT_SELF")"
[ -z "$(ls -A "$APPS")" ] || fail "an app appeared without one in the release: $(ls -A "$APPS")"
[ -z "$(ls -A "$SANDBOX/tmp")" ] || fail "the bare-agent Mac setup left temporary files: $(ls -A "$SANDBOX/tmp")"
pass "a release without VaultSync for Mac sets up with the bare agent"

# --- 4. Embedded compose = reviewed compose ----------------------------------

awk '/^\tcat >"\$compose" <<'"'"'COMPOSE'"'"'$/{flag=1; next} /^COMPOSE$/{flag=0} flag' "$SETUP_SH" >"$SANDBOX/embedded.yml"
if ! cmp -s "$SANDBOX/embedded.yml" "$COMPOSE"; then
	diff -u "$COMPOSE" "$SANDBOX/embedded.yml" >&2 || true
	fail "compose embedded in setup.sh differs from hub/docker-compose.yml"
fi
pass "embedded compose matches hub/docker-compose.yml"

# --- Menu without a terminal must not hang or guess ---------------------------

# setsid detaches from the controlling terminal so `ask` cannot read /dev/tty;
# without it (macOS) the check would block on a developer's terminal, so skip.
if command -v setsid >/dev/null 2>&1; then
	if out=$(setsid sh "$SETUP_SH" --dry-run </dev/null 2>&1); then
		fail "menu without a terminal must fail instead of guessing:
$out"
	fi
	printf '%s\n' "$out" | grep -q -- "--device or --hub" || fail "no-terminal case does not name the flags:
$out"
	pass "menu without a terminal names --device/--hub and stops"
else
	pass "menu-without-terminal check skipped (no setsid on this platform)"
fi

echo "setup.sh dry-run smoke test: all checks passed"

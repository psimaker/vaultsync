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
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
SETUP_SH="$REPO_ROOT/hub/scripts/setup.sh"
COMPOSE="$REPO_ROOT/hub/docker-compose.yml"

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

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
	printf 'docker %s (VAULTSYNC_HUB_IMAGE=%s)\n' "$*" "${VAULTSYNC_HUB_IMAGE-unset}" >>"$COMPOSE_LOG"
	case "$*" in
		"compose --project-directory "*" pull --quiet" | "compose --project-directory "*" up -d") exit 0 ;;
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
	[ -n "$out" ] && [ -f "$FAKE_RELEASE/${url##*/}" ] && cp "$FAKE_RELEASE/${url##*/}" "$out" && exit 0
	exit 22
fi
printf 'curl %s\n' "$*" >>"$VIOLATIONS"
exit 1
SHIM
# The Hub path is Linux-only; pin uname so the test is the same on a macOS
# developer machine and on the Linux CI runner.
cat >"$SANDBOX/bin/uname" <<'SHIM'
#!/usr/bin/env sh
case "$*" in
	-s) echo Linux ;;
	-m) echo x86_64 ;;
	*) echo Linux ;;
esac
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
	out=$(existing_hub_run y) || fail "existing-hub setup (yes) exited non-zero:
$out"
	printf '%s\n' "$out" | grep -q ".env now names $SHIPPED" || fail "a yes is not reported:
$out"
	cmp -s "$EXISTING/.env" "$SANDBOX/results/env-expected" || fail "a yes changed more than the image line:
$(diff -u "$SANDBOX/results/env-expected" "$EXISTING/.env" || true)"
	if ! grep -q "pull --quiet" "$COMPOSE_LOG" || ! grep -q " up -d" "$COMPOSE_LOG"; then
		fail "the stack was not pulled and restarted after a yes:
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
		[ ! -s "$COMPOSE_LOG" ] || fail "$1 still restarted the stack:
$(cat "$COMPOSE_LOG")"
		for leftover in "$EXISTING"/.env.setup.*; do
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

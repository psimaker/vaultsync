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

cat >"$SANDBOX/bin/docker" <<'SHIM'
#!/usr/bin/env sh
case "$*" in
	"info") exit 0 ;;
	"compose version") echo "Docker Compose version v2.0.0"; exit 0 ;;
esac
printf 'docker %s\n' "$*" >>"$VIOLATIONS"
exit 1
SHIM
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
pass "the Hub path refuses device arguments"

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

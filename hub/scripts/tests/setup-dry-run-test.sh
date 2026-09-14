#!/usr/bin/env sh
# Hermetic smoke test for setup.sh --dry-run.
#
# Runs the one-link setup for both menu choices with every external command
# (docker, curl, sudo, chown) replaced by PATH shims and asserts:
#   1. --dry-run exits 0 and creates nothing under the Hub directory.
#   2. The Hub path prints the compose/env plan with the caller's uid:gid and
#      the discovered port fallback.
#   3. The device path never executes a privileged or network-changing command
#      (sudo/chown shims are tripwires; curl only answers the release lookup).
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
cat >"$SANDBOX/bin/curl" <<'SHIM'
#!/usr/bin/env sh
case "$*" in
	*api.github.com*) printf '[{"tag_name": "v2.0.2"}, {"tag_name": "hub-v0.1.0"}]\n'; exit 0 ;;
esac
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

printf '<configuration><gui><apikey>x</apikey></gui></configuration>\n' >"$HOME/.local/state/syncthing/config.xml"
out=$(sh "$SETUP_SH" --device --code "tulip-anchor-07" --dry-run 2>&1) ||
	fail "device dry-run exited non-zero:
$out"
printf '%s\n' "$out" | grep -q "would download: https://github.com/psimaker/vaultsync/releases/download/hub-v0.1.0/vaultsync-hub_" ||
	fail "device dry-run does not resolve the newest hub-v* release asset:
$out"
printf '%s\n' "$out" | grep -q "pair --code tulip-anchor-07" ||
	fail "device dry-run does not plan the pair command:
$out"
pass "device dry-run resolves the release and plans pairing"

# Without Syncthing the device path must explain and exit 0, never install.
rm "$HOME/.local/state/syncthing/config.xml"
out=$(sh "$SETUP_SH" --device --dry-run 2>&1) || fail "device dry-run without Syncthing exited non-zero:
$out"
printf '%s\n' "$out" | grep -qi "no Syncthing yet" || fail "device path without Syncthing does not explain itself:
$out"
pass "device path without Syncthing explains and stops"

if [ -s "$VIOLATIONS" ]; then
	fail "privileged or network command executed under --dry-run:
$(cat "$VIOLATIONS")"
fi
pass "no privileged or network-changing command ran"

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

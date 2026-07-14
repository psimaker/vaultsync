#!/usr/bin/env sh
# Hermetic smoke test for install.sh --dry-run (#82).
#
# Runs the installer against a fixture config.xml with every external command
# (docker, curl, sudo, systemctl) replaced by PATH shims, and asserts:
#   1. --dry-run exits 0 and changes nothing on disk.
#   2. The config owner's uid:gid lands in the printed docker/systemd commands
#      (a mismatched uid is the #1 setup failure the installer exists to avoid).
#   3. Nothing privileged is ever executed under --dry-run: the sudo/systemctl
#      shims are tripwires, docker/curl only answer the read-only probes.
#   4. Asset-name contract: the runtime-built release asset name matches the
#      name pattern docker.yml's release loop produces — a rename on either
#      side breaks installs at runtime, so both sides are asserted here.
set -eu

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../../.." && pwd)
INSTALL_SH="$REPO_ROOT/notify/scripts/install.sh"

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

pass() {
	printf 'ok: %s\n' "$*"
}

# --- Sandbox layout ----------------------------------------------------------

mkdir -p "$SANDBOX/bin" "$SANDBOX/work" "$SANDBOX/home" "$SANDBOX/config" "$SANDBOX/results"
printf '<configuration></configuration>\n' >"$SANDBOX/config/config.xml"
CONFIG="$SANDBOX/config/config.xml"
EXPECTED_OWNER="$(id -u):$(id -g)"

VIOLATIONS="$SANDBOX/results/violations.log"

# Read-only probes are answered; anything else under --dry-run is a defect.
cat >"$SANDBOX/bin/docker" <<EOF
#!/bin/sh
case "\$1" in
	info) exit 0 ;;
	ps) exit 0 ;;
	*) echo "docker \$*" >>"$VIOLATIONS"; exit 1 ;;
esac
EOF

# Only the GitHub release-tag lookup is legitimate under --dry-run.
cat >"$SANDBOX/bin/curl" <<EOF
#!/bin/sh
for arg in "\$@"; do
	case "\$arg" in
		https://api.github.com/*)
			printf '%s\n' '"tag_name": "notify-v0.0.0-test"'
			exit 0
			;;
	esac
done
echo "curl \$*" >>"$VIOLATIONS"
exit 1
EOF

# Tripwire with two read-only exceptions: the #87 cross-flavor guard probes
# is-enabled/is-active (answered "no such unit"); anything else under
# --dry-run must only be printed, never executed.
cat >"$SANDBOX/bin/systemctl" <<EOF
#!/bin/sh
case "\$1" in
	is-enabled | is-active) exit 4 ;;
	*) echo "systemctl \$*" >>"$VIOLATIONS"; exit 1 ;;
esac
EOF

# Pure tripwire: --dry-run must never escalate.
cat >"$SANDBOX/bin/sudo" <<EOF
#!/bin/sh
echo "sudo \$*" >>"$VIOLATIONS"
exit 1
EOF
chmod 755 "$SANDBOX/bin/docker" "$SANDBOX/bin/curl" "$SANDBOX/bin/sudo" "$SANDBOX/bin/systemctl"

snapshot() {
	(cd "$SANDBOX" && find work config home -type f | sort)
}

run_installer() {
	# env -i: the installer must not depend on ambient state; HOME and the
	# fixture config are the only inputs. Shims shadow the real tools.
	(
		cd "$SANDBOX/work" && env -i \
			PATH="$SANDBOX/bin:/usr/bin:/bin" \
			HOME="$SANDBOX/home" \
			SYNCTHING_CONFIG="$CONFIG" \
			VAULTSYNC_NOTIFY_MODE="$1" \
			sh "$INSTALL_SH" --dry-run
	)
}

BEFORE=$(snapshot)

# --- Run A: docker flavor ------------------------------------------------------

OUT_DOCKER="$SANDBOX/results/out-docker.txt"
run_installer docker >"$OUT_DOCKER" 2>&1 || {
	cat "$OUT_DOCKER" >&2
	fail "docker-mode --dry-run exited non-zero"
}
grep -q 'Dry run complete' "$OUT_DOCKER" || fail "docker mode: missing 'Dry run complete'"
grep -q 'would run: docker run -d --name vaultsync-notify' "$OUT_DOCKER" \
	|| fail "docker mode: start command not printed"
grep "would run: docker run -d" "$OUT_DOCKER" | grep -q -- "-u $EXPECTED_OWNER " \
	|| fail "docker mode: config owner $EXPECTED_OWNER missing from the printed docker run command"
# Re-run = deliberate re-resolution (#87): without an explicit pull, Docker
# silently keeps the old local version-tag target.
grep -q 'would run: docker pull ' "$OUT_DOCKER" \
	|| fail "docker mode: no docker pull before start — a re-run would keep the old image (#87)"
grep -q 'would run: docker pull ghcr.io/psimaker/vaultsync-notify:2.0.0' "$OUT_DOCKER" \
	|| fail "docker mode: default image is not the reviewed 2.0.0 tag"
grep -q 'runtime_image=[$]new_image_id' "$INSTALL_SH" \
	|| fail "docker mode: the successful pull is not converted to an immutable local image ID"
if grep -q 'continuing with the LOCAL image' "$INSTALL_SH"; then
	fail "docker mode: failed pulls still permit a stale local-image fallback"
fi
pass "docker mode: dry run clean, owner $EXPECTED_OWNER in the start command, pull before start"

# --- Run B: binary flavor ------------------------------------------------------

# Expected asset name, mapped exactly like install.sh's detect_asset.
case "$(uname -s)" in
	Linux) GOOS=linux ;;
	Darwin) GOOS=darwin ;;
	*) fail "unsupported test host: $(uname -s)" ;;
esac
case "$(uname -m)" in
	x86_64 | amd64) GOARCH=amd64 ;;
	aarch64 | arm64) GOARCH=arm64 ;;
	*) fail "unsupported test arch: $(uname -m)" ;;
esac
EXPECTED_ASSET="vaultsync-notify_${GOOS}_${GOARCH}"

OUT_BINARY="$SANDBOX/results/out-binary.txt"
run_installer binary >"$OUT_BINARY" 2>&1 || {
	cat "$OUT_BINARY" >&2
	fail "binary-mode --dry-run exited non-zero"
}
grep -q "would download: .*/notify-v0.0.0-test/$EXPECTED_ASSET " "$OUT_BINARY" \
	|| fail "binary mode: expected asset $EXPECTED_ASSET not in the download line"
pass "binary mode: runtime asset name is $EXPECTED_ASSET"

# The systemd unit is only rendered where a systemd host is detectable.
if [ "$GOOS" = linux ] && [ -d /run/systemd/system ]; then
	grep -q "User=$(id -u)\$" "$OUT_BINARY" || fail "systemd unit: User= line missing config owner uid"
	grep -q "Group=$(id -g)\$" "$OUT_BINARY" || fail "systemd unit: Group= line missing config owner gid"
	# Re-run = upgrade (#87): enable --now does NOT restart an already-active
	# unit, so the old process would keep running after a binary swap.
	grep -q 'systemctl restart vaultsync-notify' "$OUT_BINARY" \
		|| fail "systemd: no restart after enable — a re-run would keep the old process (#87)"
	pass "systemd unit: runs as config owner $EXPECTED_OWNER, restart on re-run"
else
	pass "systemd unit render skipped (no systemd on this host)"
fi

# --- Cross-run assertions ------------------------------------------------------

[ ! -e "$VIOLATIONS" ] || {
	cat "$VIOLATIONS" >&2
	fail "--dry-run executed a command it must only print (see above)"
}
pass "no privileged or mutating command was executed"

AFTER=$(snapshot)
[ "$BEFORE" = "$AFTER" ] || {
	printf 'before:\n%s\nafter:\n%s\n' "$BEFORE" "$AFTER" >&2
	fail "--dry-run created or removed files"
}
pass "no files created or removed"

# --- Asset-name contract: install.sh <-> docker.yml release loop ---------------

grep -qF 'vaultsync-notify_%s_%s' "$INSTALL_SH" \
	|| fail "install.sh no longer builds asset names as vaultsync-notify_<goos>_<goarch>"
# The ${output_directory}/${goos}/${goarch} literals belong to docker.yml, not
# this shell. Both dist and verify-dist use the same exact name before cmp.
# shellcheck disable=SC2016
grep -qF 'out="${output_directory}/vaultsync-notify_${goos}_${goarch}"' "$REPO_ROOT/.github/workflows/docker.yml" \
	|| fail "docker.yml release loop no longer produces vaultsync-notify_<goos>_<goarch> assets"
pass "asset-name contract holds on both sides"

grep -q 'Could not fetch SHA256SUMS.*no binary was installed' "$INSTALL_SH" \
	|| fail "binary mode: missing checksum assets do not fail closed"
grep -q 'sha256sum or shasum is required.*no binary was installed' "$INSTALL_SH" \
	|| fail "binary mode: a missing SHA-256 implementation does not fail closed"
grep -q 'matches != 1' "$INSTALL_SH" \
	|| fail "binary mode: a missing or duplicate asset checksum does not fail closed"
grep -q 'non-canonical checksum' "$INSTALL_SH" \
	|| fail "binary mode: malformed checksum text does not fail closed"
pass "binary install requires one canonical checksum and cannot bypass verification"

printf 'All install.sh --dry-run smoke tests passed.\n'

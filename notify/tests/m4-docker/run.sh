#!/bin/sh
set -eu

script_directory=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
notify_directory=$(CDPATH='' cd -- "$script_directory/../.." && pwd)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/vaultsync-m4-docker.XXXXXX")
image="vaultsync-m4-namespace-test:local-$$"

cleanup() {
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf "$fixture"
}
trap cleanup EXIT INT TERM

vault="$fixture/vault"
state="$fixture/state"
attacker="$fixture/attacker-installations"
config="$fixture/config.xml"
mkdir -m 0700 "$vault" "$vault/.stfolder" "$state" "$attacker"
printf '%s\n' 'user note sentinel' >"$vault/user-note.txt"
printf '%s\n' '<configuration version="test-only" />' >"$config"
chmod 0600 "$vault/user-note.txt" "$config"

docker build --file "$script_directory/Dockerfile" --tag "$image" "$notify_directory"

common_arguments="--rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges --user $(id -u):$(id -g) --tmpfs /tmp:rw,noexec,nosuid,size=64m"

# Explicit local installer phase: this is the only phase that receives the
# selected Syncthing folder root. It creates the exact child after consent.
# shellcheck disable=SC2086
docker run $common_arguments \
  --mount "type=bind,src=$vault,dst=/selected-vault" \
  --mount "type=bind,src=$state,dst=/state" \
  --env VAULTSYNC_M4_DOCKER_PARENT=/selected-vault \
  --env VAULTSYNC_M4_DOCKER_STATE=/state \
  "$image" -test.run '^TestDiagnosticsNamespaceDockerInstallerPhase$' -test.count=1 -test.v

namespace="$vault/VaultSync Diagnostics"
test -d "$namespace"
test -f "$namespace/root-manifest.cbor"

# Dormant runtime proof: mount only the exact existing namespace, a separate
# state directory, and a read-only config fixture. The parent vault, Docker
# socket, ports, capabilities, and writable container root are absent.
# shellcheck disable=SC2086
docker run $common_arguments \
  --mount "type=bind,src=$namespace,dst=/diagnostics" \
  --mount "type=bind,src=$state,dst=/state" \
  --mount "type=bind,src=$config,dst=/config/config.xml,readonly" \
  --env VAULTSYNC_M4_DOCKER_ROOT=/diagnostics \
  --env VAULTSYNC_M4_DOCKER_STATE=/state \
  --env VAULTSYNC_M4_DOCKER_CONFIG=/config/config.xml \
  "$image" -test.run '^TestDiagnosticsNamespaceDockerRuntimeConfinement$' -test.count=1 -test.v

# A second mount placed over a fixed child must have another Linux mount ID and
# be rejected before its contents are enumerated or changed.
# shellcheck disable=SC2086
docker run $common_arguments \
  --mount "type=bind,src=$namespace,dst=/diagnostics" \
  --mount "type=bind,src=$attacker,dst=/diagnostics/installations" \
  --env VAULTSYNC_M4_DOCKER_MOUNT_SWAP_ROOT=/diagnostics \
  "$image" -test.run '^TestDiagnosticsNamespaceDockerMountSwapRejected$' -test.count=1 -test.v

test "$(cat "$vault/user-note.txt")" = 'user note sentinel'
test ! -e "$namespace/namespace-v1.json"
test -f "$state/namespace-v1.json"

printf '%s\n' 'M4 Docker host-bind confinement proof passed'

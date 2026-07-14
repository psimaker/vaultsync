#!/bin/sh
# Real installer/upgrade/rollback proof for the one supported packaging row:
# rootful Docker Engine on a standard Linux host. Run only on an ephemeral test
# host: it deliberately exercises the root-only operator installer.
set -eu

old_commit=85e527db180a17d9d23f9ba233cf5abd0e9671f6
script_directory=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
notify_directory=$(CDPATH='' cd -- "$script_directory/../.." && pwd)
repository_directory=$(CDPATH='' cd -- "$notify_directory/.." && pwd)
installer=$notify_directory/scripts/diagnostics-docker.sh
run_id=$$
old_tag=vaultsync-runtime-linux-old:$run_id
new_tag=vaultsync-runtime-linux-new:$run_id
mock_tag=vaultsync-runtime-linux-mock:$run_id
container_name=vaultsync-runtime-linux-helper-$run_id
mock_name=vaultsync-runtime-linux-mock-$run_id
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/vaultsync-runtime-linux.XXXXXX")
api_key=runtime-linux-api-key-sentinel
folder_id=runtime-linux-proof
mock_port=$((20000 + run_id % 10000))
helper_port=$((mock_port + 1))

cleanup() {
	docker rm -f "$container_name" "$mock_name" >/dev/null 2>&1 || true
	docker image rm "$old_tag" "$new_tag" "$mock_tag" >/dev/null 2>&1 || true
	rm -rf "$temporary_directory"
}
trap cleanup EXIT INT TERM

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

[ "$(uname -s)" = Linux ] || fail 'this proof requires a standard Linux host'
[ "$(id -u)" -eq 0 ] || fail 'this proof must exercise the root-only installer'
command -v docker >/dev/null 2>&1 || fail 'Docker Engine is required'
docker info >/dev/null 2>&1 || fail 'rootful Docker Engine is unavailable'
case $(docker info --format '{{json .SecurityOptions}}') in
	*rootless*) fail 'rootless Docker is not a supported proof host' ;;
esac

mkdir -p "$temporary_directory/old" "$temporary_directory/config-source"
git -c "safe.directory=$repository_directory" -C "$repository_directory" \
	cat-file -e "$old_commit^{commit}" || fail 'old helper commit is unavailable'
git -c "safe.directory=$repository_directory" -C "$repository_directory" \
	archive "$old_commit" notify | tar -x -C "$temporary_directory/old"

docker build --build-arg "VERSION=packaging-old-$old_commit" -t "$old_tag" "$temporary_directory/old/notify"
new_commit=$(git -c "safe.directory=$repository_directory" -C "$repository_directory" rev-parse HEAD)
[ -z "$(git -c "safe.directory=$repository_directory" -C "$repository_directory" status --porcelain -- notify .github/workflows/ci.yml)" ] || \
	fail 'Linux-host packaging proof requires a clean committed source tree'
docker build --build-arg "VERSION=packaging-new-$new_commit" -t "$new_tag" "$notify_directory"
docker build -t "$mock_tag" "$script_directory/mock"

old_image=$(docker image inspect --format '{{.Id}}' "$old_tag")
new_image=$(docker image inspect --format '{{.Id}}' "$new_tag")
mock_image=$(docker image inspect --format '{{.Id}}' "$mock_tag")
case $old_image:$new_image:$mock_image in
	sha256:*:sha256:*:sha256:*) ;;
	*) fail 'a build did not resolve to an immutable image ID' ;;
esac

docker run -d --name "$mock_name" --network host --read-only --cap-drop ALL \
	--security-opt no-new-privileges --env MOCK_API_KEY="$api_key" \
	--env MOCK_LISTEN_ADDRESS="127.0.0.1:$mock_port" "$mock_image" >/dev/null
attempt=0
until docker exec "$mock_name" /runtime-packaging-mock probe "127.0.0.1:$mock_port" >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	[ "$attempt" -lt 20 ] || fail 'mock services did not start'
	sleep 1
done

config_source=$temporary_directory/config-source/config.xml
printf '%s\n' \
	'<configuration version="37"><gui tls="false"><address>127.0.0.1:'"$mock_port"'</address><apikey>'"$api_key"'</apikey></gui></configuration>' \
	>"$config_source"
chown 1000:1000 "$config_source"
chmod 0400 "$config_source"

config_directory=$temporary_directory/runtime-config
state_directory=$temporary_directory/runtime-state
credential_state=$state_directory/credentials/credentials-v1.json

run_installer() {
	requested_image=$1
	command_name=$2
	VAULTSYNC_DIAGNOSTICS_SUPPORTED_HOST_CONFIRMED=1 \
	VAULTSYNC_DIAGNOSTICS_CONTAINER_NAME="$container_name" \
	VAULTSYNC_DIAGNOSTICS_CONFIG_DIR="$config_directory" \
	VAULTSYNC_DIAGNOSTICS_STATE_DIR="$state_directory" \
	VAULTSYNC_DIAGNOSTICS_IMAGE="$requested_image" \
	VAULTSYNC_DIAGNOSTICS_FOLDER_ID="$folder_id" \
	VAULTSYNC_DIAGNOSTICS_LISTEN_ADDRESS="127.0.0.1:$helper_port" \
	VAULTSYNC_DIAGNOSTICS_ADVERTISED_HOST=127.0.0.1 \
	VAULTSYNC_DIAGNOSTICS_ADVERTISED_PORT="$helper_port" \
	SYNCTHING_CONFIG="$config_source" \
	SYNCTHING_API_URL="http://127.0.0.1:$mock_port" \
	RELAY_URL="http://127.0.0.1:$mock_port" \
	"$installer" "$command_name"
}

assert_listener() {
	expected=$1
	attempt=0
	while [ "$attempt" -lt 20 ]; do
		if docker exec "$mock_name" /runtime-packaging-mock probe "127.0.0.1:$helper_port" >/dev/null 2>&1; then
			[ "$expected" = present ] && return 0
		else
			[ "$expected" = absent ] && return 0
		fi
		attempt=$((attempt + 1))
		sleep 1
	done
	fail "listener was not $expected"
}

assert_no_sensitive_logs() {
	for forbidden in "$api_key" "$folder_id" "$config_directory" "$state_directory" "http://127.0.0.1:$mock_port"; do
		if docker logs "$container_name" 2>&1 | grep -F "$forbidden" >/dev/null; then
			fail 'helper log exposed a configured identifier, path, URL, or test credential'
		fi
	done
}

assert_runtime_constraints() {
	expected_image=$1
	[ "$(docker inspect --format '{{.Image}}' "$container_name")" = "$expected_image" ] ||
		fail 'container image ID drifted'
	[ "$(docker inspect --format '{{.Config.User}}' "$container_name")" = 1000:1000 ] ||
		fail 'container user does not match the Syncthing config owner'
	[ "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$container_name")" = true ] ||
		fail 'container root filesystem is writable'
	[ "$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$container_name")" = host ] ||
		fail 'container did not retain the explicit host-network boundary'
	[ "$(docker inspect --format '{{json .HostConfig.CapDrop}}' "$container_name")" = '["ALL"]' ] ||
		fail 'container capabilities were not dropped'
	[ "$(docker inspect --format '{{json .HostConfig.SecurityOpt}}' "$container_name")" = '["no-new-privileges"]' ] ||
		fail 'container no-new-privileges boundary is absent'
	[ "$(docker inspect --format '{{index .HostConfig.Tmpfs "/tmp"}}' "$container_name")" = 'rw,noexec,nosuid,size=64m' ] ||
		fail 'container temporary filesystem boundary changed'
	mounts=$(docker inspect --format '{{range .Mounts}}{{println .Destination .RW}}{{end}}' "$container_name" | sort)
	expected_mounts=$(printf '%s\n' '/config/runtime.json false' '/state true' '/syncthing/config.xml false' | sort)
	[ "$mounts" = "$expected_mounts" ] || fail 'container has missing or unexpected persistent mounts'
}

state_digest() {
	sha256sum "$credential_state" | awk '{print $1}'
}

# Install the old image with the new opt-in config present: it stays dormant.
run_installer "$old_image" init
assert_listener absent
assert_runtime_constraints "$old_image"
[ ! -e "$credential_state" ] || fail 'old helper created diagnostics credentials'
assert_no_sensitive_logs

# Upgrade through the real deploy command.
run_installer "$new_image" deploy
assert_listener present
assert_runtime_constraints "$new_image"
if [ ! -f "$credential_state" ] || [ -L "$credential_state" ]; then
	fail 'new helper state is unavailable'
fi
[ "$(stat -c '%a' "$config_directory")" = 700 ] || fail 'runtime config directory mode is not 0700'
[ "$(stat -c '%a' "$state_directory")" = 700 ] || fail 'runtime state directory mode is not 0700'
[ "$(stat -c '%a' "$config_directory/runtime.json")" = 400 ] || fail 'runtime config mode is not 0400'
[ "$(stat -c '%a' "$state_directory/credentials")" = 700 ] || fail 'credential directory mode is not 0700'
[ "$(stat -c '%a' "$credential_state")" = 600 ] || fail 'credential state mode is not 0600'
first_state_digest=$(state_digest)
first_spki=$(docker exec "$mock_name" /runtime-packaging-mock spki "127.0.0.1:$helper_port")
[ "${#first_spki}" -eq 64 ] || fail 'TLS SPKI digest was not SHA-256 length'
case $first_spki in
	*[!0-9a-f]*|'') fail 'TLS SPKI digest was not canonical SHA-256 hex' ;;
esac
assert_no_sensitive_logs

# Downgrade through the same real deploy command. State remains preserved and
# the old helper remains unable to expose the new listener.
run_installer "$old_image" deploy
assert_listener absent
assert_runtime_constraints "$old_image"
[ "$(state_digest)" = "$first_state_digest" ] || fail 'downgrade changed credential state'
assert_no_sensitive_logs

# Forward recovery through the real deploy command reopens exactly the same
# identity and pin.
run_installer "$new_image" deploy
assert_listener present
assert_runtime_constraints "$new_image"
[ "$(state_digest)" = "$first_state_digest" ] || fail 'forward recovery changed credential state'
second_spki=$(docker exec "$mock_name" /runtime-packaging-mock spki "127.0.0.1:$helper_port")
[ "$second_spki" = "$first_spki" ] || fail 'forward recovery changed the TLS SPKI pin'
assert_no_sensitive_logs

run_installer "$new_image" status
run_installer "$new_image" stop

printf '%s\n' \
	'host=standard-linux-rootful-docker' \
	"old_commit=$old_commit" \
	"old_image=$old_image" \
	"new_commit=$new_commit" \
	"new_image=$new_image" \
	'installer_init_old=pass' \
	'installer_upgrade_new=pass' \
	'installer_downgrade_old=pass' \
	'installer_forward_recovery_new=pass' \
	'credential_state=byte-identical' \
	'upload_evidence=unset' \
	'download_evidence=unset' \
	'roundtrip_evidence=unset'

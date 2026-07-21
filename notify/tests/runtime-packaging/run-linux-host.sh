#!/bin/sh
# Real installer/upgrade/rollback proof for the one supported packaging row:
# rootful Docker Engine on a standard Linux host. Run only on an ephemeral test
# host: it deliberately exercises the root-only operator installer.
set -eu

old_commit=e4f9e3088d7b7bc47943ff59db73de369c16c543
published_old_reference=${VAULTSYNC_RUNTIME_PACKAGING_OLD_IMAGE:-}
published_new_reference=${VAULTSYNC_RUNTIME_PACKAGING_NEW_IMAGE:-}
published_new_commit=${VAULTSYNC_RUNTIME_PACKAGING_NEW_COMMIT:-}
published_new_version=${VAULTSYNC_RUNTIME_PACKAGING_NEW_VERSION:-}
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
old_endpoint_log_observations=0

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
command -v git >/dev/null 2>&1 || fail 'Git is required'
docker info >/dev/null 2>&1 || fail 'rootful Docker Engine is unavailable'
case $(docker info --format '{{json .SecurityOptions}}') in
	*rootless*) fail 'rootless Docker is not a supported proof host' ;;
esac

new_commit=$(git -c "safe.directory=$repository_directory" -C "$repository_directory" rev-parse HEAD)
[ -z "$(git -c "safe.directory=$repository_directory" -C "$repository_directory" status --porcelain -- notify .github/workflows/ci.yml .github/workflows/docker.yml .github/workflows/security.yml .github/scripts/notify-publish-safety.rb)" ] || \
	fail 'Linux-host packaging proof requires a clean committed source tree'
mkdir -p "$temporary_directory/config-source"

publication_mode=source-build
old_reference=source-commit:$old_commit
new_reference=source-commit:$new_commit
new_version=packaging-new-$new_commit
if [ -n "$published_old_reference$published_new_reference$published_new_commit$published_new_version" ]; then
	[ -n "$published_old_reference" ] || fail 'published rollout requires the old immutable image reference'
	[ -n "$published_new_reference" ] || fail 'published rollout requires the new immutable image reference'
	[ -n "$published_new_commit" ] || fail 'published rollout requires the exact new source commit'
	[ -n "$published_new_version" ] || fail 'published rollout requires the exact new version'
	printf '%s\n' "$published_old_reference" | grep -Eq '^ghcr\.io/psimaker/vaultsync-notify@sha256:[0-9a-f]{64}$' ||
		fail 'old published image must be the exact VaultSync GHCR digest reference'
	printf '%s\n' "$published_new_reference" | grep -Eq '^ghcr\.io/psimaker/vaultsync-notify@sha256:[0-9a-f]{64}$' ||
		fail 'new published image must be the exact VaultSync GHCR digest reference'
	[ "$published_new_commit" = "$new_commit" ] || fail 'published image source commit does not match the checked-out release commit'
	printf '%s\n' "$published_new_version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
		fail 'published helper version is not canonical semantic version text'
	docker pull "$published_old_reference" >/dev/null
	docker pull "$published_new_reference" >/dev/null
	old_image=$(docker image inspect --format '{{.Id}}' "$published_old_reference")
	new_image=$(docker image inspect --format '{{.Id}}' "$published_new_reference")
	[ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$published_old_reference")" = "$old_commit" ] ||
		fail 'published old helper source label does not match the rollback commit'
	[ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$published_old_reference")" = 1.8.0 ] ||
		fail 'published old helper version label is not 1.8.0'
	[ "$(docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges "$published_old_reference" --version)" = 'vaultsync-notify 1.8.0' ] ||
		fail 'published old helper binary version is not 1.8.0'
	[ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$published_new_reference")" = "$published_new_commit" ] ||
		fail 'published new helper source label does not match the release commit'
	[ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$published_new_reference")" = "$published_new_version" ] ||
		fail 'published new helper version label does not match the release version'
	[ "$(docker image inspect --format '{{.Config.User}}' "$published_new_reference")" = 65534:65534 ] ||
		fail 'published new helper default user is not the reviewed non-root identity'
	[ "$(docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges "$published_new_reference" --version)" = "vaultsync-notify $published_new_version" ] ||
		fail 'published helper binary version does not match the release version'
	if docker run --rm --network none --entrypoint /bin/sh "$published_new_reference" -c true >/dev/null 2>&1; then
		fail 'published new helper image unexpectedly contains a shell'
	fi
	publication_mode=published-digests
	old_reference=$published_old_reference
	new_reference=$published_new_reference
	new_version=$published_new_version
else
	mkdir -p "$temporary_directory/old"
	git -c "safe.directory=$repository_directory" -C "$repository_directory" \
		cat-file -e "$old_commit^{commit}" || fail 'old helper commit is unavailable'
	git -c "safe.directory=$repository_directory" -C "$repository_directory" \
		archive "$old_commit" notify | tar -x -C "$temporary_directory/old"
	docker build --build-arg "VERSION=packaging-old-$old_commit" -t "$old_tag" "$temporary_directory/old/notify"
	docker build --build-arg "VERSION=packaging-new-$new_commit" -t "$new_tag" "$notify_directory"
	old_image=$(docker image inspect --format '{{.Id}}' "$old_tag")
	new_image=$(docker image inspect --format '{{.Id}}' "$new_tag")
fi
docker build -t "$mock_tag" "$script_directory/mock"

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
	log_phase=$1
	assert_log_omits 'test credential' "$api_key"
	assert_log_omits 'folder identifier' "$folder_id"
	assert_log_omits 'config path' "$config_directory"
	assert_log_omits 'state path' "$state_directory"
	case $log_phase in
		old-*)
			if docker logs "$container_name" 2>&1 | grep -F "http://127.0.0.1:$mock_port" >/dev/null; then
				old_endpoint_log_observations=$((old_endpoint_log_observations + 1))
			fi
			;;
		*) assert_log_omits 'URL' "http://127.0.0.1:$mock_port" ;;
	esac
}

assert_log_omits() {
	label=$1
	forbidden=$2
	if docker logs "$container_name" 2>&1 | grep -F "$forbidden" >/dev/null; then
		fail "helper $log_phase log exposed a configured $label"
	fi
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
	mounts=$(docker inspect --format '{{range .Mounts}}{{if eq .Type "bind"}}{{println .Destination .RW}}{{end}}{{end}}' "$container_name" | sed '/^$/d' | sort)
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
assert_no_sensitive_logs old-initial

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
assert_no_sensitive_logs new-upgrade

# Downgrade through the same real deploy command. State remains preserved and
# the old helper remains unable to expose the new listener.
run_installer "$old_image" deploy
assert_listener absent
assert_runtime_constraints "$old_image"
[ "$(state_digest)" = "$first_state_digest" ] || fail 'downgrade changed credential state'
assert_no_sensitive_logs old-rollback

# Forward recovery through the real deploy command reopens exactly the same
# identity and pin.
run_installer "$new_image" deploy
assert_listener present
assert_runtime_constraints "$new_image"
[ "$(state_digest)" = "$first_state_digest" ] || fail 'forward recovery changed credential state'
second_spki=$(docker exec "$mock_name" /runtime-packaging-mock spki "127.0.0.1:$helper_port")
[ "$second_spki" = "$first_spki" ] || fail 'forward recovery changed the TLS SPKI pin'
assert_no_sensitive_logs new-forward-recovery
[ "$old_endpoint_log_observations" -eq 2 ] ||
	fail 'published rollback baseline endpoint-log behavior was not observed in both old-helper phases'

run_installer "$new_image" status
run_installer "$new_image" stop

printf '%s\n' \
	'host=standard-linux-rootful-docker' \
	"publication_mode=$publication_mode" \
	"old_commit=$old_commit" \
	"old_reference=$old_reference" \
	"old_image=$old_image" \
	"new_commit=$new_commit" \
	"new_reference=$new_reference" \
	"new_version=$new_version" \
	"new_image=$new_image" \
	'installer_init_old=pass' \
	'installer_upgrade_new=pass' \
	'installer_downgrade_old=pass' \
	'installer_forward_recovery_new=pass' \
	'credential_state=byte-identical' \
	'rollback_baseline_endpoint_logging=observed-known-1.8.0' \
	'candidate_sensitive_logging=not-observed' \
	'upload_evidence=unset' \
	'download_evidence=unset' \
	'roundtrip_evidence=unset'

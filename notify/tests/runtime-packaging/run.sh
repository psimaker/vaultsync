#!/bin/sh
# Linux-container packaging proof. This does not claim support for Docker
# Desktop, rootless Docker, NAS packaging, or a production host installation.
set -eu

old_commit=85e527db180a17d9d23f9ba233cf5abd0e9671f6
script_directory=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
notify_directory=$(CDPATH='' cd -- "$script_directory/../.." && pwd)
repository_directory=$(CDPATH='' cd -- "$notify_directory/.." && pwd)
run_id=$$
old_tag=vaultsync-runtime-packaging-old:$run_id
new_tag=vaultsync-runtime-packaging-new:$run_id
mock_tag=vaultsync-runtime-packaging-mock:$run_id
helper_name=vaultsync-runtime-packaging-helper-$run_id
mock_name=vaultsync-runtime-packaging-mock-$run_id
network_name=vaultsync-runtime-packaging-$run_id
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/vaultsync-runtime-packaging.XXXXXX")
api_key=runtime-packaging-api-key-sentinel
folder_id=runtime-packaging-proof
port=8443

cleanup() {
	docker rm -f "$helper_name" "$mock_name" >/dev/null 2>&1 || true
	docker network rm "$network_name" >/dev/null 2>&1 || true
	docker image rm "$old_tag" "$new_tag" "$mock_tag" >/dev/null 2>&1 || true
	rm -rf "$temporary_directory"
}
trap cleanup EXIT INT TERM

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

command -v docker >/dev/null 2>&1 || fail 'Docker is required'
command -v git >/dev/null 2>&1 || fail 'Git is required'
git -C "$repository_directory" cat-file -e "$old_commit^{commit}" || fail 'old helper commit is unavailable'

subnet_octet=201
while [ "$subnet_octet" -le 209 ]; do
	if docker network create --subnet "172.28.$subnet_octet.0/24" "$network_name" >/dev/null 2>&1; then
		break
	fi
	subnet_octet=$((subnet_octet + 1))
done
[ "$subnet_octet" -le 209 ] || fail 'no isolated proof subnet was available'
mock_address=172.28.$subnet_octet.2
helper_address=172.28.$subnet_octet.3

mkdir -p "$temporary_directory/old" "$temporary_directory/config" "$temporary_directory/state"
git -C "$repository_directory" archive "$old_commit" notify | tar -x -C "$temporary_directory/old"

docker build --build-arg "VERSION=packaging-old-$old_commit" -t "$old_tag" "$temporary_directory/old/notify"
new_commit=$(git -C "$repository_directory" rev-parse HEAD)
new_source_state=clean-commit
if [ -n "$(git -C "$repository_directory" status --porcelain -- notify)" ]; then
	new_source_state=modified-worktree
fi
docker build --build-arg "VERSION=packaging-new-$new_commit-$new_source_state" -t "$new_tag" "$notify_directory"
docker build -t "$mock_tag" "$script_directory/mock"

old_image=$(docker image inspect --format '{{.Id}}' "$old_tag")
new_image=$(docker image inspect --format '{{.Id}}' "$new_tag")
mock_image=$(docker image inspect --format '{{.Id}}' "$mock_tag")
case $old_image:$new_image:$mock_image in
	sha256:*:sha256:*:sha256:*) ;;
	*) fail 'a build did not resolve to an immutable image ID' ;;
esac

docker run -d --name "$mock_name" --network "$network_name" --ip "$mock_address" \
	--read-only --cap-drop ALL --security-opt no-new-privileges \
	--env MOCK_API_KEY="$api_key" "$mock_image" >/dev/null

attempt=0
until docker exec "$mock_name" /runtime-packaging-mock probe 127.0.0.1:8080 >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	[ "$attempt" -lt 30 ] || fail 'mock services did not start'
	sleep 1
done

runtime_config=$temporary_directory/config/runtime.json
syncthing_config=$temporary_directory/config/config.xml
credential_state=$temporary_directory/state/credentials/credentials-v1.json
printf '%s\n' \
	'{"format_version":1,"listen_address":"'"$helper_address:$port"'","advertised_host":"helper.packaging.test","advertised_port":'"$port"',"folders":[{"folder_id":"'"$folder_id"'","mount_alias":""}]}' \
	>"$runtime_config"
printf '%s\n' '<configuration version="37"></configuration>' >"$syncthing_config"
chmod 0400 "$runtime_config" "$syncthing_config"
chmod 0700 "$temporary_directory/state"

start_helper() {
	requested_image=$1
	docker run -d --name "$helper_name" --network "$network_name" --ip "$helper_address" \
		--read-only --cap-drop ALL --security-opt no-new-privileges \
		--tmpfs /tmp:rw,noexec,nosuid,size=64m --user "$(id -u):$(id -g)" \
		--mount "type=bind,src=$runtime_config,dst=/config/runtime.json,readonly" \
		--mount "type=bind,src=$temporary_directory/state,dst=/state" \
		--mount "type=bind,src=$syncthing_config,dst=/syncthing/config.xml,readonly" \
		--env VAULTSYNC_DIAGNOSTICS_CONFIG=/config/runtime.json \
		--env VAULTSYNC_DIAGNOSTICS_STATE=/state \
		--env SYNCTHING_CONFIG=/syncthing/config.xml \
		--env SYNCTHING_API_URL="http://$mock_address:8080" \
		--env SYNCTHING_API_KEY="$api_key" \
		--env RELAY_URL="http://$mock_address:8080" \
		--env STARTUP_ANNOUNCE=false --env STALE_RETRIGGER_SECONDS=0 \
		"$requested_image" >/dev/null
	attempt=0
	until [ "$(docker inspect --format '{{.State.Running}}' "$helper_name" 2>/dev/null || true)" = true ]; do
		attempt=$((attempt + 1))
		[ "$attempt" -lt 20 ] || fail 'helper did not remain running'
		sleep 1
	done
	[ "$(docker inspect --format '{{.Image}}' "$helper_name")" = "$requested_image" ] || fail 'container image ID drifted'
	[ "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$helper_name")" = true ] || fail 'root filesystem is writable'
	[ "$(docker inspect --format '{{json .HostConfig.CapDrop}}' "$helper_name")" = '["ALL"]' ] || fail 'capabilities were not dropped'
	[ "$(docker inspect --format '{{json .HostConfig.SecurityOpt}}' "$helper_name")" = '["no-new-privileges"]' ] || fail 'no-new-privileges is absent'
	mounts=$(docker inspect --format '{{range .Mounts}}{{println .Destination .RW}}{{end}}' "$helper_name")
	printf '%s\n' "$mounts" | grep -qx '/config/runtime.json false' || fail 'runtime config is not read-only'
	printf '%s\n' "$mounts" | grep -qx '/syncthing/config.xml false' || fail 'Syncthing config is not read-only'
	printf '%s\n' "$mounts" | grep -qx '/state true' || fail 'state is not the separate writable mount'
}

assert_listener() {
	expected=$1
	attempt=0
	while [ "$attempt" -lt 20 ]; do
		if docker exec "$mock_name" /runtime-packaging-mock probe "$helper_address:$port" >/dev/null 2>&1; then
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
	for forbidden in "$api_key" "$folder_id" "$runtime_config" "$temporary_directory/state" "http://$mock_address:8080"; do
		if docker logs "$helper_name" 2>&1 | grep -F "$forbidden" >/dev/null; then
			fail 'helper log exposed a configured identifier, path, URL, or test credential'
		fi
	done
}

stop_helper() {
	assert_no_sensitive_logs
	docker rm -f "$helper_name" >/dev/null
}

state_digest() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$credential_state" | awk '{print $1}'
	else
		shasum -a 256 "$credential_state" | awk '{print $1}'
	fi
}

# The old helper remains dormant even when the new opt-in variables are set.
start_helper "$old_image"
assert_listener absent
[ ! -e "$credential_state" ] || fail 'old helper created diagnostics credentials'
stop_helper

# Upgrade: the new immutable image starts the TLS 1.3 listener and persists its
# private identity only in the separate state mount.
start_helper "$new_image"
assert_listener present
[ -f "$credential_state" ] || fail 'new helper did not persist credential state'
[ ! -L "$credential_state" ] || fail 'credential state is a symlink'
first_state_digest=$(state_digest)
first_spki=$(docker exec "$mock_name" /runtime-packaging-mock spki "$helper_address:$port")
[ "${#first_spki}" -eq 64 ] || fail 'TLS SPKI digest was not SHA-256 length'
case $first_spki in
	*[!0-9a-f]*|'') fail 'TLS SPKI digest was not canonical SHA-256 hex' ;;
	*) ;;
esac
stop_helper

# Rollback: the old image neither reads nor rewrites the persisted opt-in
# credential state, and it exposes no diagnostics listener.
start_helper "$old_image"
assert_listener absent
[ "$(state_digest)" = "$first_state_digest" ] || fail 'rollback changed diagnostics credential state'
stop_helper

# Forward recovery: the exact same state reopens, preserving the public pin.
start_helper "$new_image"
assert_listener present
[ "$(state_digest)" = "$first_state_digest" ] || fail 'forward recovery changed diagnostics credential state'
second_spki=$(docker exec "$mock_name" /runtime-packaging-mock spki "$helper_address:$port")
[ "$second_spki" = "$first_spki" ] || fail 'forward recovery changed the TLS SPKI pin'
stop_helper

printf '%s\n' \
	"old_commit=$old_commit" \
	"old_image=$old_image" \
	"new_source_head=$new_commit" \
	"new_source_state=$new_source_state" \
	"new_image=$new_image" \
	'old_to_new=pass' \
	'new_to_old=pass' \
	'old_to_new_forward_recovery=pass' \
	'credential_state=byte-identical' \
	'upload_evidence=unset' \
	'download_evidence=unset' \
	'roundtrip_evidence=unset'

#!/bin/sh
# Explicit diagnostics runtime installer for the single supported deployment:
# rootful Docker Engine on a standard Linux host with exact host bind mounts.
# It never edits Syncthing configuration, ignore rules, trust, shares, or paths.
set -eu

command_name=${1:-}
[ -n "$command_name" ] || {
	printf '%s\n' 'usage: diagnostics-docker.sh init|pair|enable|deploy|list|rotate-helper|rotate-tls|revoke|status|stop' >&2
	exit 2
}
shift
[ "$#" -eq 0 ] || {
	printf '%s\n' 'ERROR: this command accepts configuration only through the documented environment variables' >&2
	exit 2
}

container_name=${VAULTSYNC_DIAGNOSTICS_CONTAINER_NAME:-vaultsync-notify}
config_directory=${VAULTSYNC_DIAGNOSTICS_CONFIG_DIR:-/etc/vaultsync-notify/diagnostics}
state_directory=${VAULTSYNC_DIAGNOSTICS_STATE_DIR:-/var/lib/vaultsync-notify/diagnostics}
runtime_config=$config_directory/runtime.json
image_reference=${VAULTSYNC_DIAGNOSTICS_IMAGE:-}
folder_id=${VAULTSYNC_DIAGNOSTICS_FOLDER_ID:-}
folder_path=${VAULTSYNC_DIAGNOSTICS_FOLDER_PATH:-}
listen_address=${VAULTSYNC_DIAGNOSTICS_LISTEN_ADDRESS:-}
advertised_host=${VAULTSYNC_DIAGNOSTICS_ADVERTISED_HOST:-}
advertised_port=${VAULTSYNC_DIAGNOSTICS_ADVERTISED_PORT:-}
syncthing_config=${SYNCTHING_CONFIG:-}
syncthing_api_url=${SYNCTHING_API_URL:-http://127.0.0.1:8384}
relay_url=${RELAY_URL:-}
app_fingerprint=${VAULTSYNC_DIAGNOSTICS_APP_FINGERPRINT:-}
revocation_reason=${VAULTSYNC_DIAGNOSTICS_REVOCATION_REASON:-}

fail() {
	printf 'ERROR: %s\n' "$*" >&2
	exit 1
}

require_value() {
	variable_name=$1
	eval "variable_value=\${$variable_name:-}"
	[ -n "$variable_value" ] || fail "$variable_name is required"
}

require_docker_mount_path() {
	mount_path_value=$1
	case $mount_path_value in
		*,*) fail 'Docker bind source paths containing commas are unsupported' ;;
	esac
	without_line_breaks=$(printf '%s' "$mount_path_value" | tr -d '\r\n')
	[ "$without_line_breaks" = "$mount_path_value" ] || fail 'Docker bind source paths containing line breaks are unsupported'
}

require_supported_host() {
	case $container_name in
		''|[!A-Za-z0-9]*|*[!A-Za-z0-9_.-]*) fail 'container name must start with an ASCII alphanumeric and contain only Docker name characters' ;;
	esac
	[ "${#container_name}" -le 128 ] || fail 'container name is too long'
	[ "$(uname -s)" = Linux ] || fail 'diagnostics packaging is unsupported on macOS and Windows'
	if grep -Eiq 'microsoft|wsl' /proc/sys/kernel/osrelease 2>/dev/null; then
		fail 'diagnostics packaging is unsupported on Windows/WSL'
	fi
	[ "$(id -u)" -eq 0 ] || fail 'run this explicit installer as root'
	command -v docker >/dev/null 2>&1 || fail 'Docker Engine is required'
	command -v realpath >/dev/null 2>&1 || fail 'realpath is required for exact host-bind identity'
	command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required for the ephemeral mount binding'
	case ${DOCKER_HOST:-} in
		''|unix://*) ;;
		*) fail 'remote Docker daemons and non-Unix Docker endpoints remain unsupported' ;;
	esac
	docker_context=$(docker context show 2>/dev/null) || fail 'the active Docker context is unavailable'
	docker_endpoint=$(docker context inspect --format '{{(index .Endpoints "docker").Host}}' "$docker_context" 2>/dev/null) ||
		fail 'the active Docker endpoint is unavailable'
	case $docker_endpoint in
		unix://*) ;;
		*) fail 'remote Docker contexts and non-Unix Docker endpoints remain unsupported' ;;
	esac
	docker info >/dev/null 2>&1 || fail 'the rootful Docker daemon is unavailable'
	case $(docker info --format '{{json .SecurityOptions}}') in
		*rootless*) fail 'rootless Docker remains unsupported' ;;
	esac
	case $(docker info --format '{{.OperatingSystem}}' | tr '[:upper:]' '[:lower:]') in
		*docker*desktop*) fail 'Docker Desktop remains unsupported' ;;
	esac
	[ "${VAULTSYNC_DIAGNOSTICS_SUPPORTED_HOST_CONFIRMED:-}" = 1 ] ||
		fail 'set VAULTSYNC_DIAGNOSTICS_SUPPORTED_HOST_CONFIRMED=1 only after confirming a standard Linux host (not NAS)'
}

require_supported_storage() {
	storage_path=$1
	storage_label=$2
	filesystem_type=$(stat -f -c '%T' "$storage_path") || fail "$storage_label filesystem type is unavailable"
	case $filesystem_type in
		nfs|nfs4|cifs|smb2|smb3|9p|fuse*|ceph|glusterfs|lustre|afs|vboxsf|prl_fs|drvfs|wslfs)
			fail 'remote, NAS, FUSE, and desktop-virtualized filesystems remain unsupported'
			;;
	esac
	docker_root=$(docker info --format '{{.DockerRootDir}}') || fail 'Docker root directory is unavailable'
	canonical_docker_root=$(realpath "$docker_root") || fail 'Docker root directory cannot be resolved'
	case "$storage_path/" in
		"$canonical_docker_root/volumes/"*) fail 'Docker named volumes and volume subpaths remain unsupported' ;;
	esac
}

validate_common_values() {
	require_value VAULTSYNC_DIAGNOSTICS_IMAGE
	require_value VAULTSYNC_DIAGNOSTICS_FOLDER_ID
	require_value VAULTSYNC_DIAGNOSTICS_LISTEN_ADDRESS
	require_value VAULTSYNC_DIAGNOSTICS_ADVERTISED_HOST
	require_value VAULTSYNC_DIAGNOSTICS_ADVERTISED_PORT
	require_value SYNCTHING_CONFIG
	require_value RELAY_URL
	case $folder_id in
		*[!A-Za-z0-9._-]*|'') fail 'folder ID must use the supported ASCII ID subset' ;;
	esac
	case $listen_address in
		*[!0-9.:]*|'') fail 'listen address must be an explicit IPv4 address and port' ;;
	esac
	case $advertised_host in
		*[!a-z0-9.-]*|'') fail 'advertised host must be a lowercase DNS name or canonical IPv4 address' ;;
	esac
	case $advertised_port in
		*[!0-9]*|'') fail 'advertised port must be numeric' ;;
	esac
	if ! [ "$advertised_port" -ge 1024 ] 2>/dev/null || ! [ "$advertised_port" -le 65535 ] 2>/dev/null; then
		fail 'the non-root diagnostics listener port must be between 1024 and 65535'
	fi
	[ "${listen_address##*:}" = "$advertised_port" ] || fail 'listen and advertised ports must match'
	case $syncthing_api_url in
		http://127.0.0.1:*) syncthing_api_port=${syncthing_api_url#http://127.0.0.1:} ;;
		*) fail 'SYNCTHING_API_URL must be an explicit loopback HTTP endpoint on this host' ;;
	esac
	case $syncthing_api_port in
		*[!0-9]*|'') fail 'SYNCTHING_API_URL must contain only one numeric port' ;;
	esac
	if ! [ "$syncthing_api_port" -ge 1 ] 2>/dev/null || ! [ "$syncthing_api_port" -le 65535 ] 2>/dev/null; then
		fail 'SYNCTHING_API_URL port is outside 1 through 65535'
	fi
	[ "${syncthing_config#/}" != "$syncthing_config" ] || fail 'SYNCTHING_CONFIG must be absolute'
	if [ ! -f "$syncthing_config" ] || [ -L "$syncthing_config" ]; then
		fail 'SYNCTHING_CONFIG must be one regular non-symlink file'
	fi
	canonical_syncthing_config=$(realpath "$syncthing_config") || fail 'SYNCTHING_CONFIG cannot be resolved'
	[ "$canonical_syncthing_config" = "$syncthing_config" ] || fail 'SYNCTHING_CONFIG must already be canonical'
	require_docker_mount_path "$canonical_syncthing_config"
	require_supported_storage "$(dirname "$canonical_syncthing_config")" 'Syncthing config'
	case $config_directory:$state_directory in
		/*:/*) ;;
		*) fail 'config and state directories must be absolute' ;;
	esac
	[ "$config_directory" != "$state_directory" ] || fail 'config and state must be separate directories'
}

resolve_image() {
	image_id=$(docker image inspect --format '{{.Id}}' "$image_reference" 2>/dev/null) ||
		fail 'the requested image is not present locally; pull and verify it explicitly first'
	case $image_id in
		sha256:*) ;;
		*) fail 'Docker did not resolve the image to an immutable content ID' ;;
	esac
}

config_alias() {
	[ -r "$runtime_config" ] || return 0
	sed -n 's/.*"mount_alias":"\([^"]*\)".*/\1/p' "$runtime_config"
}

write_runtime_config() {
	alias_value=$1
	temporary=$(mktemp "$config_directory/runtime.json.XXXXXX")
	trap 'rm -f "$temporary"' EXIT INT TERM
	printf '%s\n' \
		'{"format_version":1,"listen_address":"'"$listen_address"'","advertised_host":"'"$advertised_host"'","advertised_port":'"$advertised_port"',"folders":[{"folder_id":"'"$folder_id"'","mount_alias":"'"$alias_value"'"}]}' \
		>"$temporary"
	chown "$runtime_owner" "$temporary"
	chmod 0400 "$temporary"
	mv -f "$temporary" "$runtime_config"
	trap - EXIT INT TERM
}

prepare_directories() {
	runtime_owner=$(stat -c '%u:%g' "$syncthing_config") || fail 'cannot determine Syncthing config owner'
	case $runtime_owner in
		0:*|*:0) fail 'a root-owned Syncthing config is unsupported; the helper runtime requires an exact non-root uid:gid' ;;
	esac
	requested_config_directory=$(realpath -m "$config_directory") || fail 'config directory cannot be resolved safely'
	requested_state_directory=$(realpath -m "$state_directory") || fail 'state directory cannot be resolved safely'
	[ "$requested_config_directory" = "$config_directory" ] || fail 'config directory must already be canonical'
	[ "$requested_state_directory" = "$state_directory" ] || fail 'state directory must already be canonical'
	require_docker_mount_path "$requested_config_directory"
	require_docker_mount_path "$requested_state_directory"
	mkdir -p "$config_directory" "$state_directory"
	canonical_config_directory=$(realpath "$config_directory") || fail 'config directory cannot be resolved'
	canonical_state_directory=$(realpath "$state_directory") || fail 'state directory cannot be resolved'
	[ "$canonical_config_directory" = "$config_directory" ] || fail 'config directory must already be canonical'
	[ "$canonical_state_directory" = "$state_directory" ] || fail 'state directory must already be canonical'
	require_supported_storage "$canonical_config_directory" 'config directory'
	require_supported_storage "$canonical_state_directory" 'state directory'
	case "$canonical_config_directory/" in
		"$canonical_state_directory/"*) fail 'config and state directories must not contain one another' ;;
	esac
	case "$canonical_state_directory/" in
		"$canonical_config_directory/"*) fail 'config and state directories must not contain one another' ;;
	esac
	chown "$runtime_owner" "$config_directory" "$state_directory"
	chmod 0700 "$config_directory" "$state_directory"
}

validate_folder_path() {
	require_value VAULTSYNC_DIAGNOSTICS_FOLDER_PATH
	[ "${folder_path#/}" != "$folder_path" ] || fail 'folder path must be absolute'
	if [ ! -d "$folder_path" ] || [ -L "$folder_path" ]; then
		fail 'folder path must be one existing non-symlink directory'
	fi
	canonical_path=$(realpath "$folder_path") || fail 'folder path cannot be resolved'
	[ "$canonical_path" = "$folder_path" ] || fail 'folder path must already be canonical'
	require_docker_mount_path "$canonical_path"
	if [ ! -d "$folder_path/.stfolder" ] || [ -L "$folder_path/.stfolder" ]; then
		fail 'the exact .stfolder marker is required'
	fi
	case $folder_path in
		/volume*|/share/*|/mnt/user/*|/mnt/pool*|/srv/dev-disk-by-*) fail 'NAS paths remain unsupported' ;;
	esac
	require_supported_storage "$canonical_path" 'folder'
	source_device=$(stat -Lc '%d' "$folder_path") || fail 'folder device identity is unavailable'
	source_inode=$(stat -Lc '%i' "$folder_path") || fail 'folder inode identity is unavailable'
	case $source_device:$source_inode in
		*[!0-9:]*|0:*|*:0) fail 'folder identity is invalid' ;;
	esac
}

run_container() {
	alias_value=$(config_alias)
	set -- docker run -d --name "$container_name" --restart unless-stopped \
		--network host --read-only --cap-drop ALL --security-opt no-new-privileges \
		--tmpfs /tmp:rw,noexec,nosuid,size=64m --user "$runtime_owner" \
		--mount "type=bind,src=$runtime_config,dst=/config/runtime.json,readonly" \
		--mount "type=bind,src=$state_directory,dst=/state" \
		--mount "type=bind,src=$syncthing_config,dst=/syncthing/config.xml,readonly" \
		--env VAULTSYNC_DIAGNOSTICS_CONFIG=/config/runtime.json \
		--env VAULTSYNC_DIAGNOSTICS_STATE=/state \
		--env SYNCTHING_CONFIG=/syncthing/config.xml \
		--env SYNCTHING_API_URL="$syncthing_api_url" \
		--env RELAY_URL="$relay_url"
	if [ -n "$alias_value" ]; then
		case $alias_value in
			namespace-[1-8]) ;;
			*) fail 'runtime config contains an invalid mount alias' ;;
		esac
		validate_folder_path
		namespace_path=$folder_path/VaultSync\ Diagnostics
		if [ ! -d "$namespace_path" ] || [ -L "$namespace_path" ]; then
			fail 'the authenticated namespace host bind is unavailable'
		fi
		namespace_device=$(stat -Lc '%d' "$namespace_path") || fail 'namespace device identity is unavailable'
		namespace_inode=$(stat -Lc '%i' "$namespace_path") || fail 'namespace inode identity is unavailable'
		mount_binding=$(
			{
				printf 'eu.vaultsync.runtime/v1/mount-binding\000'
				printf '%s\000' "$folder_id" "$folder_path" "$alias_value" "$namespace_device" "$namespace_inode"
			} | sha256sum | awk '{print $1}'
		)
		case $mount_binding in
			*[!0-9a-f]*|'') fail 'ephemeral mount binding could not be computed' ;;
		esac
		[ "${#mount_binding}" -eq 64 ] || fail 'ephemeral mount binding has invalid length'
		mount_slot=${alias_value#namespace-}
		set -- "$@" --env "VAULTSYNC_DIAGNOSTICS_MOUNT_BINDING_$mount_slot=$mount_binding"
		set -- "$@" --mount "type=bind,src=$namespace_path,dst=/diagnostics/$alias_value"
	fi
	set -- "$@" "$image_id"
	"$@" >/dev/null
	sleep 3
	[ "$(docker inspect --format '{{.State.Running}}' "$container_name" 2>/dev/null)" = true ] ||
		fail 'the helper did not remain running; inspect its fixed-category logs'
}

deploy() {
	validate_common_values
	prepare_directories
	[ -r "$runtime_config" ] || fail 'run init before deploy'
	resolve_image
	if docker container inspect "$container_name" >/dev/null 2>&1; then
		docker rm -f "$container_name" >/dev/null
	fi
	run_container
	printf 'deployed immutable image %s\n' "$image_id"
}

case $command_name in
	init)
		require_supported_host
		validate_common_values
		prepare_directories
		[ ! -e "$runtime_config" ] || fail 'runtime config already exists; use deploy or enable'
		write_runtime_config ''
		deploy
		;;
	pair)
		require_supported_host
		validate_common_values
		[ -r "$runtime_config" ] || fail 'run init first'
		docker exec "$container_name" vaultsync-notify --diagnostics-pair-folder "$folder_id"
		;;
	list)
		require_supported_host
		validate_common_values
		docker exec "$container_name" vaultsync-notify --diagnostics-admin-action list --diagnostics-admin-folder "$folder_id"
		;;
	rotate-helper | rotate-tls)
		require_supported_host
		validate_common_values
		require_value VAULTSYNC_DIAGNOSTICS_APP_FINGERPRINT
		docker exec "$container_name" vaultsync-notify --diagnostics-admin-action "$command_name" \
			--diagnostics-admin-folder "$folder_id" --diagnostics-admin-app "$app_fingerprint"
		;;
	revoke)
		require_supported_host
		validate_common_values
		require_value VAULTSYNC_DIAGNOSTICS_APP_FINGERPRINT
		require_value VAULTSYNC_DIAGNOSTICS_REVOCATION_REASON
		docker exec "$container_name" vaultsync-notify --diagnostics-admin-action revoke \
			--diagnostics-admin-folder "$folder_id" --diagnostics-admin-app "$app_fingerprint" \
			--diagnostics-revocation-reason "$revocation_reason"
		;;
	enable)
		require_supported_host
		validate_common_values
		validate_folder_path
		printf 'exact namespace: %s\n' "$folder_path/VaultSync Diagnostics" >&2
		[ "${VAULTSYNC_DIAGNOSTICS_ENABLE_CONFIRMED:-}" = 1 ] ||
			fail 'set VAULTSYNC_DIAGNOSTICS_ENABLE_CONFIRMED=1 only after confirming this exact path and accepting opaque retention in peers, backups, versions, conflicts, and tombstones'
		prepare_directories
		[ "$(config_alias)" = '' ] || fail 'the folder already has an explicit mount alias'
		resolve_image
		alias_value=$(docker run --rm --network host --read-only --cap-drop ALL --security-opt no-new-privileges \
			--tmpfs /tmp:rw,noexec,nosuid,size=64m --user "$runtime_owner" \
			--mount "type=bind,src=$runtime_config,dst=/config/runtime.json,readonly" \
			--mount "type=bind,src=$state_directory,dst=/state" \
			--mount "type=bind,src=$syncthing_config,dst=/syncthing/config.xml,readonly" \
			--mount "type=bind,src=$folder_path,dst=/installer-parent" \
			--env VAULTSYNC_DIAGNOSTICS_CONFIG=/config/runtime.json \
			--env VAULTSYNC_DIAGNOSTICS_STATE=/state \
			--env SYNCTHING_CONFIG=/syncthing/config.xml \
			--env SYNCTHING_API_URL="$syncthing_api_url" \
			--env RELAY_URL="$relay_url" \
			"$image_id" --diagnostics-prepare-folder "$folder_id" \
			--diagnostics-source-path "$folder_path" --diagnostics-mounted-parent /installer-parent \
			--diagnostics-source-device "$source_device" --diagnostics-source-inode "$source_inode" \
			--diagnostics-operator-confirmed) || fail 'explicit namespace preparation failed without changing Syncthing configuration'
		case $alias_value in
			namespace-[1-8]) ;;
			*) fail 'installer returned an invalid mount alias' ;;
		esac
		write_runtime_config "$alias_value"
		deploy
		;;
	deploy)
		require_supported_host
		deploy
		;;
	status)
		require_supported_host
		docker inspect --format 'image={{.Image}} readonly={{.HostConfig.ReadonlyRootfs}} network={{.HostConfig.NetworkMode}}' "$container_name"
		docker exec "$container_name" vaultsync-notify --version
		;;
	stop)
		require_supported_host
		if docker container inspect "$container_name" >/dev/null 2>&1; then
			docker rm -f "$container_name" >/dev/null
		fi
		printf '%s\n' 'helper stopped; credentials, namespace, mappings, backups, versions, and tombstones were not deleted'
		;;
	*)
		fail 'unknown command (expected init, pair, enable, deploy, lifecycle admin, status, or stop)'
		;;
esac

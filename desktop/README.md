# vaultsync — the VaultSync desktop agent

One program that turns a Mac or a Linux computer into a VaultSync device: it
installs its own pinned Syncthing, pairs with a VaultSync Hub by code and
keeps Obsidian vaults in sync as a background service.

User documentation: [`docs/hub.md`](../docs/hub.md#desktop-agent-macos-and-linux).
Design record: [046](../docs/decisions/046-desktop-agent-pinned-syncthing-child.md).

## What is in here

| Path | Purpose |
|---|---|
| `main.go` | CLI: `setup`, `pair`, `status`, `stop`/`start`, `uninstall`, `run` (the service) |
| `pins.go` | the one Syncthing release the agent installs, with each archive's SHA-256 |
| `install.go` | download → checksum against the pin → unpack the one binary → one rename |
| `engine.go` | the engine's private home, its first-start config, the supervisor (`run`) |
| `pair.go` | pairing (one Hub, code, catalogue), the vault menu, every check, the final gate |
| `obsidian.go` | Obsidian's registry (`obsidian.json`: macOS, Linux incl. Flatpak/Snap, Windows) and the fallback scan |
| `cloud.go` | the cloud-folder block (iCloud Drive, OneDrive, Dropbox, Google Drive, Nextcloud) |
| `overlap.go` | overlap on disk (symlinks, case, file identity) and the read-only look at the user's own Syncthing |
| `service.go` | LaunchAgent / systemd user unit, install, stop/start, uninstall |
| `status.go` | `status`: service, engine, Hub connection, each vault's state on both sides |
| `attempts.go` | the pairing journal (`pairing.json`): recorded before the Hub is asked |
| `uninstall.go`, `mount_*.go` | `uninstall`; `--remove-data` anchored to VaultSync's own folder, stopping at links and mounts |

The pairing protocol, the Syncthing REST client and the device-side accept
guard come from the Hub's module (`hub/pairing`, `hub/syncthing`, `hub/join`)
through `replace github.com/psimaker/vaultsync/hub => ../hub`.

## Run the tests

```sh
cd desktop
go test ./... -count=1 -race
go vet ./...

# End to end: a fresh home, the real pinned archive, the real vaultsync-hub,
# two Syncthing instances on loopback (~15 s).
v=$(sed -n 's/^const syncthingVersion = "\(v[0-9.]*\)"$/\1/p' pins.go)
curl -fsSLO "https://github.com/syncthing/syncthing/releases/download/$v/syncthing-linux-amd64-$v.tar.gz"
VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE=$PWD/syncthing-linux-amd64-$v.tar.gz \
  go test . -run '^TestIssue175_(E2E|RealArchive)' -count=1 -v
```

On a Mac, use `syncthing-macos-arm64-$v.zip` (or `-amd64`).

## Release

Tag `desktop-vX.Y.Z` on `main`. `desktop-release.yml` publishes a GitHub
release with `vaultsync_<os>_<arch>` binaries and `SHA256SUMS` (not marked
Latest — that stays the iOS app's release).

## Bumping Syncthing

Change `syncthingVersion` and the four entries in `pins.go`. First verify
the release's `sha256sum.txt.asc` with Syncthing's release key
(`curl -fsSL https://syncthing.net/release-key.txt | gpg --import`, then
`gpg --verify sha256sum.txt.asc`). v2.1.6 is signed by
`FBA2 E162 F2F4 4657 B38F 0309 E566 5F9B D597 0C47`, a key that the older
release key `37C8 4554 E7E0 A261 E4F7 6E1E D26E 6ED0 0065 4A3E` certifies;
a signature by any other key stops the bump until that key is certified the
same way. Take each checksum from the verified file and compare it with
GitHub's asset digest
(`gh api repos/syncthing/syncthing/releases/tags/vX.Y.Z --jq '.assets[] | [.name, .digest]'`).
The Desktop Syncthing E2E must pass on the new archive: the engine's first
config is edited in the shape the pinned version generates.

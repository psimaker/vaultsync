# vaultsync — the VaultSync desktop agent

One program that turns a Mac or a Linux computer into a VaultSync device: it
installs its own pinned Syncthing, pairs with a VaultSync Hub by code and
keeps Obsidian vaults in sync as a background service. A second program,
`vaultsync-tray`, is its menu-bar icon.

User documentation: [`docs/hub.md`](../docs/hub.md#desktop-agent-macos-and-linux).
Design records: [046](../docs/decisions/046-desktop-agent-pinned-syncthing-child.md)
(the agent), [048](../docs/decisions/048-tray-separate-process-fyne-systray.md)
and [050](../docs/decisions/050-tray-asks-through-the-desktops-dialogs.md)
(the tray), [049](../docs/decisions/049-desktop-control-socket.md) (the
socket between them).

## What is in here

| Path | Purpose |
|---|---|
| `main.go` | CLI: `setup`, `pair`, `status`, `pause`/`resume`, `stop`/`start`, `uninstall`, `run` (the service) |
| `pins.go` | the one Syncthing release the agent installs, with each archive's SHA-256 |
| `install.go` | download → checksum against the pin → unpack the one binary → one rename |
| `engine.go` | the engine's private home, its first-start config, the supervisor (`run`) |
| `pair.go` | pairing (one Hub, code, catalogue), the vault menu, every check, the final gate |
| `obsidian.go` | Obsidian's registry (`obsidian.json`: macOS, Linux incl. Flatpak/Snap, Windows) and the fallback scan |
| `cloud.go` | the cloud-folder block (iCloud Drive, OneDrive, Dropbox, Google Drive, Nextcloud) |
| `overlap.go` | overlap on disk (symlinks, case, file identity) and the read-only look at the user's own Syncthing |
| `service.go` | LaunchAgent / systemd user unit, install, stop/start, uninstall |
| `status.go` | `status`: service, engine, Hub connection, each vault's state on both sides — as text and as the report the socket serves |
| `control.go` | the control socket (`agent.sock`, decision 049): the running agent answers status, pause, resume and pair for the terminal and the menu-bar app |
| `attempts.go` | the pairing journal (`pairing.json`): recorded before the Hub is asked |
| `uninstall.go`, `mount_*.go` | `uninstall`; `--remove-data` anchored to VaultSync's own folder, stopping at links and mounts |
| `internal/peer/` | whose a unix socket connection is — the one check both ends of the control socket make |
| `tray/` | the menu-bar app without the menu bar: the socket client, what the icon and the menu show (`view.go`), the loop (`controller.go`), the pairing conversation (`pairflow.go`) and the dialogs it uses (`ask.go`: osascript, zenity) — pure Go, tested on Linux |
| `cmd/vaultsync-tray/` | the `vaultsync-tray` binary: `fyne.io/systray` glue (Cocoa through cgo on macOS, StatusNotifierItem over DBus on Linux) and the GNOME hint |

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
release with `vaultsync_<os>_<arch>` and `vaultsync-tray_<os>_<arch>`
binaries and one `SHA256SUMS` (not marked Latest — that stays the iOS app's
release). The agent is cross-compiled on Linux with `CGO_ENABLED=0` as
before; the tray's Linux binaries too, while its macOS binaries are built on
a macOS runner (cgo). Locally:

```sh
cd desktop
go build ./cmd/vaultsync-tray && ./vaultsync-tray            # this computer
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/vaultsync-tray
```

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

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
socket between them), [051](../docs/decisions/051-locks-in-vaultsyncs-folder.md)
(the locks), [052](../docs/decisions/052-macos-signing-and-app-bundle.md)
(signing, notarization and the app bundle for macOS) and
[053](../docs/decisions/053-service-runs-from-vaultsync-for-mac.md) (the
service and the menu-bar app's start at login from VaultSync for Mac).

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
| `service.go` | LaunchAgent / systemd user unit, install, stop/start, uninstall; on a Mac the menu-bar app's start at login (`eu.vaultsync.tray`) |
| `appbundle.go`, `appbundle_*.go` | whether this program runs from VaultSync for Mac installed in Applications, this account's — then the service runs from the app and ends when the app goes to the Trash (decision 053) |
| `status.go` | `status`: service, engine, Hub connection, each vault's state on both sides — as text and as the report the socket serves |
| `control.go` | the control socket (`agent.sock`, decision 049): the running agent answers status, pause, resume and pair for the terminal and the menu-bar app |
| `attempts.go` | the pairing journal (`pairing.json`): recorded before the Hub is asked |
| `uninstall.go`, `mount_*.go` | `uninstall`; `--remove-data` anchored to VaultSync's own folder, stopping at links and mounts |
| `internal/peer/` | whose a unix socket connection is — the one check both ends of the control socket make |
| `internal/control/` | the control socket's client and its answers' types (`wire.go`), used by the terminal's commands and the tray alike |
| `internal/layout/` | where VaultSync keeps its files on this computer, the service's log and the `~` shortening — the agent's `layout` is an alias of it |
| `internal/lock/` | the one flock behind the agent's `lockFile`/`lockFileWait` and the tray's one-instance lock |
| `tray/` | the menu-bar app without the menu bar: the socket client, what the icon and the menu show (`view.go`), the loop (`controller.go`), the pairing conversation (`pairflow.go`) and the dialogs it uses (`ask.go`: osascript, zenity, kdialog) — pure Go, tested on Linux |
| `cmd/vaultsync-tray/` | the `vaultsync-tray` binary: `fyne.io/systray` glue (Cocoa through cgo on macOS, StatusNotifierItem over DBus on Linux) and the GNOME hint |
| `macos/` | VaultSync for Mac (decision 052): `Info.plist` and `make-app.sh` (the tray as the bundle's executable, the agent beside it), `sign.sh` (Developer ID and notarization on a tag, ad hoc on a pull request), `verify.sh` (Apple's checks of the result), `install-rcodesign.sh` (the pinned signer), `tests/` (the release path against a stand-in, the team checks against a signed fixture) |
| `scripts/release-assets.sh` | a release's assets and `SHA256SUMS`, run by the release job and by every pull request |

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

What a person meets of this agent goes into [CHANGELOG.md](CHANGELOG.md), one
`## [X.Y.Z] — date` section per release. The repository's root changelog is
the iOS app's, the Hub's and the helper's; a change that spans every component
goes there, with one line here.

Tag `desktop-vX.Y.Z` on `main`. `desktop-release.yml` publishes a GitHub
release (not marked Latest — that stays the iOS app's release) with

- `vaultsync_<os>_<arch>` — the agent, the asset `setup.sh` downloads,
- `VaultSync_darwin_<arch>.zip` — `VaultSync for Mac.app`: the menu-bar app
  with the agent inside (not `VaultSync.app`, the iOS app's name on Apple
  silicon Macs); `setup.sh` installs it on a Mac, and the service and the
  menu-bar app run from it (decision 053),
- `vaultsync-tray_linux_<arch>` — the tray for Linux,
- one `SHA256SUMS` over all of them.

The agent is cross-compiled on Linux with `CGO_ENABLED=0`, the tray's Linux
binaries too; the tray for macOS is built on a macOS runner (cgo, minimum
macOS pinned to `LSMinimumSystemVersion`), which also puts the app
together (`macos/make-app.sh`). Everything for macOS is signed with the
owner's Developer ID (hardened runtime, no entitlements, the agent as
`eu.vaultsync.agent` alone and in the bundle) by `macos/sign.sh` with
`rcodesign` on a Linux runner (pinned in `macos/install-rcodesign.sh`; bump
version and SHA-256 together); Apple notarizes the bundles, whose ticket is
stapled and also covers the bare agent (the same code directory). Only a
`desktop-v*` tag reaches the secrets, in environment `desktop-signing`, and
only after the owner approves the run there (**Review deployments**):
`MACOS_DEVELOPER_ID_P12` (base64 of a `.p12` in the legacy format rcodesign
reads — a Keychain Access export, or `openssl pkcs12 -export -legacy`),
`MACOS_DEVELOPER_ID_P12_PASSWORD`, `APP_STORE_CONNECT_API_KEY_P8` (the
`AuthKey_<KEY ID>.p8` file as is), `APP_STORE_CONNECT_KEY_ID`,
`APP_STORE_CONNECT_ISSUER_ID` (the UUID above App Store Connect's key
table). `sign.sh` refuses, before signing, a certificate that is not a
Developer ID Application certificate of the team in `MACOS_TEAM_ID` or has
expired. `macos/verify.sh` then checks the result on a Mac with `codesign`,
`stapler` and `spctl` before anything is published — on a tag against that
team, down to the designated requirement (decision 052). `MACOS_TEAM_ID`
must be a **repository** variable, never one of the environment: the build
and check jobs read only the repository's. `desktop/scripts/release-assets.sh`
puts the assets and `SHA256SUMS` together. The
release is all or nothing: when the Apple side fails (an expired
certificate, a key, the notary service), the Linux binaries wait too. A
failure outside the repository (a secret, Apple's service): fix it, then
**Re-run failed jobs** on the tag's run while its artifacts last (7 days);
the re-run signs the same artifacts again, which gives the same code
directory, so a bundle whose ticket the lookup finds is stapled with it
instead of being sent to Apple again. A lookup that fails (no ticket, or
the network) sends the bundle as on a first run.
A fix in the repository: delete the tag (and a draft it made), merge the
fix, tag the new commit — pushing an unchanged tag again starts nothing.

A pull request runs `macos/sign.sh` ad hoc, its release path against a
stand-in for Apple with every flag checked against the pinned rcodesign
(`macos/tests/sign-release-test.sh`), `macos/verify.sh` without a team —
which checks each binary's architecture and starts both architectures'
agent and menu-bar app, the Intel ones under Rosetta — its team checks
against a binary someone else signed (`macos/tests/verify-team-test.sh`)
and `release-assets.sh` over its own build. Locally:
`desktop/macos/tests/sign-release-test.sh` (any OS) and
`desktop/macos/tests/verify-team-test.sh` (macOS). The real Developer ID, Apple's verdict, the secrets step and
the release job first run on a tag: before a release, push
`desktop-vX.Y.Z-rc.1` — it runs all of it into a draft that `setup.sh` never
sees (delete the draft afterwards). A tag that is neither `desktop-vX.Y.Z`
nor `desktop-vX.Y.Z-<suffix>` stops the run, and a tag moved to another
commit while its run waited is not published.

Locally:

```sh
cd desktop
go build ./cmd/vaultsync-tray && ./vaultsync-tray            # this computer
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/vaultsync-tray

# VaultSync for Mac.app for this Mac (unsigned):
CGO_CFLAGS="-O2 -g -mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" \
  go build -o /tmp/vaultsync-tray ./cmd/vaultsync-tray
CGO_ENABLED=0 go build -o /tmp/vaultsync . && rm -rf /tmp/app && mkdir /tmp/app
macos/make-app.sh dev /tmp/vaultsync /tmp/vaultsync-tray /tmp/app
```

## Bumping Syncthing

A pin only ever moves forward: an agent refuses to install a Syncthing
older than the one `agent.json` records (decision 054), so a lowered pin
would stop every computer the newer one reached. desktop-v0.1.0 has no
such refusal, but it refuses an `agent.json` in any format but 1, and a
build from desktop-v0.2.0 on writes format 2 when it installs its engine or
puts its copy in VaultSync's place (setup, or VaultSync for Mac's run or
uninstall) — so a kept v0.1.0 download never puts its Syncthing
2.1.6 over a newer engine's state, and a bump needs no format change. A
release that adds a field an older build must not drop raises
`agentStateVersion` (and lets `loadState` read the older format). Change `syncthingVersion` and the four entries in
`pins.go`. First verify
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

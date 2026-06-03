<div align="center">

<img src="https://raw.githubusercontent.com/psimaker/vaultsync/main/ios/VaultSync/Resources/AppIcon.svg" width="108" height="108" alt="VaultSync">

# [VaultSync](https://apps.apple.com/app/vaultsync/id6761845197)

**Self-hosted Obsidian vault sync for iPhone and iPad.**<br>
Your notes sync peer-to-peer over Syncthing, straight into Obsidian's iOS sandbox — no note cloud, no account, no tracking.

<a href="https://apps.apple.com/app/vaultsync/id6761845197">
  <img src="https://developer.apple.com/assets/elements/badges/download-on-the-app-store.svg" height="44" alt="Download on the App Store">
</a>

<br><br>

[![Stars](https://img.shields.io/github/stars/psimaker/vaultsync?style=flat-square&logo=github&color=2AB5B3&label=Stars)](https://github.com/psimaker/vaultsync/stargazers)
[![License: MPL-2.0](https://img.shields.io/badge/License-MPL_2.0-blue.svg?style=flat-square)](LICENSE)
[![iOS 18+](https://img.shields.io/badge/iOS-18%2B-007AFF?style=flat-square&logo=apple&logoColor=white)](https://developer.apple.com/ios/)
[![CI](https://img.shields.io/github/actions/workflow/status/psimaker/vaultsync/ci.yml?style=flat-square&logo=github-actions&logoColor=white&label=Build)](https://github.com/psimaker/vaultsync/actions/workflows/ci.yml)

<img src="docs/images/screenshot-welcome.png" width="30%" alt="VaultSync welcome screen">
<img src="docs/images/screenshot-home.png" width="30%" alt="VaultSync home screen">

</div>

---

## 🔭 Why VaultSync

- **Peer-to-peer & private** — syncs directly between your own devices over [Syncthing](https://syncthing.net/). No note cloud, no account, no tracking.
- **Lands in Obsidian** — files sync into Obsidian's iOS sandbox, where the app already looks for them.
- **Pair by QR, resolve conflicts** — connect your server in seconds; settle Markdown conflicts with side-by-side diffs.
- **Server changes wake your iPhone** — optional Cloud Relay nudges the app the moment your server updates, so incoming notes land even while it's closed. An activity timeline and diagnostics show exactly what synced.

*VoiceOver and Dynamic Type throughout. Localized in English, German, Spanish, and Simplified Chinese. Independent project — not affiliated with Obsidian or Syncthing.*

---

## 🧭 How it works

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/vaultsync-architecture-dark.svg">
  <img alt="VaultSync architecture: your server with Syncthing syncs peer-to-peer with the VaultSync iOS app, which writes notes into your Obsidian vault. An optional Cloud Relay sends silent push wake-ups." src="docs/images/vaultsync-architecture-light.svg" width="100%">
</picture>

</div>

Syncthing runs on a machine you keep on; VaultSync joins as a peer and syncs into Obsidian. The optional sidecar + Cloud Relay wake your iPhone when the server changes.

---

## 🚀 Quick start

1. **Install** VaultSync from the [App Store](https://apps.apple.com/app/vaultsync/id6761845197).
2. **Pair your server** — scan its Syncthing Device ID by QR (or paste it), then accept the connection in that Syncthing instance.
3. **Sync your vault** — VaultSync detects your Obsidian vaults, connects the share, and runs the first sync. Open Obsidian; your notes are there.
4. **(Optional)** Enable **Cloud Relay** for faster server→iPhone updates — see below.

---

## ☁️ Cloud Relay (optional)

Without it, VaultSync syncs server changes when you open the app. **With it, your iPhone wakes on its own the moment your server changes** — even while VaultSync is closed.

### Turn it on — about 2 minutes

1. **Subscribe** in the app (monthly or yearly, at your local App Store price).
2. After you subscribe, VaultSync shows the command below with a **Copy** button — copy it.
3. **Change one thing** (the folder path), then **run it on the computer or NAS that runs Syncthing** — paste it into a terminal there.

```bash
docker run -d --name vaultsync-notify --restart unless-stopped \
  --network host \
  -v /PATH/TO/syncthing:/config:ro \
  -e SYNCTHING_CONFIG=/config/config.xml \
  -e RELAY_URL=https://relay.vaultsync.eu \
  ghcr.io/psimaker/vaultsync-notify:latest
```

> 👉 **The only edit:** replace `/PATH/TO/syncthing` with your Syncthing config folder — usually `~/.local/state/syncthing` or `~/.config/syncthing`. There's **no API key to copy.**

Done — the helper wakes your iPhone once on startup and VaultSync flips to **Cloud Relay active** by itself; sending edits *from* your iPhone stays most reliable with the app open. The relay only ever sees the Device ID and push token needed to route a wake-up — never your notes, file or folder names, or vault structure ([PRIVACY.md](PRIVACY.md)). On a NAS or prefer Docker Compose? The [full guide](notify/README.md) covers both.

---

## 📋 Requirements

| Requirement | Details |
|---|---|
| iPhone / iPad | iOS / iPadOS 18 or later |
| Obsidian | Installed on iOS / iPadOS |
| Syncthing | Running on a Mac, Linux machine, NAS, or homeserver |
| Cloud Relay | Optional — monthly or yearly in-app subscription |
| vaultsync-notify | Optional Docker sidecar for server-side wake-ups |

---

## 🔨 Build from source

Requires **Xcode 26+**, **Go 1.26+**, **gomobile**, **XcodeGen**, **Make**.

```bash
git clone https://github.com/psimaker/vaultsync.git && cd vaultsync
cd go && make patch && make xcframework && cd ..   # Go xcframework (~160 MB, bundles Syncthing)
cd ios && xcodegen generate && open VaultSync.xcodeproj
```

Full build, signing, and test steps: [docs/setup.md](docs/setup.md).

| | |
|---|---|
| Platform | iOS / iPadOS 18+ |
| Language | Swift 6, SwiftUI |
| Sync engine | Syncthing 2.x via Go/gomobile `.xcframework` |
| Background | `BGAppRefreshTask` + `BGContinuedProcessingTask` (iOS 26+ when available) |
| Push | APNs silent push via Cloud Relay |
| License | [MPL-2.0](LICENSE) |

---

## 📚 Documentation

| Doc | What it covers |
|---|---|
| [docs/setup.md](docs/setup.md) | Build and development setup |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Common failures and exact fixes |
| [docs/architecture.md](docs/architecture.md) | Codebase structure and sync strategy |
| [docs/relay-spec.md](docs/relay-spec.md) | Cloud Relay protocol reference |
| [notify/README.md](notify/README.md) | Notify sidecar setup and diagnostics |
| [PRIVACY.md](PRIVACY.md) · [TERMS.md](TERMS.md) | Privacy policy and license terms |

Filing a bug? Include your iOS and VaultSync versions, your server's Syncthing version, whether Cloud Relay and `vaultsync-notify` are running, and relevant logs or screenshots.

---

## License & acknowledgments

[MPL-2.0](LICENSE) — use, modify, and distribute under the Mozilla Public License 2.0.

Built on [Syncthing](https://syncthing.net/) (the file-sync engine) and [gomobile](https://github.com/golang/mobile) (which embeds it on iOS), for [Obsidian](https://obsidian.md). VaultSync is independent and not affiliated with, endorsed by, or sponsored by Obsidian or Syncthing.

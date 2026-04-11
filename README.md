<div align="center">

<img src="https://raw.githubusercontent.com/psimaker/vaultsync/main/ios/VaultSync/Resources/AppIcon.svg" width="108" height="108" alt="VaultSync">

# [VaultSync](https://apps.apple.com/app/vaultsync/id6761845197)

**Self-hosted Obsidian vault sync for iOS — powered by Syncthing.**<br>
Your notes, your devices, your server — no third-party cloud required.

<a href="https://apps.apple.com/app/vaultsync/id6761845197">
  <img src="https://developer.apple.com/assets/elements/badges/download-on-the-app-store.svg" height="44" alt="Download on the App Store">
</a>

<br>

[![Stars](https://img.shields.io/github/stars/psimaker/vaultsync?style=flat-square&logo=github&color=2AB5B3&label=Stars)](https://github.com/psimaker/vaultsync/stargazers)
[![Forks](https://img.shields.io/github/forks/psimaker/vaultsync?style=flat-square&logo=github&color=2AB5B3&label=Forks)](https://github.com/psimaker/vaultsync/network)
[![Contributors](https://img.shields.io/github/contributors/psimaker/vaultsync?style=flat-square&logo=github&color=2AB5B3&label=Contributors)](https://github.com/psimaker/vaultsync/graphs/contributors)
[![License: MPL-2.0](https://img.shields.io/badge/License-MPL_2.0-blue.svg?style=flat-square)](LICENSE)

[![CI](https://img.shields.io/github/actions/workflow/status/psimaker/vaultsync/ci.yml?style=flat-square&logo=github-actions&logoColor=white&label=Build)](https://github.com/psimaker/vaultsync/actions/workflows/ci.yml)
[![Last Commit](https://img.shields.io/github/last-commit/psimaker/vaultsync?style=flat-square&label=Last+Commit)](https://github.com/psimaker/vaultsync/commits)

[![iOS 26+](https://img.shields.io/badge/iOS-26%2B-007AFF?style=flat-square&logo=apple&logoColor=white)](https://developer.apple.com/ios/)
[![Swift 6](https://img.shields.io/badge/Swift-6-FA7343?style=flat-square&logo=swift&logoColor=white)](https://swift.org)
[![Xcode 26+](https://img.shields.io/badge/Xcode-26%2B-147EFB?style=flat-square&logo=xcode&logoColor=white)](https://developer.apple.com/xcode/)
[![Open Issues](https://img.shields.io/github/issues/psimaker/vaultsync?style=flat-square&label=Issues)](https://github.com/psimaker/vaultsync/issues)
[![Open PRs](https://img.shields.io/github/issues-pr-raw/psimaker/vaultsync?style=flat-square&label=PRs)](https://github.com/psimaker/vaultsync/pulls)

</div>

---

<p align="center">
  <img src="docs/images/screenshot-welcome.png" width="30%" alt="Welcome">
  <img src="docs/images/screenshot-home.png" width="30%" alt="Home">
</p>

---

## 🆕 What's New — v1.0.1

> **🛡️ Smarter Sync** — Obsidian workspace files and the Trash folder are now automatically excluded from sync, preventing the most common conflicts<br>
> **📡 Reliable Push Notifications** — Cloud Relay token management improved — push sync now self-heals when device tokens rotate<br>
> **🏠 Cleaner Home Screen** — Activity section streamlined to a single timeline link<br>
> **🔧 Permissions Fix** — Accepted folder shares now correctly ignore file permissions, fixing sync failures in Docker environments
>
> See [CHANGELOG.md](CHANGELOG.md) for full details.


---

## ✨ Features

<table>
<tr>
<td width="50%" valign="top">

**🔄 Syncthing-Powered Sync**
Proven, battle-tested protocol — your data syncs directly between your devices over LAN or Internet

**📂 Obsidian-First Design**
Syncs directly into Obsidian's sandbox on iOS — open Obsidian and your vault is up to date

**📋 5-Step Setup Checklist**
Interactive guided setup — scan a QR code to pair, accept the connection, and start syncing

**⚔️ Conflict Resolver**
Side-by-side Markdown diffs when files conflict — choose the right version with confidence

**📡 Real-Time Activity Timeline**
See exactly what's syncing, when it synced, and catch issues early

**🔐 Self-Hosted & Private**
No third-party cloud, no account required — your data never leaves your devices

</td>
<td width="50%" valign="top">

**☁️ Cloud Relay** *(recommended — $0.99/month)*
Near-realtime push-based sync via silent APNs notifications — no need to open the app

**🐳 vaultsync-notify Sidecar**
Lightweight Docker container watches Syncthing for changes and signals the relay — only the Device ID is sent, never file content

**🔧 Sync Issues Panel**
Actionable remediation for sync problems — diagnose and fix without leaving the app

**🏥 Relay Diagnostics**
Health checks and troubleshooting for your Cloud Relay connection

**🔄 Background Sync**
iOS Background Refresh keeps your vault updated even when VaultSync isn't open

**♿ Accessibility**
Full VoiceOver and Dynamic Type support throughout the app

</td>
</tr>
</table>

---

## 🏗️ How It Works

```
┌──────────────┐     Syncthing protocol      ┌──────────────────┐
│  Your Mac /  │◄───────────────────────────►│    VaultSync      │
│  Linux / NAS │     (LAN or Internet)        │    (iOS)          │
│  + Syncthing │                              │                   │
└──────┬───────┘                              │  Syncs directly   │
       │                                      │  into Obsidian    │
       │  vaultsync-notify                    │  sandbox          │
       │  (Docker sidecar)                    └────────▲──────────┘
       │         │                                     │
       │         │ wake-up signal              APNs    │
       │         ▼                             push    │
       │  ┌──────────────────┐                         │
       │  │ relay.vaultsync  │─────────────────────────┘
       │  │      .eu         │   (Cloud Relay, recommended)
       │  └──────────────────┘
```

1. **Syncthing** runs on your desktop/server and syncs files with VaultSync over the Syncthing protocol
2. **VaultSync** receives files directly into Obsidian's sandbox on iOS — open Obsidian and your vault is up to date
3. **vaultsync-notify** (optional) watches your Syncthing instance and signals the Cloud Relay when files change
4. **Cloud Relay** sends a silent push notification to wake VaultSync for immediate sync

---

## 🚀 Getting Started

**1. Install VaultSync**

Download VaultSync from the **[App Store](https://apps.apple.com/app/vaultsync/id6761845197)** (free).

**2. Connect Your Syncthing Device**

Open VaultSync and scan your desktop Syncthing Device ID via QR code — or enter it manually. Accept the connection on your desktop Syncthing instance.

**3. Sync Your Vault**

VaultSync automatically detects Obsidian vaults. Select which vault to sync and it appears directly in Obsidian on your iPhone or iPad.

**4. Enable Instant Sync (optional but recommended)**

Cloud Relay gives you near-realtime push-based sync — your vault updates on iOS within seconds of a change on your server, without opening the app. Subscribe in the app ($0.99/month), then set up the notify container on your homeserver:

```bash
curl -fsSL https://raw.githubusercontent.com/psimaker/vaultsync/main/notify/scripts/bootstrap.sh | bash
```

The script auto-detects your Syncthing instance, configures the container, and starts it — typically done in under a minute.

<details>
<summary><strong>Advanced: Manual Docker Compose setup</strong></summary>
<br>

If you prefer to configure manually, add this to your existing `docker-compose.yml`:

```yaml
vaultsync-notify:
  image: ghcr.io/psimaker/vaultsync-notify:latest
  environment:
    SYNCTHING_API_URL: http://syncthing:8384
    SYNCTHING_API_KEY: your-api-key
    RELAY_URL: https://relay.vaultsync.eu
    RELAY_API_KEY: your-relay-api-key
```

See [notify/README.md](notify/README.md) for full configuration options.

</details>

---

## 📡 Cloud Relay & vaultsync-notify

Cloud Relay solves a core iOS limitation: apps can't sync in the background in real-time. When files change on your server, the [vaultsync-notify](notify/) container sends a wake-up signal to the relay, which pushes a silent notification to your iPhone — VaultSync wakes up and syncs immediately.

**Privacy-first:** only the Syncthing Device ID is sent as a wake-up signal — **no file names, folder names, or content ever leaves your server**.

Without Cloud Relay, VaultSync still works — just open the app to trigger a sync or rely on iOS background refresh. But for the best experience, Cloud Relay is highly recommended.

See [notify/README.md](notify/README.md) for details.

---

## 🔮 Self-Hosted Relay

A fully self-hosted relay option (replacing `relay.vaultsync.eu` with your own server) is planned for a future release. In the meantime, the app works without any relay via manual sync and iOS background refresh.

---

<details>
<summary><strong>📊 Technical Details</strong></summary>
<br>

| | |
|---|---|
| **Platform** | iOS 26+ |
| **Language** | Swift 6 (strict concurrency), SwiftUI |
| **Sync engine** | Syncthing v2.x via gomobile (.xcframework) |
| **Background sync** | BGAppRefreshTask + BGContinuedProcessingTask |
| **Push sync** | APNs silent notifications via Cloud Relay |
| **License** | MPL-2.0 |

</details>

<details>
<summary><strong>🔨 Building from Source</strong></summary>
<br>

**Requirements:** Xcode 26+, Go 1.26+ with gomobile, XcodeGen, Make

```bash
git clone https://github.com/psimaker/vaultsync.git
cd vaultsync

# Patch vendored dependencies and build the Go xcframework
cd go && make patch && make xcframework && cd ..

# Generate and open Xcode project
cd ios && xcodegen generate
open VaultSync.xcodeproj
```

See [docs/setup.md](docs/setup.md) for detailed build instructions.

</details>

<details>
<summary><strong>📁 Project Structure</strong></summary>
<br>

```
├── ios/                  # Swift/SwiftUI iOS app
├── go/                   # Go bridge (gomobile → .xcframework)
├── notify/               # vaultsync-notify Docker container
├── docs/                 # Architecture and setup docs
└── .github/workflows/    # CI pipeline
```

</details>

---

## 🤝 Contributing

Contributions are welcome. Please follow the project conventions (Swift API Design Guidelines, standard Go conventions, Conventional Commits) and open a PR with a description.

- [docs/setup.md](docs/setup.md) — Build instructions
- [docs/architecture.md](docs/architecture.md) — Codebase structure
- [docs/troubleshooting.md](docs/troubleshooting.md) — Common runtime failures and exact fixes

---

## 🙏 Acknowledgments

VaultSync is built on top of these projects:

| Project | |
|---|---|
| [Syncthing](https://syncthing.net/) | The open-source file synchronization program powering VaultSync |
| [gomobile](https://github.com/golang/mobile) | Go on mobile — enables the embedded sync engine |

VaultSync is designed for [Obsidian](https://obsidian.md) — the powerful knowledge base for your local Markdown files.

---

## License

[MPL-2.0](LICENSE) — You may freely use, modify, and distribute this software under the terms of the Mozilla Public License 2.0.

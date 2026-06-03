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
- **Honest about iOS limits** — optional Cloud Relay wakes the app when your server changes; an activity timeline and actionable diagnostics show what's happening.

*VoiceOver and Dynamic Type throughout. Localized in English, German, Spanish, and Simplified Chinese. Independent project — not affiliated with Obsidian or Syncthing.*

---

## 🧭 How it works

```mermaid
flowchart LR
    S["🖥️ Your Mac / Linux / NAS<br/>+ Syncthing"] <-->|"Syncthing protocol<br/>LAN or Internet"| A["📱 VaultSync<br/>iOS / iPadOS"]
    A --> O["📝 Obsidian vault<br/>iOS sandbox"]
    S -. "optional sidecar<br/>vaultsync-notify" .-> R["☁️ relay.vaultsync.eu"]
    R -. "APNs silent push<br/>wake-up only" .-> A
```

Syncthing runs on a machine you keep on; VaultSync joins as a peer and syncs into Obsidian. The optional sidecar + Cloud Relay wake your iPhone when the server changes.

---

## 🚀 Quick start

1. **Install** VaultSync from the [App Store](https://apps.apple.com/app/vaultsync/id6761845197).
2. **Pair your server** — scan its Syncthing Device ID by QR (or paste it), then accept the connection in that Syncthing instance.
3. **Sync your vault** — VaultSync detects your Obsidian vaults, connects the share, and runs the first sync. Open Obsidian; your notes are there.
4. **(Optional)** Enable **Cloud Relay** for faster server→iPhone updates — see below.

---

## ☁️ Cloud Relay (optional)

Cloud Relay sends your iPhone an APNs silent push the moment your server changes, so incoming sync feels near-realtime instead of waiting for the next time you open the app. It **self-activates**: as soon as the helper starts it sends one wake-up, and VaultSync flips to **Cloud Relay active** on its own — no extra step.

> **The honest sync promise.** iOS forbids always-on background daemons. **Server → iPhone** is near-realtime when relay pushes arrive; **iPhone → server** is most reliable when VaultSync is open. Background refresh may help, but iOS decides if and when it runs.

Run the helper next to Syncthing with Docker Compose. It's **key-free** — the helper reads the Syncthing API key from the shared `config.xml`, so the only value you supply is `RELAY_URL`:

```yaml
vaultsync-notify:
  image: ghcr.io/psimaker/vaultsync-notify:latest
  user: "1000:1000"                     # uid that owns Syncthing's config.xml
  environment:
    SYNCTHING_API_URL: http://syncthing:8384
    SYNCTHING_CONFIG: /var/syncthing/config/config.xml
    RELAY_URL: https://relay.vaultsync.eu
  volumes:
    - syncthing-data:/var/syncthing:ro  # Syncthing's config volume, read-only
```

A plain `docker compose up` sends one real wake-up to production (the intended self-activation) — point `RELAY_URL` at a mock when testing locally.

> **NAS users:** `user:` must match the uid that owns `config.xml` (mode `0600`, so a mismatched uid can't read it). linuxserver = `911`, Unraid = `99:100`; Synology/QNAP also need `SYNCTHING_CONFIG` set to the real `config.xml`. Details in [notify/README.md](notify/README.md).

Cloud Relay is an optional monthly or yearly subscription, priced in your App Store storefront (read from StoreKit at runtime). It **never** receives note content, file or folder names, vault structure, or metadata — only the Syncthing Device ID and APNs token needed to route a wake-up ([PRIVACY.md](PRIVACY.md)). A self-hosted relay is on the roadmap.

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

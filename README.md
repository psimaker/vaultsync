<div align="center">

<img src="https://raw.githubusercontent.com/psimaker/vaultsync/main/ios/VaultSync/Resources/AppIcon.svg" width="108" height="108" alt="VaultSync">

# [VaultSync](https://apps.apple.com/app/vaultsync/id6761845197)

**Self-hosted Obsidian vault sync for iPhone and iPad — powered by Syncthing.**<br>
Your notes, your devices, your server. No managed note-sync cloud required.

<a href="https://apps.apple.com/app/vaultsync/id6761845197">
  <img src="https://developer.apple.com/assets/elements/badges/download-on-the-app-store.svg" height="44" alt="Download on the App Store">
</a>

<br><br>

[![Stars](https://img.shields.io/github/stars/psimaker/vaultsync?style=flat-square&logo=github&color=2AB5B3&label=Stars)](https://github.com/psimaker/vaultsync/stargazers)
[![License: MPL-2.0](https://img.shields.io/badge/License-MPL_2.0-blue.svg?style=flat-square)](LICENSE)
[![iOS 18+](https://img.shields.io/badge/iOS-18%2B-007AFF?style=flat-square&logo=apple&logoColor=white)](https://developer.apple.com/ios/)
[![CI](https://img.shields.io/github/actions/workflow/status/psimaker/vaultsync/ci.yml?style=flat-square&logo=github-actions&logoColor=white&label=Build)](https://github.com/psimaker/vaultsync/actions/workflows/ci.yml)

</div>

---

<p align="center">
  <img src="docs/images/screenshot-welcome.png" width="30%" alt="VaultSync welcome screen">
  <img src="docs/images/screenshot-home.png" width="30%" alt="VaultSync home screen">
</p>

---

## What VaultSync does

VaultSync is the iOS bridge for people who already trust Syncthing with their Obsidian vault. If your notes live on your own Mac, Linux box, NAS, or homeserver, VaultSync lets your iPhone or iPad join that setup — syncing **directly into Obsidian’s iOS sandbox**, so your vault appears where Obsidian expects it.

- Syncs your vault peer-to-peer with your own devices using **Syncthing** — no note cloud, no account, no tracking
- Pairs with your desktop/server by **QR code**
- Resolves Markdown conflicts with **side-by-side diffs**
- Shows a **sync activity timeline** and **actionable diagnostics** for common issues
- Optional **Cloud Relay** wakes your iPhone the moment your server changes
- Full **VoiceOver** and **Dynamic Type** support, localized in **English, German, Spanish, and Simplified Chinese**

VaultSync is **free**. Cloud Relay is an optional monthly or yearly subscription, priced in your App Store storefront. VaultSync is independent and not affiliated with Obsidian or Syncthing.

**Built for** Obsidian users who want self-hosted iOS sync, existing Syncthing/homelab/NAS setups, and privacy-conscious Markdown users.

---

## The honest sync promise

iOS does not let third-party apps run an always-on sync daemon in the background. VaultSync is designed around that limit instead of hiding it:

> **Near-realtime incoming sync to iPhone. Reliable outgoing sync when VaultSync is open.**

- **Server → iPhone/iPad:** near-realtime when Cloud Relay silent pushes are delivered
- **iPhone/iPad → Server:** most reliable when you open VaultSync
- **Background refresh** may help opportunistically, but iOS decides when — and whether — it runs

VaultSync is **not** a hosted note-sync service and **not** a magic always-on daemon. It combines foreground sync, iOS background refresh, and optional silent-push wake-ups to make Syncthing-based Obsidian sync practical on iPhone and iPad.

---

## What’s New — v1.5.1

**v1.5.1 makes Cloud Relay activate itself.** After you subscribe, the server helper (`vaultsync-notify`) sends one wake-up the moment it starts, so VaultSync flips to **Cloud Relay active** on its own the moment that wake-up arrives — no waiting for the next change. Setting up the helper no longer asks for an API key: it reads the Syncthing key (and address) straight from `config.xml`, so the in-app command and `docker compose up -d` are key-free. A reactivation card reaches subscribers whose server has never woken their iPhone, and the Cloud Relay screen shows honest live states — **not active yet**, **active**, or **went quiet**.

It also makes vault locations self-healing: iOS can move an app’s private storage (on reinstall, restore, or device migration), which used to strand an older vault on a permanent “cannot access this folder” error. VaultSync now re-derives every vault’s location from your Obsidian folder on launch, and a vault that still can’t be reached offers a one-tap **Remove this vault** or **Reconnect to Obsidian** instead of a dead end ([#25](https://github.com/psimaker/vaultsync/issues/25)).

It builds on the **v1.5.0** redesign: a tabbed home screen (Sync · Devices · Cloud Relay) with a persistent, glanceable status header, a coherent design system that is correct in light and dark mode, status that never relies on color alone, onboarding whose steps actually run the setup for you, and **Cloud Relay** in its own tab with honest delivery status, a privacy-first pitch, a yearly plan, and Apple-verified subscriptions. See [CHANGELOG.md](CHANGELOG.md) for details.

---

## Features

<table>
<tr>
<td width="50%" valign="top">

### Syncthing-powered sync

Syncs your vault between your own devices over LAN or the Internet — directly into Obsidian’s iOS sandbox.

### Markdown conflict resolver

When files conflict, VaultSync shows side-by-side Markdown diffs so you can pick the right version with confidence.

### Sync activity & issues

A timeline of what synced and when, plus an issues panel with actionable fixes for common setup and runtime problems.

</td>
<td width="50%" valign="top">

### Optional Cloud Relay

Wakes your iPhone when your server has changes, making incoming server-to-iPhone sync feel near-realtime — via a small Docker helper (a “sidecar”) on your server and APNs silent push.

### Relay diagnostics

Built-in checks help you confirm whether Cloud Relay and APNs are reachable and whether wake-ups are actually being delivered.

### Accessibility & localization

VoiceOver and Dynamic Type throughout, localized in English, German, Spanish, and Simplified Chinese.

</td>
</tr>
</table>

---

## How it works

```text
┌──────────────┐     Syncthing protocol      ┌──────────────────┐
│  Your Mac /  │◄───────────────────────────►│    VaultSync     │
│  Linux / NAS │     LAN or Internet         │    iOS / iPadOS  │
│  + Syncthing │                             │                  │
└──────┬───────┘                             │  Syncs directly  │
       │                                     │  into Obsidian’s │
       │                                     │  iOS sandbox     │
       │                                     └─────────▲────────┘
       │  Optional                                     │
       │  vaultsync-notify                             │
       │  Docker sidecar                               │
       ▼                                               │
┌──────────────────┐        APNs silent push           │
│ relay.vaultsync  │───────────────────────────────────┘
│      .eu         │        wake-up signal only
└──────────────────┘
```

1. **Syncthing** runs on your desktop, server, NAS, or homelab device.
2. **VaultSync** syncs files into Obsidian’s iOS sandbox on your iPhone or iPad.
3. **vaultsync-notify** (optional Docker sidecar) watches your Syncthing instance for real outgoing changes.
4. **Cloud Relay** sends a silent push wake-up to your iPhone when the server has new changes.
5. **VaultSync wakes** and pulls the changes. Local iPhone edits sync most reliably when you open the app.

---

## Cloud Relay & privacy

Cloud Relay is optional and improves only the **server → iPhone** direction: without it, you open VaultSync to sync (iOS background refresh may also help opportunistically); with it, a silent push wakes the app the moment your server changes.

It is **not** a note cloud. It never receives note content, Markdown text, file or folder names, vault structure, or metadata — only the minimal routing info (your Syncthing Device ID and APNs token) needed to deliver a wake-up signal. Full details in [PRIVACY.md](PRIVACY.md).

---

## Getting started

1. **Install** VaultSync from the [App Store](https://apps.apple.com/app/vaultsync/id6761845197).
2. **Pair your server:** scan your desktop/server Syncthing Device ID by QR code (or enter it manually), then accept the connection on that Syncthing instance.
3. **Sync your vault:** VaultSync detects Obsidian vaults on iOS. Pick the vault, connect it to your Syncthing share, and let the first sync finish. Open Obsidian — your vault is where it expects it.
4. **(Optional) Enable Cloud Relay** for faster server-to-iPhone updates: subscribe in the app, then set up the notify sidecar below.

### Cloud Relay sidecar

The simplest setup runs Syncthing and the helper together with Docker Compose. The helper reads the Syncthing API key from the shared config volume, so there is **no key to copy** — the only value you supply is `RELAY_URL`:

```yaml
vaultsync-notify:
  image: ghcr.io/psimaker/vaultsync-notify:latest
  user: "1000:1000"                    # match the uid that owns Syncthing's config.xml
  environment:
    SYNCTHING_API_URL: http://syncthing:8384
    SYNCTHING_CONFIG: /var/syncthing/config/config.xml
    RELAY_URL: https://relay.vaultsync.eu
  volumes:
    - syncthing-data:/var/syncthing:ro  # share Syncthing's config volume read-only
```

> A plain `docker compose up` sends one real wake-up to production (the intended self-activation). Point `RELAY_URL` at a mock when testing locally.

Running Syncthing **natively on the host** instead? `bootstrap.sh` is a guided helper for that case — it auto-detects your `config.xml`, validates connectivity, writes a `notify/.env`, and tells you how to run the binary (it does not start the Compose stack):

```bash
curl -fsSL https://raw.githubusercontent.com/psimaker/vaultsync/main/notify/scripts/bootstrap.sh | bash
```

Relay authentication uses your Syncthing Device ID, which the sidecar reads automatically — no relay key needed. Optional tuning (`DEBOUNCE_SECONDS`, `WATCHED_FOLDERS`) and full options are in [notify/README.md](notify/README.md).

---

## Requirements

| Requirement | Details |
|---|---|
| iPhone or iPad | iOS/iPadOS 18 or later |
| Obsidian | Installed on iOS/iPadOS |
| Syncthing | Running on a Mac, Linux machine, NAS, or homeserver |
| Cloud Relay | Optional, monthly or yearly in-app subscription |
| vaultsync-notify | Optional Docker sidecar for server-side wake-ups |

A fully self-hosted relay (replacing `relay.vaultsync.eu` with your own server) is planned for a future release.

---

## Building from source

Requires **Xcode 26+**, **Go 1.26+**, **gomobile**, **XcodeGen**, and **Make**.

```bash
git clone https://github.com/psimaker/vaultsync.git
cd vaultsync

# Build the Go xcframework (~160 MB; bundles the Syncthing engine)
cd go && make patch && make xcframework && cd ..

# Generate and open the Xcode project
cd ios && xcodegen generate && open VaultSync.xcodeproj
```

Full build, signing, and test instructions: [docs/setup.md](docs/setup.md).

### Technical details

| | |
|---|---|
| Platform | iOS/iPadOS 18+ |
| Language | Swift 6, SwiftUI |
| Sync engine | Syncthing 2.x via Go/gomobile `.xcframework` |
| Background execution | `BGAppRefreshTask` + `BGContinuedProcessingTask` (iOS 26+ when available) |
| Push wake-ups | APNs silent notifications via Cloud Relay |
| License | [MPL-2.0](LICENSE) |

### Project structure

```text
├── ios/                  # Swift/SwiftUI iOS app
├── go/                   # Go bridge: gomobile → .xcframework
├── notify/               # vaultsync-notify Docker sidecar
├── docs/                 # Architecture, setup, troubleshooting, relay spec
└── .github/workflows/    # CI pipeline
```

---

## Documentation

| Doc | What it covers |
|---|---|
| [docs/setup.md](docs/setup.md) | Build and development setup |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Common runtime failures and exact fixes |
| [docs/architecture.md](docs/architecture.md) | Codebase structure and sync strategy |
| [docs/relay-spec.md](docs/relay-spec.md) | Cloud Relay protocol reference |
| [notify/README.md](notify/README.md) | Notify sidecar setup and diagnostics |
| [PRIVACY.md](PRIVACY.md) · [TERMS.md](TERMS.md) | Privacy policy and license terms |

Found a bug? Open an issue with your iOS/iPadOS and VaultSync versions, your server’s Syncthing version, whether Cloud Relay and `vaultsync-notify` are running, and relevant logs or screenshots.

---

## Contributing

Contributions are welcome. Please follow the project conventions: Swift API Design Guidelines, Swift strict concurrency where applicable, standard Go conventions, Conventional Commits, and clear PR descriptions. Start with [docs/setup.md](docs/setup.md) and [docs/architecture.md](docs/architecture.md).

---

## Acknowledgments

Built on [Syncthing](https://syncthing.net/) (the open-source file-synchronization engine powering VaultSync) and [gomobile](https://github.com/golang/mobile) (which embeds it on iOS). Designed for [Obsidian](https://obsidian.md), the knowledge base for local Markdown files.

VaultSync is an independent project and is not affiliated with, endorsed by, or sponsored by Obsidian or Syncthing.

---

## License

[MPL-2.0](LICENSE) — use, modify, and distribute under the terms of the Mozilla Public License 2.0.

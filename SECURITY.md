# Security Policy

VaultSync syncs users' personal notes. Security reports are taken seriously and handled with priority — thank you for taking the time to report responsibly.

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

- **Preferred:** [GitHub Private Vulnerability Reporting](https://github.com/psimaker/vaultsync/security/advisories/new) — opens a private advisory visible only to you and the maintainer.
- **Alternative:** email [umut.erdem@protonmail.com](mailto:umut.erdem@protonmail.com).

Please include what you can: affected component and version, steps to reproduce, and your assessment of the impact. A proof of concept helps but is not required.

## What to expect

VaultSync is maintained by a single person. Honest expectations:

- You will get an acknowledgment within a few days.
- Confirmed vulnerabilities are fixed with priority and disclosed in coordination with you — details stay private until a fixed version has shipped.
- You will be credited in the release notes if you wish.
- There is no bug bounty program.

## Supported versions

Only the **latest release** of each component receives security fixes. If you are on an older version, please update first.

| Component | Supported version |
|---|---|
| VaultSync iOS app | Latest App Store release (currently 3.0.1) |
| VaultSync Hub (`vaultsync-hub`) | Latest `hub-v*` release (currently 0.3.1) |
| VaultSync for Mac and Linux (`vaultsync` desktop agent) | Latest `desktop-v*` release (currently 0.3.0) |
| `vaultsync-notify` server helper | Latest `notify-v*` release (currently 2.0.2) |
| Cloud Relay (hosted service) | The currently deployed version (1.4.1) — operated by the maintainer, no user action needed |

## Scope

In scope:

- The **iOS app** (`ios/`), including the embedded Go sync bridge (`go/bridge/`)
- The **VaultSync Hub** (`hub/`): the pairing coordinator and the Docker Compose stack it runs in
- **VaultSync for Mac and Linux** (`desktop/`): the desktop agent and its menu-bar app
- The **`vaultsync-notify` server helper** (`notify/`)
- The **installer scripts**: `hub/scripts/setup.sh` (served at `https://vaultsync.eu/setup.sh`), `notify/scripts/install.sh` and `notify/scripts/install.ps1` (served at `https://vaultsync.eu/notify.sh` and `https://vaultsync.eu/notify.ps1`)
- **Release artifacts**: the Docker images of the Hub and the helper, and the prebuilt binaries of the Hub, the helper and the desktop agent, including VaultSync for Mac
- The **Cloud Relay service** at `relay.vaultsync.eu` — the code lives in a separate repository, but the service is operated by the maintainer, so reports about its protocol or behavior are welcome here

Strict limits for the hosted Relay:

- **No testing against production infrastructure.** Report suspected issues from reading the [protocol reference](docs/relay-spec.md) or from traffic your own devices generate in normal use.
- **No denial-of-service or load testing** of any kind.
- **Never access, or attempt to access, data belonging to other users.**

## Security design documentation

The security-relevant design decisions — including threat models and the invariants that protect user data — are recorded in [`docs/decisions/`](docs/decisions/). The [Cloud Relay protocol reference](docs/relay-spec.md) documents the relay/helper/app trust boundaries.

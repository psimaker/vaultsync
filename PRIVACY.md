# Privacy Policy

**Effective date:** April 8, 2026

VaultSync is designed to keep your data on your devices. This policy explains what information is — and isn't — collected.

## Data Collection

**VaultSync does not collect any personal data.** All files are synchronized directly between your devices using Syncthing's peer-to-peer protocol. Nothing passes through our servers.

## Cloud Relay

If you subscribe to the optional Cloud Relay service, the following data is stored on the relay server (`relay.vaultsync.eu`):

- **Syncthing Device ID** — used to route push notifications to your device
- **APNs device token** — used to deliver silent push notifications via Apple Push Notification service
- **StoreKit transaction ID** — used to validate your active subscription with Apple

That's it. **No file content, file names, folder names, or any other metadata ever leaves your homeserver.** The relay receives only a wake-up signal containing the Device ID.

### Data Security

- APNs device tokens are encrypted at rest (AES-256-GCM)
- Tokens are automatically deleted after 90 days without a successful push delivery

### Subscriptions

Subscription billing is handled entirely by Apple through StoreKit. VaultSync does not process or store any payment information.

## Third-Party Services

VaultSync uses **no tracking SDKs, no analytics, and no advertising**. The only external service involved is Apple Push Notification service (APNs) for Cloud Relay subscribers.

## Your Rights (GDPR)

You can request deletion of all data associated with your device at any time by contacting us. Upon request, we will remove your Device ID, APNs token, and transaction ID from the relay server.

## Contact

For privacy-related questions or data deletion requests:

**Email:** umut.erdem@protonmail.com

## Changes

If this policy changes, the updated version will be published in this repository with a new effective date.

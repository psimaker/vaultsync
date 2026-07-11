# Privacy Policy

**Effective date:** July 11, 2026

VaultSync is designed to keep your data on your devices. This policy explains what information is — and isn't — collected.

## Data Collection

VaultSync does not collect note content, usage analytics, advertising identifiers, or tracking data. All files are synchronized directly between your devices using Syncthing's peer-to-peer protocol. The optional Cloud Relay processes only the limited routing and subscription data described below.

## Cloud Relay

If you subscribe to the optional Cloud Relay service, the following data is stored on the relay server (`relay.vaultsync.eu`):

- **Syncthing Device ID** — used to route push notifications to your device
- **APNs device token and a one-way token hash** — the token is used to deliver silent push notifications through Apple Push Notification service; the hash prevents duplicate registrations
- **StoreKit verification record** — original and latest transaction identifiers, product, App Store environment, subscription expiry, transaction signing time, verification time, and whether the registration has completed verified migration
- **Operational timestamps** — when the subscription or push registration was created or updated and when a push was last sent

VaultSync sends the StoreKit signed transaction when registering Cloud Relay. The relay verifies it locally against Apple's certificate chain and retains only the verification fields listed above, not the complete signed transaction.

That's it. **No file content, file names, folder names, or any other metadata ever leaves your homeserver.** The relay receives only a wake-up signal containing the Device ID.

### Data Security

- APNs device tokens are encrypted at rest (AES-256-GCM)
- Tokens that Apple reports as invalid are deleted automatically
- StoreKit signed transactions and transaction identifiers are not written to application logs

### Subscriptions

Subscription billing is handled entirely by Apple through StoreKit. VaultSync does not process or store payment-card or billing information.

## Third-Party Services

VaultSync uses **no tracking SDKs, no analytics, and no advertising**. Cloud Relay subscriptions use Apple's App Store/StoreKit for purchase confirmation and Apple Push Notification service (APNs) for silent wake-ups.

## Your Rights (GDPR)

You can request deletion of all data associated with your device at any time by contacting us. Upon request, we will remove your Device ID, APNs registration, StoreKit verification record, and associated operational timestamps from the relay server.

## Contact

For privacy-related questions or data deletion requests:

**Email:** umut.erdem@protonmail.com

## Changes

If this policy changes, the updated version will be published in this repository with a new effective date.

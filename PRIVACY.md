# Privacy Policy

**Effective date:** July 12, 2026

VaultSync is designed to keep your data on your devices. This policy explains what information is — and isn't — collected.

## Data Collection

VaultSync does not collect note content, usage analytics, advertising identifiers, or tracking data. All files are synchronized directly between your devices using Syncthing's peer-to-peer protocol. The optional Cloud Relay processes only the limited routing and subscription data described below.

## Cloud Relay

If you subscribe to the optional Cloud Relay service, the following data is stored on the relay server (`relay.vaultsync.eu`):

- **Syncthing Device ID** — used to route push notifications to your device
- **APNs device token and a one-way token hash** — the token is used to deliver silent push notifications through Apple Push Notification service; the hash prevents duplicate registrations
- **StoreKit verification record** — original and latest transaction identifiers, product, App Store environment, subscription expiry, transaction signing time, verification time, and whether the registration has completed verified migration
- **Operational timestamps** — when the subscription or push registration was created or updated, when a push was last sent, and when the relay last accepted a wake-up signal for the homeserver Device ID

VaultSync sends the StoreKit signed transaction when registering Cloud Relay. The relay verifies it locally against Apple's certificate chain and retains only the verification fields listed above, not the complete signed transaction.

That's it. **No file content, file names, folder names, or any other metadata ever leaves your homeserver.** The relay receives only a wake-up signal containing the Device ID.

The last-signal timestamp helps the app distinguish a server that has not yet
reached the relay from an iPhone that is still waiting for a wake-up. Because
the current signal is not cryptographically authenticated, this timestamp does
not prove who sent it. It also does not confirm Apple push delivery or successful
file synchronization; only the iPhone can record local wake-up receipt, and sync
progress is evaluated separately. No file or vault metadata is added to this flow.

## Local Synchronization Diagnostics

Relay Diagnostics includes an optional synchronization-path check that begins
only after you tap its button. It takes a fresh local sync-engine baseline and
keeps the current result in memory, separately for each eligible server/folder
pair. Folder and device names are used only to label that local Diagnostics
screen. The random check identifier and per-folder results are not written to
storage, sent to Cloud Relay, or included in logs. Leaving the screen,
cancelling, locking protected data, or an app-lifecycle interruption stops the
finite check.

The check is passive. It creates no marker or probe file, performs no rescan,
and does not rename, overwrite, move, or delete vault data. It only observes
whether the sync engine applies a fresh incoming file change after the local
baseline. Such an observation proves local data progress during the check
window; it does not prove network transfer, upload, controlled download, or a
complete roundtrip. A successful check also cannot guarantee that iOS will
grant future background execution.

VaultSync stores two local timestamps in app preferences: the last silent-push
background-sync start and the last fresh incoming file application observed
during such a run. A timestamp written by older versions from a generic “sync
finished” result is intentionally ignored because it is weaker evidence; that
old timestamp is left untouched only so a rollback can still read the older
app's state. The new timestamps contain no device, folder, vault, path, file,
account, transaction, or check identifier and are removed when the app's local
data is deleted.

Relay diagnostic failures are stored locally as a fixed context, error category,
optional HTTP status, and time. Response bodies are never persisted. Older
free-form Relay diagnostics are deleted when read. Older background outcomes
retain only their time, fixed trigger category, and result during migration;
their free-form detail is discarded and the legacy record is removed. This
migration does not touch subscription state, provisioning, APNs registration,
folder mappings, sync identities, or wake-up history.

Application diagnostic logs contain only generic states, counts, durations, and
error categories. Unfiltered embedded Syncthing logging is disabled because its
upstream attributes can include file paths, folder IDs, or peer IDs. None of the
local diagnostic values above is sent to Cloud Relay. Cloud Relay never receives
a file name, folder or vault name, file or vault path, file content, or
diagnostic check identifier.

### Data Security

- APNs device tokens are encrypted at rest (AES-256-GCM)
- Tokens that Apple reports as invalid are deleted automatically
- StoreKit signed transactions and transaction identifiers are not written to application logs

### Subscriptions

Subscription billing is handled entirely by Apple through StoreKit. VaultSync does not process or store payment-card or billing information.

## Third-Party Services

VaultSync uses **no tracking SDKs, no analytics, and no advertising**. Cloud Relay subscriptions use Apple's App Store/StoreKit for purchase confirmation and Apple Push Notification service (APNs) for silent wake-ups.

## Your Rights (GDPR)

You can request deletion of all data associated with your device at any time by contacting us. Upon request, we will remove your Device ID, APNs registration, StoreKit verification record, last-signal time, and associated operational timestamps from the relay server.

## Contact

For privacy-related questions or data deletion requests:

**Email:** umut.erdem@protonmail.com

## Changes

If this policy changes, the updated version will be published in this repository with a new effective date.

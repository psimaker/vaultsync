# Privacy Policy

**Effective date:** July 13, 2026

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

Application and `vaultsync-notify` operational logs contain only generic states,
bounded counts, durations, status codes, and fixed error categories. The helper
does not log Device IDs, folder IDs, event markers, endpoint URLs, configuration
paths, Syncthing API keys, or raw request/response bodies. Unfiltered embedded
Syncthing logging is disabled because its upstream attributes can include file
paths, folder IDs, or peer IDs. None of the local diagnostic values above is sent
to Cloud Relay. Cloud Relay never receives a file name, folder or vault name,
file or vault path, file content, or diagnostic check identifier.

### Dormant Helper Diagnostics Namespace

The repository contains a dormant, test-only foundation for a future optional
helper diagnostics namespace. It is not connected to the VaultSync app product
flow or to the installed helper runtime. It starts no listener, makes no network
call, changes no Syncthing configuration, and does not automatically create a
folder or namespace. No production service currently writes these artifacts.

The repository also contains dormant upload-attestation and response/cleanup
implementations for tests. Request and response payloads are each exactly 256
random bytes; operation IDs, nonces, digests, signatures, bindings, epochs, and
message bytes exist only in test memory and the already disclosed authenticated
namespace artifacts. Authenticated cleanup targets only exact message digests
and cannot erase backups, versions, remote history, conflict copies, or
tombstones.

The helper foundation has no listener, logging, telemetry, crash annotation,
support-bundle export, operation database, Cloud Relay/APNs/StoreKit call, or
product entry point. Swift acceptance is test-only. A local E2E uses only
temporary Syncthing homes/folders and disables discovery, Relay, NAT, upgrades,
usage reporting, and crash reporting; no production service receives its data.
The synchronized copy of a helper attestation is control data and cannot become
upload evidence without the exact pinned local-channel response for the active
query.

The response foundation creates no app download or roundtrip evidence; no
response has been accepted after a fresh local apply on an iPhone.

Before a future supported installer could create the namespace, the operator
would have to choose an exact, existing Syncthing folder subdirectory and give
explicit consent. The visible root name is always **VaultSync Diagnostics**.
The folder and its protocol files would be visible like other synchronized
files in Obsidian, Apple Files or another file browser, on every configured
Syncthing peer, and in filesystem backups. They could also appear in Syncthing
versioning (including `.stversions`), remote file history, conflict copies,
deletion records, or tombstones.

The namespace is limited to fixed protocol filenames containing random
installation and operation identifiers, public-key identifiers, signatures,
hashes, counters, expiry times, and fixed status values. It must never contain
note content, user-derived filenames, vault or folder names, local display
labels, Syncthing credentials, helper credentials, or Cloud Relay contract
data. Local display labels remain on the device. Helper credentials and local
authorization state remain in a separate, non-synchronized helper state store.
Nothing in this namespace is sent to Cloud Relay.

Expiry and bounded cleanup apply only to the exact authenticated live operation
artifacts owned by the helper. Cleanup does not erase the namespace root,
README, manifests, authorization records, credentials, backup copies, versioned
copies, conflict copies, remote history, or tombstones. Backups, `.stversions`,
remote history, and tombstones can retain diagnostics artifacts beyond their
live expiry or cleanup time according to the operator's Syncthing and backup
policies. Disabling or rolling back diagnostics stops future activity but does
not promise deletion from the live folder, peers, backups, versions, history,
or tombstones. Those copies require deliberate operator removal under the
policies of each system that retains them.

Only an explicit Docker host bind mount to an exact existing subdirectory is
within the currently tested M4 scope. Docker named volumes, NAS packages,
macOS packaging, and Windows packaging are not supported for this diagnostics
namespace unless their isolation and rollback are separately proven.

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

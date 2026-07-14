# Privacy Policy

**Effective date:** July 14, 2026

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

### Optional Helper Diagnostics Runtime

The source tree contains an optional local helper runtime for the authenticated
diagnostics protocol. It is not yet published or deployed, and the released app
does not call it. Installing or upgrading through the ordinary helper installers
does not activate it. The runtime starts only when an operator separately
supplies both a read-only diagnostics configuration and a writable private state
directory; otherwise it creates no listener, credential, mapping, namespace, or
artifact.
The supported runtime accepts only a private owner-only, single-link config file
and rejects activation on non-Linux binaries.

When explicitly configured, the runtime listens only on the exact
operator-selected local/LAN/VPN address, requires TLS 1.3 with an out-of-band
SPKI pin, and requires exact application signatures. Pairing uses a one-time QR
secret shown only to the local operator. The read-only operator configuration
contains the selected local folder ID and a fixed mount alias. The helper stores
its signing and TLS private keys, a digest of that folder ID, opaque
homeserver/folder bindings, authorized app public keys, epochs, revocations, and
the alias mapping in the separate non-synchronized state directory. It stores no
raw host folder path, app private key, note content, folder/vault name, user
filename, operation payload, or operation-proof history there.

The runtime's fixed endpoints can process upload-attestation and
response/cleanup messages only after explicit pairing and namespace
authorization. Request and response payloads are each exactly 256 random bytes.
Operation IDs, nonces, digests, signatures, bindings, epochs, and message bytes
remain in memory and the authenticated namespace artifacts; there is no
operation database. Authenticated cleanup targets only exact message digests
and cannot erase backups, versions, remote history, conflict copies, or
tombstones. A synchronized helper attestation is control data and cannot become
upload evidence without the exact pinned local-channel response for the active
query.

The supported host installer reads the canonical folder path only for its
explicit one-shot mutation and runtime bind. It verifies the pre-bind source
device/inode inside the one-shot container, then derives an ephemeral SHA-256
binding over folder ID, canonical path, fixed alias, and namespace device/inode.
Only that digest enters the long-running container environment; neither it nor
the raw path enters protocol messages or logs. The runtime also pins the local
Syncthing Device ID across each preflight. The supported package accepts only an
exact loopback HTTP Syncthing API endpoint and rejects HTTP redirects from
Syncthing, Relay, and the local operator channel.
The folder and expanded-ignore responses used for that preflight are bounded by
fixed byte, entry-count, and pattern-length limits.

All remote and local diagnostics mutations are serialized by one protected
cross-process lock. Crash completion is forward-only and stores no pending
signed enablement body: an explicit rerun may resume only a root already bound
by the protected root record, current helper key/epoch, exact digest/signature,
fixed layout, source identity, and fresh local Syncthing checks. It creates
nothing in recovery mode and cannot adopt an unregistered or conflicting root.

The helper logs no pairing QR, secret, key or pin bytes, Device/folder ID or
digest, opaque binding, nonce, transcript fingerprint, signed body, namespace
path, mount alias, operation value, or artifact name. It creates no diagnostics
telemetry, crash annotation, support-bundle export, Cloud Relay/APNs/StoreKit
call, discovery request, trust adoption, share, rescan, or Syncthing
configuration/ignore change. No response has been accepted after a fresh local
apply on an iPhone; product upload, download, and roundtrip evidence remain
unset.

Before the supported installer creates the namespace, the app must send a valid
signed enablement and the local operator must choose an exact existing Syncthing
folder and give explicit consent. The visible root name is always
**VaultSync Diagnostics**. Before mutation, the installer displays the exact
resulting path and requires a separate acknowledgement that the operator accepts
possible copies in peers, backups, versions, conflicts, and tombstones.
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

Every app installation has an independently paired stable identifier and its
own immutable authorization chain. A separately paired installation can join an
already authenticated namespace only after its own signed authorization; no
trust or identity is inherited. Revoking an app leaves its immutable signed
history in the synchronized namespace. Later helper-key manifests can advance
the namespace for another active installation without rewriting those historical
records, and the active installation must still add a fresh signed authorization
epoch before operations resume. Before a namespace-wide helper manifest is
written, the helper repeats the pinned Device ID, folder/ignore, root, and exact
mount-binding preflight for every affected namespace.

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

Only rootful Docker Engine on an explicitly confirmed standard Linux host, with
an exact host bind of the existing namespace subdirectory, is supported for this
diagnostics runtime. Its container root is read-only; all capabilities are
dropped; it runs as the exact non-root config owner; config is read-only; state
is separate; and the parent vault is absent at runtime. Docker named volumes,
rootless Docker, remote Docker daemons/contexts, non-Unix Docker endpoints, NAS
packages, Docker Desktop, WSL, remote/NAS/FUSE filesystems, Linux binary/systemd
installs, macOS packaging, and Windows packaging remain unsupported unless their
isolation and rollback are separately proven.

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

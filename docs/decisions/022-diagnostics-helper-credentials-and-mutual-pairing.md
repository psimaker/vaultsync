# 022 — Diagnostics helper credentials and mutual pairing

**Status:** Proposed design; not implemented, not independently reviewed, and not approved for runtime use. Human security and product approval is required. This decision authorizes no endpoint, key, pairing record, namespace, probe, Relay change, installer permission change, or rollout.

## Scope and hard invariants

This decision supplies the credential and pairing milestone required by [Decision 021](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md). It applies only to a future diagnostics capability. Existing Syncthing pairing, Trigger v1, Cloud Relay provisioning/status, APNs, StoreKit, folder mappings, and the published helper remain unchanged.

- A Syncthing Device ID is a local binding input, not a helper credential.
- StoreKit JWS, APNs tokens, Relay registrations, API keys, and possession of a synchronized folder cannot bootstrap helper trust.
- Pairing is explicit, mutually authenticated, scoped to one app installation, one homeserver, and selected folders, and is never inferred or repeated automatically.
- An old, unpaired, unreachable, downgraded, or revoked helper yields `capability unavailable` or `re-pair required`; it never creates a general sync error or weaker proof.
- Pairing alone creates no synchronized namespace and no upload, download, or roundtrip evidence.

## Cryptographic suite

The first protocol version has one suite and no algorithm negotiation. An unknown suite or version fails closed.

| Purpose | Proposed primitive | Rule |
|---|---|---|
| App request signatures | Ed25519 | One random 32-byte seed per app installation; never reused for TLS, Syncthing, Relay, or StoreKit. |
| Helper protocol signatures | Ed25519 | One random 32-byte seed per helper installation; separate from the app and TLS keys. |
| Pairing TLS identity | ECDSA P-256 certificate key | TLS 1.3 only; the app pins the SHA-256 digest of the DER SubjectPublicKeyInfo delivered out of band. Certificate renewal may keep the pinned key; key rotation follows the explicit rotation flow below. |
| One-time pairing proof | HMAC-SHA-256 | A cryptographically random 32-byte secret, single use, five-minute maximum lifetime. |
| Digests and key IDs | SHA-256 | Full 32-byte output; no truncation for protocol identifiers. |
| Nonces and opaque bindings | CSPRNG | 32 bytes each; never derived from time, identifiers, paths, accounts, or transactions. |

Ed25519 is available in Go's standard library and as `Curve25519.Signing` in CryptoKit. The iOS key is stored as opaque generic-password data because CryptoKit's Curve25519 signing key is not a native `SecKey`. The design follows [RFC 8032](https://www.rfc-editor.org/info/rfc8032), [TLS 1.3](https://www.rfc-editor.org/info/rfc8446), and Apple's [CryptoKit Keychain guidance](https://developer.apple.com/documentation/cryptokit/storing-cryptokit-keys-in-the-keychain).

## Canonical pairing encoding and signature domains

Pairing and credential-lifecycle messages use the RFC 8949 core deterministic CBOR encoding. The accepted subset is definite-length maps with unsigned-integer labels, unsigned integers, byte strings, and restricted ASCII text constants. Floats, tags, indefinite lengths, duplicate keys, non-shortest integers, unknown fields, invalid UTF-8, and non-deterministic map order are rejected before signature verification. A decoder re-encodes and byte-compares the accepted body.

Every signature input is the exact ASCII domain including its trailing NUL byte, followed by the deterministic CBOR body without the signature field:

| Message | Signature domain |
|---|---|
| App pairing request | `eu.vaultsync.helper-pairing/v1/app-request\0` |
| Helper pairing acceptance | `eu.vaultsync.helper-pairing/v1/helper-accept\0` |
| App-key rotation | `eu.vaultsync.helper-pairing/v1/app-key-rotation\0` |
| Helper-key or TLS-pin rotation | `eu.vaultsync.helper-pairing/v1/helper-key-rotation\0` |
| Revocation | `eu.vaultsync.helper-pairing/v1/revocation\0` |

HMAC uses the separate domain `eu.vaultsync.helper-pairing/v1/bootstrap-hmac\0` followed by the same app-request body. A valid signature from one domain is invalid in every other domain.

## Credential storage

### iOS app

- Store the Ed25519 seed and pairing records under a diagnostics-specific Keychain service, not the existing APNs helper API.
- Use `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`, `kSecAttrSynchronizable = false`, and no shared access group. A protected-data lock makes diagnostics unavailable/interrupted rather than weakening accessibility.
- Store only key material, public-key IDs, opaque homeserver/folder bindings, pairing and rotation epochs, the locally selected Device/folder mapping, endpoint and TLS pin, and revocation state. Store no folder/vault names, paths, operation values, or proof history.
- Keep a random installation marker in app-owned protected storage. If the app container is lost but a Keychain item survives reinstall, do not silently reuse it; show an explicit recover/revoke/re-pair choice.
- `ThisDeviceOnly` means device replacement and restore to another device require a new pairing. There is no iCloud Keychain sync or Cloud Relay escrow.

### Helper

- Introduce a dedicated diagnostics state directory or Docker volume, separate from `config.xml`, the synchronized folder, and logs. Directory mode is `0700`; files are `0600`; writes are create-new or atomic temp-file/fsync/rename operations.
- Store separate helper-signing and TLS private keys, opaque binding records, authorized app public keys and scopes, monotonically increasing epochs, revocation tombstones, and locally trusted folder-ID-to-mount aliases. Never store an app private key or pairing secret after bootstrap.
- Docker uses a dedicated state volume and a read-only container root. Host/NAS/macOS/Windows packages use an OS account and ACL that deny other non-administrator users. A platform that cannot provide this isolation is unsupported for the capability.
- An operator may explicitly back up the complete helper credential state in an encrypted, access-controlled backup. There is no automatic backup, cloud escrow, or partial key export. Restoring that state is recovery of the same helper identity, not a new pairing.
- Old helpers ignore the new state. Upgrade, downgrade, or app rollback never auto-deletes credentials or the future namespace root.

## Homeserver and folder bindings

At helper initialization, the helper creates a random 32-byte homeserver binding and pins it locally to the full Device ID read from its trusted Syncthing API. Every authorized folder receives an independent random 32-byte folder binding pinned to the local Syncthing folder ID and an operator-approved diagnostics mount alias.

The pairing QR carries domain-separated SHA-256 digests of the normalized Device ID and folder ID, not their raw values. The app computes the same digests from its already-local mapping and refuses a mismatch. After pairing, both sides store the full local mapping privately but protocol artifacts carry only the random bindings and key IDs.

Before capability acceptance and before every evidence transition:

1. the helper's current Device ID must equal its pinned local value;
2. the app's selected homeserver/folder mapping must equal its paired record;
3. the folder binding must remain authorized to that app key and helper key epoch; and
4. the locally configured mount must still resolve to the operator-approved binding.

A path, folder ID, Device ID, or binding supplied by a network request never selects a filesystem target. A binding is only a lookup key into local trusted state.

## Explicit trust bootstrap

Pairing is disabled by default. The helper exposes no diagnostics listener until the operator configures an explicit local/LAN or VPN endpoint and starts a local privileged pairing command for one existing Syncthing folder ID.

1. The local command verifies the helper state, current Device ID, selected folder ID, and future access preconditions without creating a namespace. It creates one in-memory pending record, a 32-byte one-time secret, helper/app transcript nonce, random homeserver/folder bindings, and a five-minute monotonic deadline.
2. It displays a QR containing the exact capability/version, fixed HTTPS endpoint, P-256 TLS SPKI pin, helper Ed25519 public key, binding values, Device/folder ID digests, nonce, expiry, and one-time secret. The QR is sensitive bootstrap material; it is never logged, persisted, copied to Cloud Relay, or placed in the synchronized folder.
3. The app user selects the already-known homeserver/folder, scans the QR, verifies both identifier digests, creates its per-installation Ed25519 key, and connects with TLS 1.3 while pinning the exact SPKI. DNS or public-CA trust alone is insufficient.
4. The app sends a deterministic CBOR request containing both public identities, bindings, transcript nonce, app nonce, requested folder scope, issued/expiry times, HMAC, and app signature. The endpoint path is fixed and contains no identifier.
5. The helper verifies the TLS session, HMAC, signature, exact pending record, scope, nonce, expiry, and unused secret; consumes the secret before committing; then writes the authorization record atomically.
6. The helper returns a signed acceptance binding the complete request digest, helper/app keys, bindings, epochs, and a fresh helper nonce. The app verifies it and stores its record atomically.
7. Both sides display the same 12-hex-character transcript fingerprint. The user confirms the match before the app enables authenticated capability discovery. A mismatch revokes the incomplete record locally; no namespace exists to clean.

At most one pending pairing per folder and four per helper may exist. Restart, timeout, cancellation, a second use, or any validation failure destroys only the in-memory pending secret and produces no pairing.

No mDNS, UPnP, Cloud Relay tunnel, unauthenticated synchronized file, StoreKit transaction, or Syncthing TLS key participates in bootstrap. Remote access requires an operator-controlled VPN or separately reviewed reverse-proxy configuration; the standard installer opens no public port.

## Mutual authentication after pairing

- Every app request carries the app key ID, helper key ID, homeserver/folder bindings, pairing epoch, protocol version, fresh request nonce, issue/expiry bounds, and an app signature.
- Every helper response covers the complete request digest plus the same bindings/epochs and has its own nonce and helper signature.
- TLS protects transport and the pinned SPKI prevents endpoint substitution; application signatures remain authoritative and survive proxy/library behavior. HTTP status is transport diagnostics only.
- Authenticated capability discovery is additive. A pre-pairing version string can say only that pairing might be supported; it cannot authorize an operation.
- Fixed endpoint paths and disabled body/access logging prevent operation or binding values from entering server or reverse-proxy logs.

## Multiple app installations

Every iPhone/iPad installation has a distinct app key, pairing epoch, folder scopes, quotas, and revocation state. The helper never copies authorization from one installation to another. Adding another installation requires a new one-time pairing and creates a separate immutable authorization record under the same authenticated helper/folder ownership. Evidence and cleanup for one app key never upgrade or delete another app's operation.

The helper UI/CLI identifies installations only by a user-confirmed local label stored outside protocol artifacts plus a short public-key fingerprint. Labels never enter signed artifacts, logs, Cloud Relay, or the synchronized namespace.

## Rotation, revocation, recovery, and loss

| Event | Required behavior |
|---|---|
| App key rotation with old key available | App creates a new Ed25519 key; an old-key-signed rotation binds the new public key and incremented epoch; helper acceptance is signed. Commit both records atomically, then require explicit user confirmation before retiring the old key. |
| App key lost or app moved to another device | No automatic recovery. Pair again as a new installation, then explicitly revoke the lost key from the helper. |
| Helper signing key rotation while old key is trusted | Helper cross-signs the new key and incremented epoch with both old and new keys; every app explicitly confirms. Until confirmed, capability is unavailable for that app. |
| Suspected helper-key compromise or helper state loss | Cross-signing is insufficient. Generate a new helper identity and explicitly re-pair every installation/folder. Old credentials and namespace content are never adopted. |
| TLS certificate renewal | Allowed with the same pinned P-256 key. A new TLS key requires a helper-key-signed pin rotation plus explicit app confirmation, or a new pairing if compromise is suspected. |
| App revocation | A signed app request or local helper-admin command marks the app key revoked and increments the authorization epoch. No new operations/evidence are accepted. |
| Folder unauthorization | Remove the binding from the app scope after bounded authenticated cleanup. Do not remove the Syncthing share, folder mapping, credentials for other folders, or namespace root. |
| App/helper downgrade | Preserve credentials. The capability becomes unavailable/dormant; no automatic re-pair, deletion, or trust conversion occurs. |

Revoked public keys and epochs remain as minimal tombstones for at least the maximum artifact TTL plus 24 hours so stale files can be rejected and authenticated cleanup can finish. Private-key retirement or credential deletion is always an explicit security action, never an upgrade/rollback side effect.

## Replay and downgrade defense

- Pairing secrets and nonces are single use; all messages bind protocol/suite, message domain, both public-key IDs, both opaque bindings, both epochs, issue/expiry bounds, and the prior transcript/request digest.
- The app and helper accept only the current locally stored epoch and exact active request. Old, copied, cross-folder, cross-homeserver, cross-installation, and out-of-order messages fail closed.
- Maximum clock skew is defined by the canonical contract; a local monotonic deadline is always at least as strict as wall-clock expiry.
- Unknown algorithms, versions, mandatory fields, or a lower advertised capability than the pinned minimum produce `capability unavailable`/`unsupported`, never fallback pairing.
- The app never treats Syncthing peer authentication as a helper signature, and the helper never exports or reuses Syncthing's TLS private key.

## Compatibility and rollback matrix

| App | Helper | Credential behavior |
|---|---|---|
| Existing app | Existing helper | No diagnostics credential, endpoint, pairing, or namespace. Trigger v1 unchanged. |
| Existing app | New dormant helper | Helper keys may exist only after explicit operator setup; no app authorization or namespace is created automatically. Existing behavior is unchanged. |
| New app | Existing helper | `capability unavailable`; passive local-progress evidence remains available. No pairing attempt mutates existing setup. |
| New app | New unpaired helper | Pairing is offered only after explicit local bootstrap. Until completed, no probe or general error. |
| New paired app | New paired helper | Only exact authorized folder bindings may negotiate the later contract. Pairing itself proves no transfer. |
| App downgrade | New helper | Helper stays dormant for that installation; credentials and namespace root remain; bounded expiry cleanup only. |
| New app | Helper downgrade | Capability becomes unavailable; app starts no operations and never converts old artifacts/timestamps into proof. |

## Privacy and operational logging

Logs, telemetry, crash reports, support bundles, and Cloud Relay must not contain private/public key bytes, pairing secrets, QR payloads, TLS pins, Device/folder identifiers or digests, opaque bindings, nonces, transcript fingerprints, signed bodies, paths, or credential records. Allowed logs are fixed pairing state categories, protocol major version, bounded counts, coarse durations, and fixed remediation actions.

Pairing data travels only over the pinned local HTTPS channel and local visual bootstrap. Cloud Relay receives no capability request, credential, correlation, hash, result, or cleanup state. `PRIVACY.md` must be updated before any runtime credential or pairing transport exists.

## Required tests before implementation approval

- RFC 8032 vectors and cross-language Go/CryptoKit sign/verify fixtures for every signature domain.
- Deterministic-CBOR golden vectors plus rejection of duplicates, non-shortest forms, reordered maps, unknown fields, malformed lengths, truncation, and arbitrary bytes.
- Wrong/rotated/revoked keys, wrong helper/app/binding/epoch, replay, duplicate, expiry/skew, out-of-order, QR reuse, and first-request races.
- TLS pin mismatch, certificate renewal, TLS-key rotation, endpoint substitution, proxy/body logging, unavailable endpoint, and LAN/VPN loss.
- Multiple installations/folders/homeservers, independent revocation/rotation, app reinstall with orphaned Keychain state, helper restore, helper loss, and both downgrades.
- Keychain accessibility/backup migration tests and helper state permission/atomicity/crash tests on Docker, Linux host/NAS, macOS, and Windows.
- Privacy snapshots proving every forbidden value is absent from logs, persistence outside approved credential stores, Cloud Relay, Trigger v1, and crash annotations.
- Property/fuzz tests for transcripts, epoch transitions, authorization lookup, and arbitrary message ordering.

## Human approval required

Review must explicitly approve or reject each of these choices before implementation:

1. Ed25519 application signatures, separate P-256 TLS identity, HMAC-SHA-256 bootstrap, and deterministic CBOR domains.
2. `WhenUnlockedThisDeviceOnly` app storage and a non-synchronizing, non-migrating per-installation key.
3. A dedicated helper state store with optional operator-controlled encrypted whole-state backup and no escrow.
4. Explicit QR + pinned-TLS LAN/VPN bootstrap, with no automatic discovery, public listener, Relay tunnel, or TOFU.
5. Per-installation/per-folder authorization, explicit rotation/revocation, and re-pair-only recovery after key loss or suspected compromise.
6. The downgrade rule that preserves credentials and only makes the capability unavailable/dormant.

Approval of this document still does not approve a namespace, access widening, probe, canonical operation contract, helper rollout, or app implementation.

## Result

The proposed design can attribute future messages to one explicitly paired app installation and helper while binding them to the locally known homeserver and folder. It intentionally chooses fail-closed re-pairing over silent recovery and keeps Cloud Relay and Trigger v1 outside the trust path. Until human review accepts it and the separate namespace/access and canonical-contract gates are also accepted, Decision 021 remains blocked.

## Links

- [Decision 019 — Relay evidence stays layered](019-relay-proof-hierarchy.md)
- [Decision 020 — Sync-path proof requires correlated evidence](020-sync-path-proof-requires-correlated-evidence.md)
- [Decision 021 — Capability-negotiated helper contract](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md)

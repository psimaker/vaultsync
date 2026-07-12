# 023 — Diagnostics namespace and least-privilege access

**Status:** Proposed design; not implemented, not independently reviewed, and not approved for runtime use. Human security and product approval is required. This decision creates no directory, manifest, mount, ACL, artifact, tombstone, credential, endpoint, installer change, or rollout.

## Scope and invariants

This decision supplies the namespace/access milestone required by [Decision 021](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md). It applies only after separate mutual pairing and canonical-contract decisions are approved. Trigger v1, Cloud Relay, APNs, StoreKit, Syncthing folder configuration, existing paths, mappings, ignores, versioning, backups, and user data remain unchanged.

- The exact visible namespace name is **`VaultSync Diagnostics`** at the root of one explicitly selected Syncthing folder.
- Namespace enablement is a separate, explicit app-and-operator action. App/helper upgrade, pairing, launch, onboarding, silent push, background execution, or Relay activity never creates it.
- A pre-existing unauthenticated path with that exact name is a collision. It is never adopted, renamed, merged, overwritten, deleted, or bypassed with a suffix.
- Runtime helper access is confined to that exact authenticated namespace. Mounting or granting read/write access to the complete vault is not least privilege and is rejected.
- Network data selects only opaque bindings already mapped in local trusted configuration. It never supplies a path, folder ID, relative component, mount, share, or filename.
- No automatic action changes a Syncthing folder mode, share, pause state, ignore rules, versioning, backup policy, ownership, or user file.
- The namespace root and persistent ownership records are never automatically deleted, including during revocation, uninstall, app/helper downgrade, cleanup, or rollback.

## User-visible product contract

`VaultSync Diagnostics` is deliberately visible in Files, Obsidian, Syncthing peers, backup tools, and version history. It is not a hidden dot-directory and the design never claims that another client will hide it. Before enablement, the app must explain in all four supported languages:

- the folder is app-owned diagnostics infrastructure, not a user vault;
- it contains only opaque random protocol data, public-key identifiers, signatures, hashes, and expiry metadata, never note content or user-derived filenames;
- temporary files and deletion tombstones synchronize to peers;
- backups and Syncthing versioning may retain opaque artifacts after live cleanup; and
- disabling diagnostics stops new operations but does not automatically delete the root, credentials, backups, versions, or tombstones.

The consent screen names the exact homeserver and vault locally for the user's decision, but those labels never enter artifacts, helper logs, or Cloud Relay. Dismissing or declining creates nothing.

## Ownership and collision model

### Initial creation

The uniform first-version flow is helper-side, because Docker can safely mount an exact existing subdirectory but must not mount the parent vault read/write at runtime.

1. Mutual pairing and folder authorization already exist; no namespace exists.
2. The app user explicitly requests enablement for one local homeserver/folder binding. The app sends the short-lived signed enablement request over the paired local channel; the helper holds only that exact pending request in memory for the local operator step. It is permission to begin, not a filesystem path.
3. The operator runs a local privileged installer/CLI command and selects the already-configured Syncthing folder by local folder ID. The command resolves its path only from local trusted Syncthing configuration, shows the exact local path for confirmation, and rejects symlinks, missing markers, path changes, or an existing `VaultSync Diagnostics` entry.
4. The local command atomically creates exactly that directory, its fixed layout, and a helper-signed root manifest. It applies the dedicated account/ACL or prepares the exact subdirectory mount, then drops parent access before the helper runtime starts.
5. The app sees the manifest arrive through Syncthing, validates the paired helper key, homeserver/folder binding, namespace ID, and fresh enablement digest, then sends a signed installation-authorization candidate over the paired local channel. The helper validates and countersigns that exact body and exclusively creates the immutable record. The app accepts it only after it arrives through Syncthing and both signatures validate. No operation may start before both sides validate ownership.

The privileged creation step is a narrowly scoped installer mutation and requires its own review, dry-run, path-confinement tests, and user confirmation. It is not part of normal helper runtime. If exact parent-path resolution or permission narrowing cannot be proven, enablement fails as `unsupported` and Trigger v1 continues.

### Existing authenticated root and multiple installations

A second app installation may join only after separately pairing with the same helper/folder and validating the existing helper-signed root manifest. This is not adoption of arbitrary user data: the root must authenticate to the already-paired helper key and exact binding. After explicit user authorization, the helper creates an immutable app-and-helper-signed authorization record for that installation. Invalid, missing, copied, stale, conflicting, or differently bound ownership is a collision and remains untouched.

At most eight app installations may be authorized per folder in the first version. Removing one authorization does not modify another installation, the root, the Syncthing share, or user data.

## Fixed namespace layout

All names below are protocol constants. Installation and operation components are lowercase unpadded base32 encodings of already-validated 32-byte bindings/IDs. Epoch components are canonical base-10 ASCII without a sign or leading zero. Callers never pass raw path strings.

```text
VaultSync Diagnostics/
├── README.txt
├── root-manifest.cbor
├── manifest-epochs/
│   └── <epoch>.helper-manifest.cbor
└── installations/
    └── <installation-binding>/
        ├── authorization.cbor
        ├── authorization-epochs/
        │   └── <authorization-epoch>.authorization.cbor
        └── operations/
            ├── <operation-id>.request.cbor
            ├── <operation-id>.attestation.cbor
            └── <operation-id>.response.cbor
```

- `README.txt` is fixed, non-executable, multilingual explanatory text with no identifiers or generated values.
- `root-manifest.cbor` is create-once and binds capability/version, random namespace ID, opaque homeserver/folder bindings, helper key ID/epoch, created time, and helper signature. It contains no Device ID, folder/vault name, path, account, StoreKit, APNs, or Relay value.
- Helper-key rotation appends an immutable signed epoch manifest; it never overwrites the root or prior epoch. Epoch count is capped at eight; exceeding it requires explicit re-pair/re-enable review rather than unbounded files.
- A stable installation binding is domain-separated from the installation's namespace-initial app key plus the random homeserver/folder bindings; it never changes during app/helper key rotation and is never accepted from a request without recomputing it from local pairing state.
- `authorization.cbor` binds the initial app/helper keys and epochs, namespace/root, installation binding, and current credential-state digest with both app and helper signatures. Rotation appends a doubly signed immutable authorization-epoch record linked to the previous record; it never renames the installation or overwrites authorization.
- Authorization records contain no user label. At most eight epoch records exist per installation; exhaustion makes the capability unavailable until an explicitly reviewed re-pair/re-enable flow, never compaction or overwrite.
- Operation files are immutable create-once artifacts. Their exact canonical schemas and evidence meaning belong to the separate contract decision.

No executable, script, note, attachment, arbitrary extension, nested user-selected directory, symlink, hard link, reparse point, socket, device, FIFO, or sparse/unbounded file is valid protocol content.

## Canonical ownership records

Ownership records use the [RFC 8949](https://www.rfc-editor.org/info/rfc8949) core deterministic CBOR subset: definite maps, unsigned-integer labels/values, exact byte strings, and the one exact ASCII capability text. Duplicate/unknown keys, non-shortest integers, indefinite items, tags, floats, invalid UTF-8, non-deterministic key order, trailing bytes, nesting deeper than four, more than 32 map entries, or an encoded/decoded size over 16 KiB are rejected before signature processing. Accepted input is re-encoded and must byte-equal the original.

Ed25519 public keys/signatures and SHA-256 use the credential decision's exact suite. A key ID is `SHA-256("eu.vaultsync.key-id/ed25519/v1\0" || raw_public_key)`. The stable installation binding is `SHA-256("eu.vaultsync.namespace/installation/v1\0" || namespace_initial_app_key_id || homeserver_binding || folder_binding)`. All inputs are exactly 32 bytes. The namespace-initial ID is the current paired app key when this installation receives its first namespace authorization; the helper preserves and recomputes it from protected local state before mapping it to a directory.

Every signature covers its exact ASCII domain including the trailing NUL plus the deterministic map as specified below:

| Signature | Domain |
|---|---|
| Namespace enablement request by app | `eu.vaultsync.namespace/v1/enablement-request\0` |
| Root manifest by helper | `eu.vaultsync.namespace/v1/root-manifest\0` |
| Helper epoch manifest by prior helper key | `eu.vaultsync.namespace/v1/helper-epoch-prior\0` |
| Helper epoch manifest by current helper key | `eu.vaultsync.namespace/v1/helper-epoch-current\0` |
| Initial authorization by app | `eu.vaultsync.namespace/v1/authorization-initial-app\0` |
| Initial authorization by helper | `eu.vaultsync.namespace/v1/authorization-initial-helper\0` |
| Authorization epoch by app | `eu.vaultsync.namespace/v1/authorization-epoch-app\0` |
| Authorization epoch by helper | `eu.vaultsync.namespace/v1/authorization-epoch-helper\0` |

The exact field registry is:

| Label | Field | Type and length |
|---:|---|---|
| `1` | capability | exact ASCII text `eu.vaultsync.diagnostics.namespace/1` |
| `2`, `3` | protocol, suite | uint, exactly `1` |
| `4` | message_type | uint: `1=enablement`, `2=root`, `3=helper_epoch`, `4=initial_authorization`, `5=authorization_epoch` |
| `5`, `6` | homeserver_binding, folder_binding | bstr, 32 bytes each |
| `7` | namespace_id | random bstr, 32 bytes |
| `8` | installation_binding | derived bstr, 32 bytes |
| `9` | namespace_initial_app_key_id | bstr, 32 bytes |
| `10`, `11` | current_app_public_key, current_app_key_id | bstr, 32 bytes each |
| `12` | current_app_epoch | uint |
| `13`, `14` | current_helper_public_key, current_helper_key_id | bstr, 32 bytes each |
| `15` | current_helper_epoch | uint |
| `16`, `17` | prior_helper_public_key, prior_helper_key_id | bstr, 32 bytes each |
| `18` | prior_helper_epoch | uint |
| `19` | enablement_nonce | random bstr, 32 bytes |
| `20` | enablement_request_digest | SHA-256 record digest, 32 bytes |
| `21` | root_manifest_digest | SHA-256 record digest, 32 bytes |
| `22` | prior_helper_manifest_digest | SHA-256 record digest, 32 bytes |
| `23` | current_helper_manifest_digest | SHA-256 record digest, 32 bytes |
| `24` | prior_authorization_digest | SHA-256 record digest, 32 bytes |
| `25` | current_credential_state_digest | SHA-256 credential-state digest, 32 bytes |
| `26`, `27` | issued_at, expires_at | uint Unix seconds |
| `28` | created_at | uint Unix seconds |
| `29` | readme_digest | SHA-256 over the exact fixed `README.txt` bytes, 32 bytes |
| `30` | authorization_nonce | random bstr, 32 bytes |
| `31` | authorization_epoch | uint; initial value `1` |
| `253` | app_signature | Ed25519 signature, 64 bytes |
| `254` | prior_helper_signature | Ed25519 signature, 64 bytes |
| `255` | helper_signature | Ed25519 signature, 64 bytes |

Type `1` contains `1`–`6`, `9`–`15`, `19`, `26`, `27`, and `253`. Type `2` contains `1`–`7`, `13`–`15`, `19`, `20`, `28`, `29`, and `255`. Type `3` contains `1`–`7`, `13`–`18`, `21`, `22`, `28`, `29`, `254`, and `255`. Type `4` contains `1`–`15`, `21`, `23`, `25`–`27`, `30`, `31`, `253`, and `255`. Type `5` contains the type-`4` fields plus `24`. Every other field is forbidden for that type.

The app signature is over its app domain and the body with labels `253`–`255` omitted. For authorization, the helper verifies that signature, inserts it as label `253`, and signs its helper domain plus the map with only label `255` omitted. The helper creates the resulting two-signature file once; neither side edits it. For an epoch manifest, the prior helper signs the body with `254` and `255` omitted; the current helper then signs the current-helper domain plus the map containing label `254` but omitting `255`. For a root manifest, the helper signs the root domain plus the body with label `255` omitted.

A record digest is `SHA-256("eu.vaultsync.namespace/v1/record-digest\0" || complete_canonical_record)`, including all signatures. The root binds the digest of the exact short-lived app enablement request. An epoch manifest binds the immutable root and the immediately prior root/epoch record; its current helper epoch is exactly prior epoch plus one. Authorization binds the root, the current root/epoch record, current credential state, and—after rotation or another credential-state transition—the immediately prior authorization record. Type `1` and initial authorization require `namespace_initial_app_key_id == current_app_key_id`; initial authorization uses authorization epoch `1`. Later records preserve the namespace-initial ID and increment authorization epoch by exactly one. The current helper manifest is the root digest before helper rotation and the latest valid epoch-manifest digest afterward. Initial/current key IDs, key epochs, authorization epoch, signatures, and locally recomputed installation binding must all agree with paired state.

Enablement and authorization candidates have `0 < expires_at - issued_at <= 300` seconds, a local 300-second monotonic deadline, and at most ±120 seconds wall-clock skew. The helper must countersign/create before that deadline. Once created, ownership and authorization records are persistent history; timestamps never prove sync and expiry never authorizes deletion or adoption. A copied, truncated, reordered, stale, forked, incorrectly countersigned, or non-current chain is collision/unsupported and remains untouched.

The paired local channel uses only `POST /api/v1/diagnostics/namespace/enablement` and `POST /api/v1/diagnostics/namespace/authorization`, with `application/cbor` bodies capped at 16 KiB. Identifiers never appear in paths, queries, headers, certificates, or access logs; redirects, compression, raw-body logging, and Cloud Relay calls are forbidden. HTTP status is transport state, not ownership. At most one pending request exists per app/helper/folder tuple, three enablement starts per day per folder, ten attempts per pending request, and 30 namespace-control requests per minute helper-wide. Expiry, cancellation, restart, validation failure, or limit exhaustion drops only the in-memory pending request and creates no filesystem entry.

## Deployment and mount model

The current helper reads `config.xml` through a read-only mount or runs as the Syncthing/config owner. Neither model alone authorizes diagnostics writes. Capability support is per deployment, and unsupported deployments keep all existing wake-up behavior.

| Deployment | Proposed least-privilege model | First-version status |
|---|---|---|
| Docker with an explicit host folder bind | Local installer creates the exact namespace on the host, then starts the helper with a read-only root filesystem, separate state volume, config read-only, and only the exact namespace bind-mounted read/write through long `--mount` syntax. No parent vault mount, Docker socket, added capability, or privileged mode. | Conditionally supportable after real packaging tests. |
| Docker Compose with a named Syncthing volume | A verified volume-subpath workflow must create the subdirectory in a bounded local installer container, then mount only that existing subpath. Mounting the entire named volume read/write is forbidden. | `unsupported` until supported Docker/Compose versions and rollback are proven. |
| Linux host/systemd | Replace the current Syncthing-owner runtime for diagnostics with a dedicated helper account. POSIX ACL grants traversal only to required parents and read/write only to the exact namespace; config/API access stays separately read-only. | Conditionally supportable after ACL and service-upgrade tests. |
| Synology/QNAP/Unraid or other NAS | Dedicated package/container identity with vendor ACL limited to the exact namespace. The locally trusted installer maps one folder ID to one path and records the opaque binding. | `unsupported` when exact ACLs, path semantics, or upgrade rollback cannot be demonstrated on that NAS. |
| macOS launchd | Current same-user LaunchAgent/LaunchDaemon access is too broad. A future dedicated account plus explicit ACL must expose only config read access, the state directory, and exact namespace. | Current packages remain Trigger-v1-only; diagnostics `unsupported` until packaged and tested. |
| Windows Scheduled Task | Current per-user task inherits broad profile access and is insufficient. A future dedicated service/task identity needs NTFS ACL only on state/config and exact namespace. | Current no-admin package remains Trigger-v1-only; diagnostics `unsupported` until packaged and tested. |

Docker documentation notes that bind mounts are read/write by default and can modify host files; therefore the design uses an exact subdirectory mount and read-only container root, never a broad default bind. See [Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/).

An installer re-run never broadens access automatically. A new helper may advertise that packaging supports diagnostics, but remains dormant until the explicit per-folder enablement flow completes. Removing a runtime mount/ACL is a separate explicit disable action after bounded cleanup; it does not delete the namespace or credential state.

## Path authority and filesystem confinement

The helper stores `folder_binding -> fixed local mount alias` in protected local state. The runtime opens the mount root once, verifies its device/inode or Windows file identity against local enablement state, and performs every operation relative to that handle.

- Decode identifiers as exact-length bytes, then encode filenames internally. Reject separators, dots, percent escapes, Unicode, alternate data streams, reserved device names, overlong names, and any non-canonical base32.
- Create files and directories with exclusive semantics; never follow or replace an existing entry. Files are regular, non-sparse, single-link, owner/ACL-checked, and within fixed size limits.
- Read, stat, and delete through the already-open directory handle. Re-check file identity and type on the opened handle; do not perform check-then-open pathname sequences.
- Linux uses `openat2` with `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS | RESOLVE_NO_MAGICLINKS` and `O_NOFOLLOW`/`O_EXCL`; a component-wise `openat` fallback is allowed only after equivalent race tests. See [openat2(2)](https://man7.org/linux/man-pages/man2/openat2.2.html).
- macOS uses descriptor-relative `openat`/`fstatat` with `O_NOFOLLOW`, component-by-component identity checks, and no path canonicalization as an authorization decision.
- Windows opens components with `CreateFileW` and `FILE_FLAG_OPEN_REPARSE_POINT`, rejects every reparse point and alternate stream, and verifies the final handle remains beneath the authorized root with `GetFinalPathNameByHandleW` plus volume/file identity. See [CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew).
- A symlink, junction, mount swap, hard-link count greater than one, changed root identity, inaccessible path, or path-resolution feature unavailable yields `unsupported` before start or `interrupted` during an operation.

The helper never reads note files or enumerates outside the fixed namespace. The app never grants an arbitrary security-scoped URL to the helper; it selects only its existing settled folder mapping and constructs the same fixed child name locally.

## Ignore rules

Before enablement, operation start, and evidence acceptance, both app and helper evaluate the exact namespace and all protocol child patterns with Syncthing-compatible ignore semantics for that folder.

- Any matching ignore, unsupported include file, unreadable ignore configuration, or inconsistent result makes the target `unsupported`.
- Neither side edits `.stignore`, adds an exception, triggers a rescan, or temporarily disables ignores.
- An ignore change during an operation interrupts the target and prevents later evidence acceptance.
- A probe or “try and see” write is not an ignore preflight.

The implementation must use Syncthing's own matcher behavior or a regression-pinned equivalent on both platforms; a partial reimplementation is not sufficient.

## Backup and Syncthing versioning

Live TTL and cleanup do not erase backups, snapshots, Syncthing `.stversions`, remote peer versions, or deletion tombstones. The artifacts contain no note content or user-derived metadata, but they do contain opaque random values, public-key IDs, signatures, hashes, and times.

- The product discloses this retention before enablement and in `PRIVACY.md` before runtime rollout.
- The helper/app never disables versioning, changes backup jobs, edits exclude rules, or claims a retention duration they cannot enforce.
- Operators may separately exclude the exact namespace from backups only if that policy still permits Syncthing transfer and is independently documented/tested. Backup exclusion is not Syncthing ignore.
- If the user/product cannot accept opaque retention outside live TTL, the capability remains blocked for that folder/deployment.

## Conflicts, immutable files, and tombstones

- Every operation ID is unique and every artifact is create-once. No shared mutable status file or last-writer-wins pointer exists.
- Any Syncthing conflict copy, duplicate valid-looking file, manifest conflict, unexpected child, partial file, wrong owner/type, or authorization disagreement produces `conflict`/`partial`; no ambiguous artifact sets evidence.
- Conflict copies and unknown entries are never auto-deleted. The app shows a fixed safe manual next step without exposing paths in logs.
- Cleanup deletions produce ordinary Syncthing tombstones and may create versioned copies. Tombstones, deletions, index updates, and cleanup acknowledgements are never proof.
- Helper/app clock or ordering differences cannot choose a winner. Signatures, bindings, immutable names, active operation state, and causal contract decide acceptance.

## Limits and abuse resistance

| Limit | Proposed hard maximum |
|---|---|
| Namespace roots | One exact authenticated root per Syncthing folder. |
| Authorized installations | Eight per folder. |
| Active operations | One per app/homeserver/folder tuple; two per app process; eight per helper. |
| Protocol artifact size | 16 KiB each, checked before allocation and again on the opened handle. |
| Random request/response payload | 256 bytes each. |
| Entries scanned during startup/cleanup | 128 per namespace and 32 per installation; overflow is `cleanup failed`/`conflict`, never broad deletion. |
| Files created per operation | Three immutable files plus existing persistent manifests; no retries create new logical artifacts. |
| Key-rotation manifests | Eight retained immutable epochs. |
| Authorization-epoch records | Eight retained immutable records after the initial authorization. |

The canonical contract sets TTL, skew, rate, retry, and HTTP-body limits. Namespace limits cannot be raised by network input or capability negotiation; a later version requires a new reviewed decision.

## Cleanup and lifecycle

Evidence state and cleanup state remain independent. Cleanup failure never erases or upgrades valid per-leg evidence.

1. The app normally deletes the exact request it created after terminal evidence; the helper deletes the exact attestation/response it created after an authenticated cleanup request.
2. Either side may remove an expired counterpart artifact only after validating the root, installation authorization, signature, binding, operation ID, exact canonical filename/content, TTL, and file identity.
3. Cleanup is idempotent: missing already-deleted files are success; changed or unverified files are untouched and reported as conflict/failure.
4. Roundtrip success, partial, timeout, cancellation, view exit, protected-data loss, engine generation change, helper/app restart, revocation, and disablement all schedule the same bounded cleanup rules.
5. The app never resumes proof after restart. The capable helper performs one bounded authenticated startup scan, handles active expiry timers, and responds to explicit cleanup calls; it does not poll permanently or loop forever.
6. A cleanup attempt uses at most three immediate retries with fixed backoff, then stops as `cleanup failed`. A later explicit app visit or helper restart may make one new bounded attempt.
7. The root, README, root/epoch manifests, valid initial/epoch installation authorizations, credentials, unverified entries, user files, parent folder, and Syncthing `.stfolder` marker are never automatic cleanup targets.

Manual removal of the namespace is an advanced, explicit, offline procedure performed only after diagnostics is disabled, all capable helpers are stopped, credentials/bindings are reviewed, and the user accepts backup/tombstone behavior. It is not part of app rollback or ordinary support remediation.

## Existing users and rollback

- Existing users receive no namespace, mount, ACL, state directory, re-pair, or folder mutation from upgrade alone.
- A capable helper ships dormant and preserves Trigger v1 with old and new apps.
- Helper rollback removes capability availability but does not remove the mount configuration, namespace, credentials, root, or artifacts. The app starts no new operation and shows `capability unavailable`; authenticated artifacts expire and await the next capable bounded cleanup.
- App rollback leaves the capable helper dormant for that app after active expiry cleanup. The old app ignores the visible namespace like any other folder and never adopts it as user data.
- Folder path changes, container migration, ACL loss, and NAS volume moves invalidate the local mount identity. There is no automatic rebase or broad fallback; re-enable explicitly after review.
- Unsupported folders/deployments remain normal syncing folders and never become a general Relay/subscription/app error.

## Privacy and Cloud Relay boundary

Namespace artifacts flow only through the selected Syncthing folder to its existing peers. They never travel through Cloud Relay. The Relay receives no namespace name, manifest, path, mount, operation ID, nonce, hash, key ID, payload, result, cleanup state, conflict, or capability message.

Operational logs, telemetry, crash reports, and support bundles omit folder/Device identifiers, names, paths, mount aliases, namespace/installation bindings, app/helper key IDs, operation values, nonces, hashes, signatures, bodies, entry names, and backup/version data. Allowed logging is fixed state categories, protocol major version, bounded counts, coarse durations, and fixed remediation actions.

## Failure and threat matrix

| Threat/failure | Required safe result |
|---|---|
| User already has `VaultSync Diagnostics` | Collision/unsupported; leave it and every child untouched; no suffix or adoption. |
| Copied valid namespace from another folder/server | Binding/helper/authorization mismatch; unsupported; no cleanup or proof. |
| Symlink, junction, hard link, reparse point, mount swap, traversal | Descriptor/handle checks fail closed before content access; unsupported/interrupted. |
| Network request supplies path-like data | Schema rejection; local binding lookup is the only authority. |
| Container/host/NAS path mismatch | Enablement or mount-identity preflight fails; Trigger v1 remains healthy. |
| Ignore match or ignore semantics unavailable | Unsupported; never edit rules or probe. |
| Conflict copy, partial write, duplicate, unexpected entry | Conflict/partial; never choose, overwrite, or auto-delete. |
| Backup/version/tombstone retention | Disclose opaque retention; never claim TTL deletion outside live tree. |
| Helper compromise within namespace | Runtime account/mount limits damage to app-owned diagnostics artifacts; user notes and parent vault stay inaccessible. |
| Helper/app crash or downgrade | No proof resume; bounded authenticated cleanup only; root/credentials persist. |
| Cleanup entry explosion | Stop at hard scan limits and report cleanup failed; no unbounded loop or broad deletion. |

## Required tests before implementation approval

- Cross-language deterministic-CBOR golden bytes and signature/digest-chain fixtures for enablement, root, helper epochs, initial authorization, authorization epochs, app/helper key rotation, and TLS-pin state changes.
- Namespace absent/create race, pre-existing file/directory, valid and invalid manifests, copied/forked roots, stable installation bindings, multiple installations, exhausted authorization/rotation epochs, and collision UX.
- Linux symlink/magic-link/mount/hard-link races, macOS descriptor-relative races, Windows junction/reparse/alternate-stream cases, and arbitrary path fuzzing.
- Exact Docker bind and named-volume-subpath packaging, rootless Docker, read-only root, dropped capabilities, config/state mounts, and rollback.
- Linux systemd ACL, supported NAS ACL/path variants, macOS dedicated-account ACL, Windows NTFS ACL, and explicit unsupported behavior for current broad-access packages.
- Ignore patterns/includes and mid-operation changes on both app/helper sides; proof that no test edits ignores or performs a preflight write.
- Syncthing conflict copies, duplicate delivery, partial files, immutable create races, folder mode/path changes, unmounts, and engine/helper/app restarts.
- Versioning modes, backup snapshots, tombstone behavior, cleanup after every terminal path, idempotency, bounded retries/scans, and no permanent polling.
- Privacy snapshots proving identifiers, paths, names, artifacts, correlation values, and raw bodies stay out of logs, durable result stores, and Cloud Relay.
- Old/new app × old/new helper, both downgrades, multiple folders/homeservers, and unsupported deployments without any existing-user mutation.

## Human approval required

Review must explicitly approve or reject each of these choices before implementation:

1. The exact visible root name `VaultSync Diagnostics` and its unavoidable visibility in Obsidian, Files, peers, backups, versions, and tombstones.
2. Helper-side explicit creation by a local privileged installer step, followed by an exact-subdirectory runtime mount/ACL and no parent-vault access.
3. The deployment matrix: current named-volume Docker, macOS, Windows, and NAS variants remain unsupported until their exact isolation is proven.
4. The canonical dual-signature ownership records, stable installation binding, append-only helper/authorization epoch chains, immutable files, no suffix/adoption behavior, and root/manifest non-deletion rule.
5. Acceptance that live TTL cannot erase backups, Syncthing versions, remote peer history, or tombstones; otherwise the capability remains blocked.
6. The fixed scan/file/install/rotation limits and conflict behavior that leaves all unexpected content untouched.
7. Explicit disable/rollback behavior that removes no user data, namespace root, or credentials automatically.

Approval of this document still does not approve credentials/pairing, the canonical operation contract, a probe, packaging changes, helper rollout, or app implementation.

## Result

The proposed design can confine a future helper to a visible, authenticated app-owned namespace without granting runtime access to user notes. That claim is conditional on real per-platform installer, ACL, subdirectory-mount, path-race, ignore, backup, conflict, tombstone, and rollback evidence. Until human review accepts those tradeoffs and every supported deployment proves confinement, Decision 021 remains blocked and the current helper stays config-read-only/Trigger-v1-only.

## Links

- [Decision 019 — Relay evidence stays layered](019-relay-proof-hierarchy.md)
- [Decision 020 — Sync-path proof requires correlated evidence](020-sync-path-proof-requires-correlated-evidence.md)
- [Decision 021 — Capability-negotiated helper contract](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md)

# M5 dormant upload-attestation readiness

## Status and claim boundary

M5 is an internal, upload-only implementation foundation for Decision 024. It
does not authorize or provide helper runtime, App runtime, capability
negotiation, packaging, publication, rollout, controlled download, or causal
roundtrip. VaultSync 2.0 remains **NO-GO**.

| Evidence field | M5 state | Strongest permitted claim |
|---|---|---|
| Upload | Test/mock only | Exact fresh app-authored request bytes became readable through the confined namespace to the paired test helper, whose exact signed attestation was accepted from the pinned mock channel for the active query. |
| Download | Unset | Not implemented and not inferred from a synchronized attestation copy, file presence, time, HTTP, scan, index, idle, completion, or cleanup. |
| Roundtrip | Unset | Cannot be derived without a separately approved controlled download for the same causal chain. |
| Authenticated correlation | Test/mock only | Exact app/helper keys and epochs, homeserver/folder binding, operation, request/payload/query digests, nonces, TTL, signatures, and active byte-identical query. |

There is no global success flag. State and limits are scoped to the exact
app/homeserver/folder/helper/key-epoch tuple and operation. Cleanup state is
orthogonal to evidence.

## Implemented boundary

- Decision 024 deterministic CBOR messages 3–5 only: `operation_request`,
  `attestation_query`, and `upload_attestation`.
- Exact 256-byte request payload, SHA-256 domain/payload digests, Ed25519
  signatures and key IDs, nonzero operation/nonces, exact bindings/epochs, and
  600-second TTL with 120-second wall-clock skew.
- Helper reads the complete immutable request only through the M4 confined root
  handle after validating the current authenticated namespace authorization.
- The helper creates an anonymous inode, writes and fsyncs the complete
  attestation, links it once to the final filename, fsyncs the directory, then
  returns the same persisted bytes. Partial final names and overwrite are
  impossible on the supported Linux filesystem path.
- Repeated exact queries and helper restart return only the exact authenticated
  persisted attestation. A different query, binding, epoch, key, operation,
  digest, signature, payload, clock, or conflicting artifact cannot upgrade.
- Fixed limits: one active operation per tuple, two in the Swift app test model,
  eight helper-wide, eight polls, three starts/hour and twelve/day per
  app/folder, sixty starts/day helper-wide, 30 direct requests/minute per paired
  app, and 120/minute helper-wide. Invalid requests count.
- Swift parsing and upload acceptance are test-target-only and require the exact
  pinned mock channel plus the exact active query. Pending, HTTP acceptance,
  reachability, timestamps, and synchronized copies do not set upload.

Not implemented: capability query/response, any flags, local endpoint, TLS,
listener, response authorization, response artifact, authenticated cleanup
messages, download evidence, roundtrip, product UI, automatic discovery,
namespace creation, pairing/trust adoption, or durable operation history.

## Compatibility matrix

| App | Helper | Result |
|---|---|---|
| Current product app | Current product helper | Trigger v1, Cloud Relay v1, passive/local evidence, and all existing setup behavior remain unchanged. No M5 code is called. |
| Current product app | Source tree containing M5 | Same behavior. The helper entry point, installers, Compose, image, and binaries do not reference M5 and advertise no capability. |
| M5 Swift test client | Current product helper | Unsupported/capability unavailable; no artifact and no fallback evidence. |
| M5 Swift test client | M5 in-process mock attestor with authenticated M4 fixture | Upload-only test evidence after exact signed attestation; download and roundtrip remain unset. |
| Any old/new product combination after source revert | Any old/new product combination | Existing wire behavior is unchanged. Product state needs no migration. Opaque test artifacts or user-controlled backup/version/tombstone copies may remain but are ignored and never regain validity. |
| Rotated/revoked/lost key or changed epoch/binding | M5 mock attestor | Fail closed; explicit re-pair/re-authorization is required by the dormant M3/M4 foundations. No automatic trust transfer or proof resumption. |

## Namespace, retention, and cleanup

The live request and attestation are exact bounded operation artifacts under one
authenticated installation. M4 cleanup deletes only a previously owned file
whose descriptor-relative identity and digest still match. It never changes
upload/download/roundtrip fields and never targets the root, README, manifests,
authorization, credentials, unverified entries, user files, backups,
`.stversions`, conflict copies, remote history, or tombstones. Retained opaque
copies can outlive TTL but cannot be accepted by a later operation.

The Decision 024 authenticated cleanup protocol is deliberately absent and must
be implemented in its own later milestone together with response foundations.

## Privacy and external systems

M5 has no logger, telemetry, crash annotation, support export, UserDefaults,
Keychain, credential database, Relay, APNs, StoreKit, Trigger, status, probe, or
Syncthing runtime client. Contract values live only in test memory and the
disclosed namespace artifacts. The local E2E uses two temporary Syncthing homes
and folders with discovery, Relay, NAT, upgrades, usage reporting, and crash
reporting disabled inside a network-isolated container. It changes no installed
Syncthing configuration and contacts no production service.

## Rollback and next gate

M5 rollback is a source revert: no product migration or wire rollback is
required because no runtime calls the code and no release or deployment exists.
Test artifacts are removed with temporary directories; user-controlled copies
would follow the retention rules above.

After M5 merge, Decisions 021–025 must be compared against the actual code.
Decision 025 does not authorize Helper/App runtime or Phase B. A separate owner
scope approval is required before the dormant response/cleanup foundation, and
every subsequent milestone retains its own PR and owner merge gate.

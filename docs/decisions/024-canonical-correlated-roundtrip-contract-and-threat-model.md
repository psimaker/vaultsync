# 024 — Canonical correlated-roundtrip contract and threat model

**Status:** Proposed design; not implemented, not independently reviewed, and not approved for runtime use. Human security and product approval is required. This decision authorizes no capability endpoint, credential, namespace, artifact, probe, bridge event, Relay change, helper rollout, or app implementation.

## Scope and current truth

This decision supplies the canonical contract/threat-model milestone required by [Decision 021](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md). It assumes, but does not approve or implement, separately accepted mutual pairing and least-privilege namespace/access designs.

The strongest implemented proof remains a fresh successful local `ItemFinished` apply newer than cursor, nanosecond start, and engine-generation baselines. Upload, controlled download, and roundtrip are not implemented or confirmed. Timestamp proximity, HTTP success, Relay observation, APNs, scan/index activity, completion, and `idle` cannot populate the new evidence fields.

Trigger v1, Cloud Relay provisioning/status, APNs payloads, StoreKit, folder/device mappings, passive diagnostics, and old helper behavior remain byte-for-byte and semantically unchanged. Cloud Relay is never a carrier or observer of this contract.

## Capability identity and negotiation

The exact first-version capability identifier is:

```text
eu.vaultsync.diagnostics.correlated-roundtrip/1
```

Protocol major version is unsigned integer `1`; cryptographic suite is unsigned integer `1`. A paired app sends a signed capability query over the pinned local helper channel. A signed helper response must echo the query digest and exact app/helper keys, epochs, homeserver/folder bindings, and advertise capability flags `0x0f`:

| Bit | Required feature |
|---|---|
| `0x01` | Helper upload attestation after reading the exact synchronized request. |
| `0x02` | App-signed authorization before helper response creation. |
| `0x04` | Helper-signed response delivered through the synchronized namespace. |
| `0x08` | Mutually authenticated idempotent cleanup. |

All four bits are mandatory for a roundtrip operation. Missing capability, missing bit, unknown mandatory bit, unknown version/suite, invalid signature, revoked pairing, inaccessible namespace, or transport loss yields `capability unavailable`/`unsupported` before any artifact is created. There is no feature downgrade and no fallback roundtrip meaning.

The capable helper is installed first and stays dormant for old apps. Capability discovery itself creates no namespace, operation, wake-up, Relay request, or persistent proof.

## Canonical encoding

All signed messages and synchronized artifacts use the [RFC 8949](https://www.rfc-editor.org/info/rfc8949) core deterministic CBOR encoding and a deliberately narrow data model:

- definite-length maps and arrays only;
- unsigned-integer map labels and unsigned-integer values;
- byte strings with exact schema lengths;
- the one exact ASCII capability text string;
- no negative integers, floats, tags, booleans, nulls, indefinite items, duplicate keys, invalid UTF-8, or alternate text normalization;
- shortest integer/length encodings and RFC 8949 deterministic map-key order;
- no unknown fields in protocol major 1; and
- maximum nesting depth four, maximum map entries 32, maximum array entries eight, and maximum decoded/encoded message size 16 KiB.

Decoders reject invalid or non-canonical bytes before semantic or signature processing. An accepted body is re-encoded and must byte-equal the input. Generic decoders that discard duplicate keys are not permitted. Parsers allocate only after validating bounded lengths.

The signature field is label `255` and is exactly 64 bytes. Signatures use Ed25519 as defined in [RFC 8032](https://www.rfc-editor.org/info/rfc8032). The signed body is the deterministic map with label `255` omitted. Digests are SHA-256 over the exact signature domain plus that body; payload digests are SHA-256 over the exact payload bytes. An app or helper key ID is `SHA-256("eu.vaultsync.key-id/ed25519/v1\0" || raw_public_key)`, where `raw_public_key` is the exact 32-byte RFC 8032 compressed public key.

## Field registry

| Label | Name | Type and length |
|---:|---|---|
| `1` | capability | exact ASCII text `eu.vaultsync.diagnostics.correlated-roundtrip/1` |
| `2` | protocol_major | uint, exactly `1` |
| `3` | suite | uint, exactly `1` |
| `4` | message_type | uint enum below |
| `5` | homeserver_binding | bstr, 32 bytes |
| `6` | folder_binding | bstr, 32 bytes |
| `7` | app_key_id | SHA-256 key ID, 32 bytes |
| `8` | helper_key_id | SHA-256 key ID, 32 bytes |
| `9` | app_epoch | uint |
| `10` | helper_epoch | uint |
| `11` | operation_id | random bstr, 32 bytes |
| `12` | issued_at | uint Unix seconds |
| `13` | expires_at | uint Unix seconds |
| `14` | request_nonce | random bstr, 32 bytes |
| `15` | request_payload | random bstr, exactly 256 bytes |
| `16` | request_payload_digest | SHA-256, 32 bytes |
| `17` | request_digest | SHA-256 message digest, 32 bytes |
| `18` | helper_nonce | random bstr, 32 bytes |
| `19` | observed_at | uint Unix seconds |
| `20` | attestation_digest | SHA-256 message digest, 32 bytes |
| `21` | authorization_nonce | random bstr, 32 bytes |
| `22` | authorization_digest | SHA-256 message digest, 32 bytes |
| `23` | response_nonce | random bstr, 32 bytes |
| `24` | response_payload | random bstr, exactly 256 bytes |
| `25` | response_payload_digest | SHA-256, 32 bytes |
| `27` | capability_flags | uint bitset, exactly `0x0f` |
| `28` | cleanup_targets | array of one to three artifact message digests, each 32 bytes |
| `29` | cleanup_results | array matching `28`; each uint is `1=deleted`, `2=already_absent`, `3=retained_conflict`, or `4=failed` |
| `30` | query_nonce | random bstr, 32 bytes |
| `31` | prior_message_digest | SHA-256, 32 bytes |
| `255` | signature | Ed25519 signature, 64 bytes |

Operation/key/binding values are never encoded as filenames until exact-length validation succeeds. Filenames are generated internally as lowercase unpadded base32; a received path string is never a contract field.

Cleanup targets are sorted by their 32-byte digest in ascending byte order and contain no duplicates. A cleanup acknowledgement has the same targets in the same order and exactly one result per target; a missing, extra, reordered, or unknown result is invalid.

## Message types and signature domains

Each signature input is the exact ASCII domain including its trailing NUL byte, followed by the deterministic CBOR body without field `255`.

| Type | Message | Signer | Required fields beyond `1`–`10`, `12`, `13` | Signature domain |
|---:|---|---|---|---|
| `1` | capability_query | App | `30` | `eu.vaultsync.roundtrip/v1/capability-query\0` |
| `2` | capability_response | Helper | `27`, `30`, `31` = query digest | `eu.vaultsync.roundtrip/v1/capability-response\0` |
| `3` | operation_request artifact | App | `11`, `14`, `15`, `16` | `eu.vaultsync.roundtrip/v1/operation-request\0` |
| `4` | attestation_query | App | `11`, `17`, `30` | `eu.vaultsync.roundtrip/v1/attestation-query\0` |
| `5` | upload_attestation artifact/response | Helper | `11`, `16`, `17`, `18`, `19`, `30`, `31` = query digest | `eu.vaultsync.roundtrip/v1/upload-attestation\0` |
| `6` | response_authorization | App | `11`, `17`, `20`, `21` | `eu.vaultsync.roundtrip/v1/response-authorization\0` |
| `7` | response_artifact | Helper | `11`, `17`, `20`, `22`, `23`, `24`, `25` | `eu.vaultsync.roundtrip/v1/response-artifact\0` |
| `8` | cleanup_request | App | `11`, `28` | `eu.vaultsync.roundtrip/v1/cleanup-request\0` |
| `9` | cleanup_ack | Helper | `11`, `28`, `29`, `31` = cleanup-request digest | `eu.vaultsync.roundtrip/v1/cleanup-ack\0` |

Fields not listed for a message are forbidden. The `request_digest`, `attestation_digest`, and `authorization_digest` are computed with the corresponding domain and canonical body; a message does not contain its own digest. A signature valid for one type/domain is invalid for every other type, protocol, or capability.

## Transport and fixed local endpoints

Control messages use the mutually paired, [TLS 1.3](https://www.rfc-editor.org/info/rfc8446)/SPKI-pinned helper channel. Paths are fixed so identifiers cannot leak through access logs or proxy URLs:

| Endpoint | Request | Response |
|---|---|---|
| `POST /api/v1/diagnostics/capability` | `capability_query` | `capability_response` |
| `POST /api/v1/diagnostics/attestation` | `attestation_query` | exact `upload_attestation`, or fixed pending/unavailable transport category |
| `POST /api/v1/diagnostics/authorize-response` | `response_authorization` | fixed accepted transport category; the later synchronized artifact is authoritative |
| `POST /api/v1/diagnostics/cleanup` | `cleanup_request` | `cleanup_ack` |

Requests use `Content-Type: application/cbor`, reject compression/chunk ambiguity, and are capped at 16 KiB before decoding. There are no operation IDs, key IDs, bindings, hashes, or nonces in URL paths, queries, headers, certificates, or access logs. The helper has no raw-body logging and returns fixed error bodies/categories.

HTTP `200`, `202`, `404`, timeout, or endpoint reachability is transport diagnostics only. It cannot set upload, download, roundtrip, or cleanup success without the exact signed message and local state transition. Cloud Relay is never called by these endpoints.

The operation request, helper attestation, and helper response are immutable files in the authenticated synchronized namespace. For one logical attestation lookup, the app creates one signed query and retransmits those byte-identical bytes for every bounded poll; a retry never changes its nonce or digest. After the helper has validated both the request artifact and that query, it creates the exact signed attestation once, writes those bytes before serving them through the local endpoint, and returns the same bytes idempotently for the same query. A different query for that operation is rejected. This permits bounded restart recovery without an ambiguous second attestation. The app accepts upload evidence only from the paired endpoint response for its active query; a copy arriving through Syncthing is control data, not upload evidence. The response artifact is accepted only through the fresh local-apply/content gates below.

## Causal operation sequence

One user tap starts at most one operation for one paired app/homeserver/folder/helper tuple.

1. **Preflight.** Verify active pairing/epochs, signed capability `0x0f`, exact settled folder mapping, one designated connected unpaused peer, `sendreceive` on both relevant sides, authenticated namespace/authorization, supported ignores/access, no collision/conflict, and concurrency/rate limits. Failure creates no artifact.
2. **Active state.** Generate a fresh 32-byte operation ID, request nonce, and 256-byte random request payload. Capture the app's operation monotonic deadline and current engine generation. Keep correlation state in memory.
3. **Request creation.** Build and sign the canonical `operation_request`; verify its payload digest; create the one exact request filename with exclusive immutable semantics. The app never claims upload from its own write, local scan, index change, or folder state.
4. **Helper observation.** The paired helper encounters the exact request within its locally bound namespace, opens it through the confined handle, verifies canonical bytes, app signature, keys/epochs/bindings, active TTL/rate state, nonce/digest, folder preconditions, and replay cache, then reads all 256 payload bytes.
5. **Upload attestation.** The app sends one signed `attestation_query` and retransmits it byte-for-byte on bounded retries. Once the helper has both the validated request and this exact query, it creates exactly one immutable signed `upload_attestation` binding the request/payload digest, query digest, and fresh helper nonce; persists it before replying; and returns those same bytes idempotently. Pending HTTP responses are not evidence.
6. **Upload acceptance.** The app validates the endpoint pin, query digest, full attestation signature/content/bindings/epochs/TTL, active operation, and request/payload digest. Only now set `upload observed`. Exact claim: fresh app-authored bytes became readable to the paired helper in the selected logical folder; no byte-count, direct-route, or per-block peer claim.
7. **Response gate.** After upload acceptance, capture a second local response baseline: current event cursor, nanosecond time, and engine generation. Generate a fresh authorization nonce and send a signed `response_authorization` binding the accepted attestation digest. A response existing before this authorization is invalid.
8. **Response creation.** The helper verifies the authorization and exact attestation, then generates a fresh 32-byte response nonce and 256-byte random response payload and exclusively creates one signed `response_artifact` binding request, attestation, authorization, and response digests. It never overwrites/reuses a file.
9. **Download acceptance.** This iPhone must observe a successful `ItemFinished` for the exact expected response path newer than both response cursor and nanosecond baselines in the unchanged engine generation. It then opens that exact file locally, enforces size/canonical schema, and verifies all keys/epochs/bindings/IDs/nonces/digests/signature/TTL and response payload. Only now set `download observed`. Exact claim: fresh helper-authored bytes became locally readable in the selected folder; no direct-route, APNs, background, or per-block peer claim.
10. **Roundtrip derivation.** Set `roundtrip confirmed` only when steps 6 then 9 succeeded for the same active operation, request digest, attestation, authorization, app/helper keys/epochs, homeserver/folder bindings, and TTL. It is a scoped causal propagation claim, never global health or future-delivery evidence.
11. **Cleanup.** Preserve each evidence field and separately run mutually authenticated bounded cleanup. Cleanup outcome cannot upgrade or erase evidence.

If response bytes arrive before the response baseline/authorization, after engine restart, outside TTL, without upload acceptance, or through an unexpected path/event, they are stale/conflict and cannot set download. An app restart destroys active correlation; surviving valid-looking artifacts can only be cleanup candidates.

## Evidence state machine

Evidence and cleanup are orthogonal records scoped to one tuple; there is no global success flag.

| State/field | Entry rule | Allowed next result |
|---|---|---|
| `checking` | Preflight passed and active in-memory operation exists. | upload, timeout, cancelled, interrupted, conflict. |
| `upload observed` | Exact paired-helper attestation accepted for active request. | response authorization, then download/roundtrip or partial/terminal failure. |
| `download observed` | Fresh post-authorization local apply plus exact response validation. Upload remains independently visible. | roundtrip if same causal chain, otherwise conflict/partial. |
| `roundtrip confirmed` | Matching upload then download for one causal contract. | Evidence terminal; cleanup state continues separately. |
| `partial` | Valid upload exists but download did not complete before a terminal outcome. | Terminal; no later artifact upgrades it. |
| `capability unavailable` | Old/missing/unreachable/unpaired/downgraded capability before artifact creation. | A later explicit new check may retry; no current operation. |
| `unsupported` | Folder/peer/mode/ignore/access/namespace/policy cannot meet preconditions. | Per-target terminal; other sync remains healthy. |
| `timeout` | Ten-minute monotonic deadline expires. | Terminal with any existing upload field preserved; cleanup. |
| `cancelled` | User/view cancellation. | Terminal; cleanup; late messages stale. |
| `interrupted` | App/helper/engine restart, protected-data loss, mapping/mode/peer/access change. | Terminal; cleanup; late messages stale. |
| `conflict` | Duplicate/ambiguous/non-canonical/unexpected authenticated namespace content. | Terminal; no ambiguous evidence; bounded safe cleanup only. |
| `cleanup pending/failed` | Exact owned artifacts remain or retry budget is exhausted. | Evidence unchanged; later bounded explicit/startup cleanup may retry. |

Organic fresh `ItemFinished` events may continue to populate the existing local-progress field under Decision 020, but never the contract's upload/download/roundtrip fields.

## Time, size, rate, and concurrency limits

| Limit | Proposed value |
|---|---:|
| Capability response lifetime | 120 seconds maximum. |
| Operation TTL and app monotonic deadline | 600 seconds maximum; no message extends it. |
| Allowed wall-clock skew | ±120 seconds, while monotonic deadline remains authoritative locally. |
| Pairing/bootstrap lifetime | 300 seconds maximum under the separate pairing design. |
| Message/artifact/HTTP body | 16 KiB maximum encoded bytes. |
| Request and response payload | Exactly 256 random bytes each. |
| Active operations | One per app/homeserver/folder tuple, two per app process, eight per helper. |
| Starts | Three/hour and twelve/day per app/folder; sixty/day per helper. |
| Attestation polls | Eight maximum with fixed delays `2, 4, 8, 16, 30, 60, 120, 120` seconds; cancellation/TTL can stop sooner. |
| Direct control requests | Thirty/minute per paired app and 120/minute helper-wide, including invalid requests. |
| Immediate cleanup retries | Three with fixed `1, 5, 30` second backoff, then `cleanup failed`. |

The app rejects `expires_at <= issued_at`, `expires_at - issued_at > 600`, issue time more than 120 seconds in the future, or an already-expired message after skew handling. The helper applies the same bounds and its own monotonic operation lifetime. Limits are hard local policy, not network-negotiable values.

## Cleanup contract

- `cleanup_request` lists only the one to three exact message digests from the active/terminal operation. The helper maps them to canonical filenames under the authenticated app installation; the network supplies no path.
- Before deletion, re-open through the confined namespace handle and validate root/authorization, filename, regular-file identity, complete canonical content, signature, bindings, operation, and digest. A changed/unverified file is retained as conflict.
- A creator normally deletes its own artifact: app request by the app, helper attestation/response by the helper. After expiry either side may delete a valid counterpart only under the namespace decision's authenticated rules.
- Repeated cleanup of an already-absent exact artifact returns signed `already_absent`; it is idempotent success, not evidence.
- Root, README, manifests, credentials, authorization records, unverified entries, parent paths, user files, backups, versions, and tombstones are never cleanup targets.
- App/helper restart performs at most one bounded authenticated scan; there is no permanent cleanup poll or endless retry.

## Compatibility and rollback matrix

| App | Helper | Required behavior |
|---|---|---|
| Existing app | Existing helper | Trigger v1 and passive/local evidence only. No capability messages or artifacts. |
| Existing app | Capable helper | Helper stays dormant; old app ignores capability and authenticated namespace if a newer app explicitly enabled it. Trigger v1 unchanged. |
| Capable app | Existing helper | `capability unavailable`; no endpoint fallback, namespace creation, probe, error escalation, or v1 change. |
| Capable app | Capable but unpaired/disabled helper | Pair/enable guidance only after explicit user action; no artifact. |
| Capable app | Paired/enabled capable helper | Operation may start only after every signed capability and target precondition passes. |
| App downgrade | Capable helper | Helper remains dormant for that app, expires/cleans authenticated artifacts within bounded rules, preserves credentials/root, and keeps Trigger v1. |
| Capable app | Helper downgrade | Capability becomes unavailable; no new operation, no fallback success, no mapping/credential/root deletion. |
| Helper re-upgrade | Capable app | Revalidate pairing epochs, namespace ownership, access, and capability from scratch; never resume an old proof. |

Capability removal/downgrade never converts timestamps, HTTP responses, Relay data, existing artifacts, or local folder state into evidence. Unsupported folders remain normal syncing folders.

## Failure matrix

| Failure | Evidence result | Cleanup/result behavior |
|---|---|---|
| Missing/old/unreachable helper before request | Capability unavailable; no evidence. | No artifact or cleanup. |
| Invalid capability signature/flags/version | Capability unavailable/unsupported; no request. | Fixed security category; no raw body/log. |
| Request signature/digest/binding/TTL invalid | No upload; helper creates nothing. | Exact invalid request may remain for app cleanup; never helper-authored response. |
| Helper reads request but app never receives valid attestation | No upload accepted; timeout/partial absent. | Helper/app bounded cleanup. |
| Valid attestation, authorization never arrives | Upload observed; terminal partial/timeout/cancelled. | No response; cleanup request/attestation. |
| Response appears before authorization/baseline | Conflict/stale; no download/roundtrip. | Do not overwrite; bounded authenticated cleanup. |
| Fresh event but content/signature mismatch | Existing local progress may be separate; contract download unset. | Conflict/partial; exact invalid file retained unless safely owned/expired. |
| Response valid but event predates cursor/time or generation changed | No download; interrupted/stale. | No later upgrade; bounded cleanup. |
| Folder/peer/mode/ignore/access changes mid-operation | Interrupted/unsupported; preserve valid upload only. | Bounded cleanup; never auto-resume/reconfigure. |
| App/helper crash or restart | Interrupted; operation never resumes proof. | Reconstruct only idempotent response/cleanup from authenticated artifacts. |
| Cleanup fails | Evidence remains exact; cleanup failed visible. | Stop finite retries; no broad delete/polling. |
| Cloud Relay/APNs/StoreKit unavailable | Contract evidence unchanged; those layers retain their own states. | No contract data/call reaches Relay. |

## Threat model

| Threat | Required defense and claim limit |
|---|---|
| Malicious/compromised folder peer fabricates files | App/helper signatures, exact bindings/epochs/digests, active operation, response authorization, TTL, and event baselines reject forgeries/replays. A peer may relay valid signed bytes, so no direct-transport/per-block claim. |
| Copied valid artifacts from another app/folder/server | App/helper keys, epochs, homeserver/folder bindings, operation and causal digests differ; fail closed. |
| Network MITM or endpoint substitution | TLS 1.3 SPKI pin plus mutual application signatures; HTTP/body alone is never evidence. |
| Replay/downgrade/out-of-order delivery | Active in-memory operation, unique nonces/ID, exact prior-message digests, epochs, TTL/monotonic deadline, and strict state transitions. |
| Non-canonical/ambiguous parser input | Narrow deterministic-CBOR subset, duplicate rejection, re-encode comparison, bounded allocation, cross-language golden/fuzz tests. |
| Path traversal/symlink/reparse/mount race | Separate namespace/access decision's local binding and descriptor/handle confinement; contract carries no path. |
| Paired malicious app floods helper | Per-app signatures, folder scopes, active/rate/daily/body/entry limits, fixed endpoints, and revocation. |
| Helper compromised within its runtime rights | Least-privilege mount/ACL confines it to diagnostics namespace. A helper signature proves authorship, not benign software or user-data safety outside that boundary. |
| Key theft/revocation/rotation/loss | Separate credential decision's epochs and explicit lifecycle; fail closed/re-pair. No automatic recovery or trust transfer. |
| Clock manipulation/delayed synchronization | Monotonic local deadline plus bounded wall-clock skew; stale/timeout, never time-only proof. |
| Artifact/block reuse | Independent 256-byte random request/response payloads and full digests make accidental reuse implausible; claim remains logical propagation, not network byte accounting. |
| Logs/proxies/crash diagnostics | Fixed paths/categories, no raw bodies/access values, privacy lint/snapshots; Cloud Relay receives nothing. |
| Backup/version/tombstone retention | Disclosed opaque retention; TTL governs acceptance/live cleanup only, not historical deletion. |

## Property, fuzz, model, and E2E test plan

### Canonical/cryptographic

- Cross-language Go/Swift golden bytes and RFC 8032 vectors for every message/domain, digest, signature, key ID, and filename encoding.
- Roundtrip encode/decode/re-encode equality and rejection of duplicate/reordered/unknown keys, non-shortest integers, indefinite lengths, wrong types/lengths, tags, floats, deep nesting, huge lengths, truncation, trailing bytes, and arbitrary input.
- Wrong app/helper/key epoch/binding/operation/nonce/digest/payload/signature/domain/version/suite/flags, plus rotated/revoked key fixtures.
- Fuzz every decoder, canonicalizer, domain constructor, digest chain, filename encoder, and fixed endpoint body parser with bounded allocations and no panics.

### State/model properties

- Generate arbitrary event/message orders and assert: roundtrip implies prior upload and download; download implies a post-authorization fresh apply; no terminal/cancelled/restarted operation upgrades; one tuple never affects another; cleanup never changes evidence.
- Replay, duplicate, copy, delay, partial write, response-before-attestation, response-before-authorization, multiple attestations/responses, cursor overflow, generation changes, and clock skew.
- Model-check concurrency caps, single-flight leases, retry/poll/TTL termination, rate windows, helper restart idempotency, and app restart non-resumption.
- Prove no transition exists from timestamp, HTTP/Relay/APNs, scan/index/idle/completion, capability reachability, cleanup, or tombstone alone to upload/download/roundtrip.

### Compatibility/failure/privacy

- Every old/new app × old/new helper row, both downgrades and re-upgrade, missing features/versions, multiple apps/homeservers/folders, unsupported folder modes/peers/access/ignores.
- Snapshot app/helper/bridge/proxy logs, crash annotations, local stores, Cloud Relay requests, Trigger v1 requests, and APNs payloads with sentinels for every forbidden identifier/path/key/nonces/hash/body.
- Regression-pin that Relay v1 receives exactly its existing fields/calls and no capability, namespace, artifact, operation, digest, correlation, result, or cleanup data.

### Local/mock E2E only

- Use temporary directories, two or more ephemeral local Syncthing instances, a mock pinned helper endpoint, test credentials, and no production Relay/APNs/StoreKit/trigger/status/probe service.
- Demonstrate request propagation, exact helper read/attestation, app upload acceptance, post-baseline authorization, response propagation, fresh local apply/content verification, separate upload/download fields, causal roundtrip, and cleanup.
- Inject offline peers, delay/reorder/replay/copy, multi-peer ambiguity, conflicts, ignores, versioning, restarts, permission loss, path races, and cleanup failure.
- Packaging E2E must separately prove helper-first upgrade and rollback on every supported Docker/host/NAS/macOS/Windows model before any app release.

No suite is considered complete if it uses production services, skips cross-language fixtures, or asserts only a final green state without the independent per-leg and cleanup fields.

## Privacy and persistence

Within product-controlled live state, active operation IDs, payloads, nonces, digests, message bytes, event baselines, and per-leg evidence live only in app/helper memory and the approved TTL-bounded namespace artifacts. User-controlled backups, Syncthing versions, remote peer history, and deletion tombstones may retain opaque artifact copies beyond live TTL as disclosed by the namespace decision; such copies never regain validity. Operation data never enters UserDefaults, Keychain, helper credential databases, logs, telemetry, crash reports, support bundles, Cloud Relay, APNs, StoreKit, or durable diagnostic history.

The helper may reconstruct idempotent response/cleanup only from exact authenticated namespace artifacts; it creates no separate operation database. The app never resumes proof after restart. `PRIVACY.md`, operator docs, all four localizations, and privacy lint must be updated and approved before any runtime artifact or transport exists.

## Rollout and rollback gate

1. Human reviewers accept the credential/pairing, namespace/access, and this canonical contract/threat model.
2. Contract/model/fuzz/privacy tests land before runtime carriers.
3. A dormant helper capability ships with no automatic listener, namespace, artifact, or Relay/v1 change.
4. Helper packaging, least privilege, upgrade, and both rollback directions are proven on supported deployment rows.
5. The helper release is actually published and rollout evidence is recorded before the app recognizes/offers the capability.
6. App support first exposes authenticated capability unavailable/unsupported states; the smallest transfer implementation is upload attestation only.
7. Controlled response/download and roundtrip derive only in later separately reviewed milestones after upload, replay, cleanup, backup, and real-device evidence.

If helper-first rollout or rollback is not proven, Phase B and app runtime work remain blocked. A helper rollback yields capability unavailable; an app rollback leaves the helper dormant. Neither deletes credentials, namespace root, mappings, or user data.

## Human approval required

Review must explicitly approve or reject each of these choices before implementation:

1. Exact capability ID, deterministic-CBOR subset/field registry, Ed25519 domains, digest definitions, and fail-closed unknown-field/version behavior.
2. The two-carrier/two-phase sequence: direct signed upload attestation, then app-signed response authorization, then synchronized response artifact.
3. Exact upload/download/roundtrip claim language and the explicit rejection of byte-count, direct-peer, Relay/APNs, global-health, and future-delivery claims.
4. TTL/skew, payload/body, rate, poll, concurrency, daily, retry, and cleanup limits.
5. The evidence state machine, especially no resume after restart and no download/roundtrip from a response that predates upload acceptance/authorization/baseline.
6. Fixed local endpoint paths, no identifiers in URLs/logs, pinned mutual authentication, and Cloud Relay's total exclusion.
7. Failure, compatibility, privacy, property/fuzz/model, local E2E, helper-first rollout, and both downgrade matrices as release gates.

Approval of this document still does not approve runtime code, a probe, namespace creation, credentials, installer permissions, a helper release, production rollout, Phase B, or an app release.

## Result

The proposed contract makes upload and download independently verifiable and permits a scoped roundtrip only through one exact app-authored request, paired-helper attestation, app authorization, and fresh helper-authored response applied locally in causal order. It still cannot prove exact network bytes, a direct transport peer, or per-block provenance. Until all three design milestones receive human security/product approval and helper-first rollout/rollback is demonstrated, Decision 021 remains blocked and VaultSync 2.0 remains NO-GO.

## Links

- [Decision 019 — Relay evidence stays layered](019-relay-proof-hierarchy.md)
- [Decision 020 — Sync-path proof requires correlated evidence](020-sync-path-proof-requires-correlated-evidence.md)
- [Decision 021 — Capability-negotiated helper contract](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md)

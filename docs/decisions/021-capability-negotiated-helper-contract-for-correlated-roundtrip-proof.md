# 021 — Capability-negotiated helper contract for correlated roundtrip proof

**Status:** Proposed design; not implemented. This decision authorizes no probe write, namespace creation, credential, endpoint, Trigger v2, Relay change, or runtime rollout.

## Context and current truth

The strongest implemented synchronization-path proof is fresh local data progress: a successful incoming file `ItemFinished` newer than both the check cursor and nanosecond start boundary, within one stable engine generation. It proves that this iPhone applied a file change during the check window. It does not prove that the check caused the change, that network bytes moved, which peer supplied blocks, or that the helper participated.

Upload is not confirmed. A controlled download is not confirmed. A full roundtrip is not confirmed. Temporal proximity is only a freshness boundary, never correlation. Engine reachability, scan completion, local or remote index activity, folder `idle`, 100% completion, Relay observation, and HTTP success remain diagnostics rather than transfer evidence. These limits from Decisions 019 and 020 remain normative.

The current `vaultsync-notify` helper has no inbound app endpoint, no capability contract, no pairing key, and no write access to vault data in the standard Docker deployment; it reads `config.xml` through a read-only mount and calls Syncthing's REST API. The current bridge exposes folder/device snapshots and sanitized events, but an `ItemFinished` event does not carry authenticated source-peer provenance. No design may pretend those missing properties already exist.

## Decision

A future controlled check may claim a roundtrip only through an additive, helper-first, capability-negotiated contract that produces two independent pieces of cryptographically bound evidence for one active operation. The synchronized data plane, if approved, uses a visible app-owned diagnostics namespace inside one selected Syncthing folder; capability discovery, pairing, and helper control may use a separate additive local contract. The Cloud Relay is never part of the correlation path.

This is a conditional architecture decision, not runtime approval. A synchronized namespace is the only evaluated option that exercises both Syncthing directions, but namespace ownership/access and helper authentication are unresolved blockers. A new credential/pairing milestone is required before any response can be attributed honestly to a particular helper and homeserver.

## Proof definitions and claim limits

Every proof stays scoped to one active operation and one locally selected tuple: app installation, homeserver mapping, folder mapping, paired helper key, and opaque folder binding. There is no global success flag, and evidence for one tuple never upgrades another.

| Evidence | Minimum acceptance rule | Exact claim | Explicitly not claimed |
|---|---|---|---|
| **Upload observed** | The app verifies a helper-signed attestation for the active opaque operation ID. It binds the paired helper key, opaque folder binding, request digest, fresh random request payload, protocol version, and expiry, and states that the helper read those exact bytes from its locally configured folder after accepting the request. | Fresh information created and signed by this app installation became readable to the paired helper in the selected logical folder. | Exact byte count, direct transport, that every block crossed the network, that the helper's Syncthing instance received every block directly from this iPhone, or that no other peer relayed/copied data. |
| **Download observed** | After the upload attestation, this iPhone sees a fresh local apply event inside the active cursor/time/generation window, reads the exact response artifact, verifies the paired helper signature, opaque folder binding, operation ID, request and response digests/nonces, payload, and TTL. | Fresh information authored by the paired helper became locally readable in the selected logical folder. | Exact byte count, direct helper-to-iPhone transport, APNs delivery, background execution, or which Syncthing peer supplied individual blocks. |
| **Roundtrip confirmed** | Valid upload evidence is followed by valid download evidence for the same operation, helper key, folder binding, request digest, and causal response; download cannot precede upload. | One fresh app-authored request was observed by the paired helper and caused a fresh helper-authored response that this iPhone applied. | General sync health, future delivery, every folder, every peer, Relay/APNs success, or a physical network-route trace. |

The probe payloads must be cryptographically random and unique enough to carry fresh information, which makes accidental block reuse implausible. Even then, the claim is logical propagation and causal helper participation, not network accounting. Source attribution means “signed by this app installation” and “attested by this paired helper”; it does not mean Syncthing can identify the transport peer that delivered each block.

Weaker evidence never fills a stronger field. A local apply without a valid helper response remains local data progress only. A helper signature without a fresh local apply may set upload evidence but not download evidence. Timestamps and HTTP status codes may bound a session or report availability, but neither can create upload, download, or roundtrip proof.

## Logical contract, not a selected wire format

Before implementation, the protocol must freeze a canonical encoding and signature domain. At minimum, the logical messages need:

- an explicit protocol/capability identifier and version;
- a cryptographically random opaque operation ID generated by the app;
- an opaque per-folder binding, never a folder name or path;
- a request nonce, fresh random payload, and payload digest;
- an app-installation key identifier and request signature or equivalent request authentication;
- a helper response nonce and fresh random response payload;
- a typed upload-observation record binding the exact request digest;
- creation/expiry bounds subject to an app-enforced maximum TTL;
- a helper key identifier and signature over all security-relevant fields.

There is no `success` boolean. Unknown versions, algorithms, mandatory fields, or capability identifiers fail closed as capability unavailable or unsupported. A parser never logs or persists a raw message body. An HTTP `200`, if a local endpoint is eventually selected, means only that a response was transported; the signed model still decides evidence.

The exact carrier remains open. A local helper endpoint can advertise capability, support pairing, or transport a signed upload attestation, but an HTTP response does not prove a Syncthing download. The response bytes that set download evidence must still arrive through the selected Syncthing folder and pass the local event/content gates.

## Capability negotiation and compatibility

The capability is additive and versioned, for example a semantic identifier such as `correlated-roundtrip/1`; the exact identifier is not frozen here. Capability discovery must itself be authenticated before it can authorize a probe. An unauthenticated version string is useful diagnostics only.

- Missing capability means **capability unavailable**, never a setup failure and never an invitation to fall back to weaker “roundtrip” semantics.
- A new helper must ship first and remain dormant for old apps. It must not create a namespace, key pairing, probe, response, or new Relay call merely because it was upgraded.
- A capable app may ship only after the helper capability and its rollback behavior are available in supported helper distributions.
- An old helper continues Trigger v1 exactly as today. A new app talking to an old helper exposes no controlled check and preserves the passive local-progress proof.
- Rolling the helper back makes the capability unavailable; the app stops creating operations, retains no stronger proof, and performs only safe app-owned cleanup.
- Rolling the app back leaves the new helper dormant. Expired app-owned artifacts are cleaned by the capable helper's bounded cleanup rules; old app behavior and Relay v1 remain unchanged.
- Capability removal or downgrade never turns a prior timestamp, folder state, or HTTP response into proof.
- Trigger v1, provisioning, Relay status, APNs payloads, StoreKit, and existing folder/device mappings are unchanged.

### Compatibility matrix

| App | Helper | Expected behavior |
|---|---|---|
| Existing app | Existing helper | Existing Trigger v1 and passive/local evidence only; no controlled check. |
| Existing app | Capable helper | Existing behavior unchanged; helper capability stays dormant and old app ignores it. |
| Future capable app | Existing helper | `capability unavailable`; no error, probe, namespace creation, fallback success, or v1 change. |
| Future capable app | Capable helper | The check is offered only after authenticated capability, pairing, namespace, folder, path, and peer preconditions pass; otherwise the exact unavailable/unsupported state remains visible. |

## Probe/diagnostics namespace

No namespace is created by this decision. Before the first runtime write, all of the following must be approved and tested:

1. **Visible semantic name.** Select and document one deterministic, user-understandable namespace name. A hidden dot-name or system-looking name is not acceptable without proven cross-client semantics and explicit UX. The name is intentionally not chosen here.
2. **Exclusive ownership.** A future app may atomically create the exact namespace only when that path is absent and the user explicitly starts/enables the diagnostic. A pre-existing path is a collision and makes the target unsupported; it is never adopted, renamed, overwritten, deleted, or bypassed with an arbitrary suffix.
3. **Authenticated manifest.** The namespace contains a versioned ownership manifest with opaque bindings and public-key identifiers, but no Device ID, vault/folder name, path, account, StoreKit, APNs, or user data. Invalid, missing, copied, or conflicting ownership makes the target unsupported/conflict, never repairable by deleting the existing content.
4. **Visible cross-client behavior.** The directory and its temporary files are visible to users and other Syncthing clients. Product copy must say so. The design must not rely on Files, Obsidian, backup tools, or Syncthing hiding it.
5. **Ignore behavior.** The namespace must not match `.stignore` on either participating side because ignored data cannot prove transfer. A match yields unsupported. The app/helper never edits ignore rules to force the check. Backup exclusions are separate operator policy and cannot be confused with Syncthing ignores.
6. **Backup/versioning behavior.** Random probe bytes contain no user data, but server backups and Syncthing file versioning may retain them after live cleanup. TTL governs live protocol acceptance, not backup retention. If opaque retained artifacts are unacceptable, the design is blocked until a proven backup/versioning policy exists.
7. **Conflict behavior.** Operations use unique create-once files and never overwrite shared state. Any Syncthing conflict copy, manifest conflict, duplicate response, or unexpected content yields `conflict` or `partial`; it cannot set proof. Only artifacts whose ownership and signatures validate may be cleanup candidates.
8. **Tombstones.** Cleanup deletions create normal Syncthing tombstones and may remain in indexes/version history. Tombstones are never evidence. Operation frequency, file count, and concurrency must be capped to prevent unbounded churn.
9. **Path authority.** The app selects only an existing mapped folder; it never supplies a free filesystem path. The helper resolves an opaque folder binding through its locally trusted Syncthing configuration plus an operator-approved allowlist/mount. Network input cannot choose a vault, folder, host path, or relative traversal.
10. **Filesystem confinement.** Helper access is limited to the owned namespace, rejects symlinks and traversal, uses canonical directory handles/relative opens, and never renames, overwrites, or deletes user data. The current Docker helper's read-only configuration/access model does not satisfy this and must not be silently widened to the entire vault.

The helper's data-path access model is a release blocker: host binaries may share the Syncthing service account's filesystem view, while containers usually see different paths and currently mount no vault data. A future installer must make any permission change explicit, least-privileged, reversible, and independently reviewable.

## Probe lifecycle

- Generate at least 128 bits of cryptographically secure randomness for every opaque operation ID; never derive it from a Device ID, transaction, JWS, APNs token/data, account, time, path, folder name, or user identifier.
- Probe content is small random data plus the canonical signed envelope. It contains no note data, filenames from the vault, or other user content.
- The protocol has a short negotiated TTL with an app-enforced hard maximum. The exact maximum, clock-skew allowance, size limit, and rate limit are blockers to freeze before implementation; timeout produces no stronger proof.
- The active operation and correlation values live in memory. They are not written to app preferences, helper databases, logs, telemetry, crash annotations, or Cloud Relay. Their only permitted on-disk occurrence is the TTL-bounded protocol artifacts themselves; this narrow necessity must be reflected explicitly in the future Privacy Policy update.
- The app never resumes proof after restart. It marks the old operation interrupted, creates no success from surviving files, and may run a bounded cleanup scan of the authenticated namespace.
- The helper may reconstruct cleanup work after restart only from authenticated, unexpired/expired namespace artifacts. Reprocessing is idempotent: create the same response or cleanup outcome, never a second logical result.
- Cleanup is idempotent and attempted after roundtrip confirmation, timeout, cancellation, app lifecycle exit, app restart, helper restart, conflict resolution outcome, and expiry. Failure changes only cleanup state, not evidence.
- The app and helper delete only exact authenticated protocol artifacts under the owned namespace. They do not delete the namespace root or any unverified entry automatically.
- Concurrent checks use distinct operation IDs and immutable files. At most one operation per homeserver/folder tuple runs at once, plus a small global cap. One target cannot consume another target's response.
- Multiple homeservers and folders keep independent pairing, binding, capability, evidence, timeout, and cleanup states. No aggregate success or failure is inferred.
- Old responses, copied artifacts, responses for another binding/key/request digest, expired artifacts, and events predating the cursor/time/generation baseline are stale and cannot set proof.
- Polling is finite and user-initiated. No onboarding, launch, silent-push, background, timer, or permanent helper loop starts probes; cleanup scans are bounded and back off without an endless retry loop.

Partial evidence, conflicts, timeouts, and cleanup failures remain visible. A roundtrip may be confirmed while cleanup is pending/failed, but the UI must show both facts and must not collapse them into one green state.

## Authentication, homeserver/folder binding, and replay defense

The iPhone must bind a response to the same locally selected homeserver and folder before accepting it:

1. A pairing record pins a helper public key to the locally known homeserver Device ID and to one or more opaque folder bindings.
2. The selected app folder must still map to that homeserver under the approved peer policy when the operation starts and when evidence is accepted.
3. The signed request and response cover protocol version, app/helper key IDs, opaque folder binding, operation ID, both nonces, request/response digests, typed evidence, and expiry.
4. The app accepts only its active in-memory operation, exact key/binding, valid signature and digest, correct causal ordering, valid TTL, and fresh local event window.
5. Exclusive create semantics and immutable operation files make duplicate processing idempotent. Copied artifacts fail the folder binding; expired/consumed operations fail active-session and TTL checks.

Another Syncthing peer can read, copy, replay, or fabricate unsigned files in a multi-peer folder. A unique filename or temporal proximity does not stop it. A valid helper signature prevents another peer from forging helper authorship, but a peer may still relay signed bytes; therefore even the signed design does not claim a direct transport path or per-block source peer.

Existing Syncthing peer identity is insufficient by itself. It authenticates Syncthing protocol connections, but the current app event model does not retain signed per-file origin and the helper does not hold an application signing capability bound to that connection. Reusing or exporting Syncthing's TLS private key would expand and couple a long-lived sync identity to a new application protocol; that alternative is rejected absent an explicit upstream-compatible security design.

A separate mutually authenticated pairing is therefore required. At minimum the helper signs responses and the app pins that key; to prevent unpaired peers from causing response-file churn or exhausting quotas, the helper must also authenticate app requests. The likely shape is separate app-installation and helper signing keys with domain-separated signatures, but algorithm choice, key generation/storage, QR or one-time pairing UX, multi-iPhone authorization, rotation, revocation, recovery, device replacement, and lost-key rollback require their own credential/pairing decision and threat review.

StoreKit JWS, APNs tokens, Relay provisioning, and knowledge of a Syncthing Device ID do not prove control of the homeserver helper and must not bootstrap this pairing. Trust-on-first-use through an unsigned synchronized file is also insufficient because another folder peer can win the first write.

## Folder and peer matrix

The first honest runtime scope is deliberately narrow. “Unsupported” is a per-target result, not a general app, Relay, subscription, or sync error.

| Configuration at either relevant side | Upload | Download | Full roundtrip | Required result |
|---|---|---|---|---|
| Unpaused `sendreceive` on iPhone and paired helper, one designated peer, accessible owned namespace, no matching ignores | Conditionally supportable | Conditionally supportable | Conditionally supportable after all blockers/tests | Check may start. |
| iPhone `receiveonly` or helper cannot send local changes | Not supportable through the folder | A separately initiated download-only diagnostic might be designed later | Unsupported | `unsupported`; do not mutate folder type or create a weaker roundtrip. |
| iPhone `sendonly` or helper cannot receive/respond bidirectionally | Upload-only may be designed later | Unsupported | Unsupported | `unsupported`; no general error. |
| `receiveencrypted`/encrypted folder on either side | Helper cannot safely inspect clear request semantics | Clear signed response semantics are not established | Unsupported | `unsupported` until an encryption-aware design proves both legs. |
| Folder or device paused | No active transfer claim | No active transfer claim | Unsupported while paused | Per-target `unsupported`; never resume automatically for a probe. |
| Designated peer/helper offline or capability endpoint unreachable | Not observed | Not observed | Not confirmed | `capability unavailable` before start, otherwise finite `timeout`/`interrupted`; existing sync remains healthy. |
| Multi-peer folder | A helper may eventually attest reading, but transport/source peer is ambiguous | A signature can prove helper authorship, not delivery path | Unsupported in the first runtime scope | Per-target `unsupported`; no peer is removed or unshared. |
| Namespace or child matched by Syncthing ignore rules | Cannot traverse the intended data plane | Cannot traverse the intended data plane | Unsupported | `unsupported`; never edit ignores automatically. |
| Missing, unreadable, symlinked, unmounted, or otherwise inaccessible folder/namespace path | Unsafe to read/write | Unsafe to read/write | Unsupported | Preflight `unsupported`; a mid-check loss is `interrupted`, with bounded cleanup pending/failed. |
| Several homeservers and/or folders | Independent per tuple | Independent per tuple | Independent per tuple | Bounded concurrency; one result never changes another. |

Mode and peer checks run at both operation start and evidence acceptance. A configuration change mid-check interrupts the target; it never retroactively validates an artifact.

## State machine

Evidence state and cleanup state are separate axes. The evidence model retains individual upload/download fields even when a terminal presentation state is shown.

| State | Meaning and allowed transition |
|---|---|
| `checking` | Authenticated capability and target preconditions passed; the finite user-initiated operation is active. No proof yet. |
| `capability unavailable` | Helper is old, capability is absent/unreachable, or authenticated negotiation cannot complete. This is not a sync/setup error and creates no probe. |
| `unsupported` | Folder mode, peer topology, ignore, namespace collision, path access, encryption, or pairing policy cannot support this target honestly. Other targets continue. |
| `upload observed` | Valid paired-helper upload attestation accepted for the active request. Download and roundtrip remain unset. |
| `download observed` | A fresh local response apply plus exact content/signature/binding validation succeeded. If matching upload evidence is absent, the result stays partial. |
| `partial` | Exactly one leg, or otherwise incomplete valid evidence, exists at terminal time. The present leg remains visible; no roundtrip. |
| `roundtrip confirmed` | Upload then download are valid for the same active causal operation. This is scoped proof, not global health. |
| `timeout` | The finite deadline expired. Existing partial evidence remains visible; absence is not sync failure. |
| `cancelled` | User/view cancellation stopped the operation. No later artifact can upgrade it. |
| `interrupted` | App/helper restart, engine generation change, protected-data loss, configuration change, or path/access loss ended the operation. No later artifact can upgrade it. |
| `conflict` | Namespace, manifest, operation, or Syncthing conflict-copy semantics became ambiguous. No ambiguous artifact sets proof. |
| `stale` | An artifact/result is expired, outside the active cursor/time/generation window, from a prior operation, or older than the presentation freshness window. It cannot be reused. |
| `cleanup pending` | Orthogonal state: owned artifacts still need bounded idempotent cleanup after any outcome. |
| `cleanup failed` | Orthogonal state: cleanup exhausted its finite retry budget. Evidence is not erased or upgraded; the exact safe manual next step must be shown. |

No state is derived solely from a timestamp, elapsed duration, HTTP `200`, Relay observation, folder idle/completion, scan/index activity, or an uncorrelated Syncthing event.

## Privacy and logging

The future operation creates only:

- in app memory: the selected local target mapping, opaque operation ID, nonces, random payloads/digests, key references, event cursor/time/generation boundary, per-leg evidence, and cleanup state;
- in helper memory: the matching authenticated request, local binding, bounded replay/idempotency state, response payload, and cleanup state;
- temporarily in the synchronized namespace: an ownership manifest and small signed request/response artifacts containing opaque bindings/IDs, random bytes, hashes, public-key identifiers, and TTL fields;
- in a future credential store: only the separately approved pairing keys and opaque bindings required for authentication, with lifecycle defined by the credential decision.

Temporary artifacts are transmitted through Syncthing to participating folder peers and can be visible to users, other clients, file versioning, and backups. They contain no note content or user-derived metadata. Live artifacts expire and are cleaned on the lifecycle paths above, but backup/version retention is outside that TTL and must be disclosed. The Cloud Relay never receives capability messages, file names, paths, folder names, operation IDs, probe values, hashes, payloads, contents, results, or cleanup state.

Logs, telemetry, crash diagnostics, and durable diagnostic result stores must not contain:

- Device IDs or prefixes;
- APNs tokens or payload data;
- StoreKit transaction identifiers or JWS;
- file, vault, or folder names;
- file, vault, folder, or server paths;
- operation/correlation values, nonces, hashes, or probe content;
- Syncthing API keys;
- raw request/response bodies or signed envelopes.

Allowed operational logging is limited to fixed event categories, protocol major version, bounded counts, durations, and coarse terminal states, with no target-identifying values. The current privacy lint and embedded-Syncthing log suppression remain floors, not complete coverage. `PRIVACY.md` is unchanged by this design-only decision; it must be updated before any runtime artifact, persistence, credential, or transmission exists.

## Alternatives

| Alternative | Assessment | Decision |
|---|---|---|
| Synchronized app-owned diagnostics namespace | It is the only evaluated data plane that can carry fresh app bytes to the helper and fresh helper bytes back through Syncthing. It also creates path ownership, permissions, ignore, conflict, tombstone, backup, cleanup, and authentication risks. | Conditional direction only; blocked until namespace/access and pairing are proven. |
| Additive local helper endpoint/contract | Good candidate for authenticated capability discovery, pairing, control, and an upload attestation. It introduces discovery, TLS/auth, exposure, and rollback work. An HTTP response alone bypasses Syncthing and cannot be download or roundtrip proof. | May complement the namespace; never sufficient alone for transfer proof. |
| Pure Syncthing events without a probe | Events can show fresh local apply, index activity, state changes, or completion, but current events do not establish a causal artifact, authenticated helper, source peer, or both directions. | Rejected for upload, controlled download, and roundtrip proof; remains valid only for current local-progress evidence. |
| Cloud Relay as correlation channel | It would move operation data into central infrastructure, still would not prove Syncthing transfer, and would weaken the minimal wake-up privacy boundary. | Rejected. Relay must never receive filenames, paths, probe values/hashes/content, correlation IDs, or results. |
| Existing Syncthing peer identity alone | It authenticates Syncthing sessions but does not sign helper application responses or preserve per-file origin in the current app event model. | Rejected as sufficient proof; separate pairing is required. |

## Failure and threat model

| Threat/failure | Required safe outcome |
|---|---|
| Old/missing helper, capability downgrade, offline endpoint | Capability unavailable; no probe and no fallback success. |
| Another folder peer fabricates or copies artifacts | Invalid signatures/bindings fail closed; copied valid bytes cannot claim direct transport and cannot cross target/operation boundaries. |
| Replay of an old valid response | Active operation ID, request digest, folder binding, TTL, immutable files, and fresh cursor/time/generation gates reject it as stale. |
| Local process/user creates the reserved path or look-alike files | Pre-existing namespace is unsupported; unverified entries are never overwritten/deleted or accepted as evidence. |
| Path traversal, symlink swap, container/host path mismatch | Locally allowlisted canonical namespace access fails closed; no network-supplied path and no user-data mutation. |
| Helper/app crash or restart | Proof is interrupted and never resumed; bounded authenticated cleanup is reconstructed without upgrading evidence. |
| Response conflict, partial write, duplicate delivery | Conflict/partial state; immutable verified bytes only, no global success. |
| Clock skew or delayed synchronization | TTL failure/timeout/stale; time never substitutes for signature and active-operation correlation. |
| Key theft, unpairing, rotation, device replacement | No automatic recovery or trust transfer; fail closed until the credential decision defines revocation/re-pairing. |
| Malicious request flood or tombstone churn | Manual initiation, request authentication, size/rate/concurrency caps, short TTL, and finite cleanup; never permanent polling. |
| Backup/version retention | Only opaque non-user random data can remain; retention is disclosed and may block rollout if policy is unacceptable. |
| Cleanup failure | Preserve proof/partial truth plus cleanup-failed state; never broaden deletion scope. |

## Rollout, rollback, and required tests

Rollout order is helper first:

1. Approve the namespace/access and credential/pairing decisions and freeze canonical schemas, limits, and threat model.
2. Ship a dormant helper capability plus local key/pairing foundation with no automatic namespace or probe write and no Trigger v1/Relay change.
3. Verify helper upgrade/rollback across Docker, host service, NAS, macOS, and Windows packaging, including least-privilege path access.
4. Only then ship app-side authenticated capability recognition; missing/old helper remains unavailable without error.
5. Implement the smallest transfer milestone as foreground-only, explicit, one target, one peer, `sendreceive`, upload attestation only. It must still make no download/roundtrip claim.
6. Add controlled response download only after upload, replay, cleanup, backup, and threat tests are green; derive roundtrip last.

Rollback never changes Relay v1 or folder mappings. Helper rollback disables capability; the app stops new operations and safely expires/cleans only authenticated app-owned artifacts. App rollback leaves the helper dormant and lets it expire owned artifacts. Keys, pairing records, and namespace roots are not auto-deleted during rollback; their separate lifecycle must be explicit so rollback cannot strand trust or delete user content.

The contract/model test matrix must include:

- all four old/new app × old/new helper combinations, capability absence, unknown versions/fields, downgrade, and rollback;
- valid/invalid/truncated/non-canonical signatures, wrong key, wrong app/helper, wrong binding, wrong operation, wrong digest/nonce, expiry, clock skew, replay, copy, duplicate, and out-of-order response;
- the complete folder/peer matrix above, mode changes mid-check, multi-homeserver/folder isolation, bounded concurrency, and single-flight per tuple;
- namespace absent/create race/collision, invalid manifest, symlink/traversal, inaccessible/unmounted path, ignore matches, conflicts, partial files, tombstones, versioning, and least-privilege confinement;
- app/helper/engine restarts, protected-data loss, timeout, cancellation, view exit, every cleanup path, idempotency, finite retries, and no endless polling;
- proof non-escalation from local events, scan/index/idle/completion, timestamps, Relay status, and HTTP success;
- log/persistence snapshots proving the forbidden values and raw bodies never appear, plus proof that Cloud Relay and Trigger v1 receive no new fields or calls;
- fuzz/property tests for canonical decoders, signature domains, path confinement, state transitions, and arbitrary event ordering.

No runtime implementation may begin until all of these entry criteria are satisfied:

- exact visible namespace name, ownership manifest, collision UX, and user consent are approved;
- Docker/host/NAS path mapping and least-privilege helper read/write access are proven without arbitrary vault access;
- capability carrier/discovery and local endpoint exposure, if any, have an authenticated network design and rollback plan;
- a separate credential/pairing decision defines keys, trust bootstrap, multi-device authorization, storage, rotation, revocation, recovery, and loss;
- canonical request/response/signature schemas, TTL/skew/size/rate/concurrency limits, and folder bindings are frozen and independently security-reviewed;
- ignore, conflict, cleanup, tombstone, backup, and Syncthing versioning behavior are tested on supported deployments;
- Privacy Policy, UX/localization, operator documentation, compatibility matrix, and rollback runbook are approved before any artifact is created;
- contract/model tests above exist and the unchanged Relay v1 wire contract is regression-pinned.

## Result

An honest controlled roundtrip appears implementable in principle if fresh random artifacts traverse an exclusively app-owned synchronized namespace and mutually authenticated app/helper signatures bind both legs to one operation, folder, and homeserver. It cannot honestly claim exact network bytes, a direct transport peer, or per-block provenance.

Implementation is currently blocked by two unresolved safety properties: a collision-free, least-privilege namespace/access model across real helper deployments, and authenticated pairing/replay/recovery semantics. Existing Syncthing peer identity is not enough, so a new credential/pairing milestone is required.

The smallest defensible next runtime milestone is a dormant helper-first capability and credential/pairing foundation with no probe writes and no Relay/v1 change. After that foundation is approved and deployed, the smallest transfer milestone is a foreground-only upload attestation for one paired peer and one eligible `sendreceive` folder; download and roundtrip remain unset until a later separately reviewed milestone.

## Links

- [Decision 019 — Relay evidence stays layered](019-relay-proof-hierarchy.md)
- [Decision 020 — Sync-path proof requires correlated evidence](020-sync-path-proof-requires-correlated-evidence.md)
- [Architecture proof hierarchy](../architecture.md#relay-and-sync-proof-hierarchy)
- [Cloud Relay specification](../relay-spec.md)
- [Issue #91](https://github.com/psimaker/vaultsync/issues/91)

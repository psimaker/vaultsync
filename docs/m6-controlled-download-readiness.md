# M6 controlled-download readiness

**Status:** Unreleased app source. The controlled download leg of
[Decision 024](decisions/024-canonical-correlated-roundtrip-contract-and-threat-model.md)
is implemented against the published, unchanged helper 2.0.2 runtime. Causal
roundtrip remains unset and is a later, separately gated milestone. VaultSync
2.0 remains NO-GO.

## Scope

One explicit user tap with localized confirmation starts one operation for one
paired app/homeserver/folder/helper tuple. The upload leg is unchanged from the
M5 readiness boundary. This milestone adds only the app-side response leg:

- a signed type-6 response authorization is created only after the exact
  type-5 upload attestation was accepted for the still-active operation;
- the authorization is sent once over the fixed TLS-1.3/SPKI-pinned endpoint
  `POST /api/v1/diagnostics/authorize-response`; the 202 acknowledgement is
  transport diagnostics only and never evidence;
- before the authorization is sent, the app captures a fresh response baseline:
  the current bridge event cursor, the wall clock, and the engine generation
  from the exact preflight boundary;
- `download observed` is set only after a fresh successful `ItemFinished`
  apply of the exact expected response path — newer than the cursor and
  wall-clock baselines inside the unchanged engine generation — followed by a
  complete read of that exact file and full canonical, signature, key, epoch,
  binding, operation, digest, nonce, payload, and TTL validation of the type-7
  response artifact against the active request, attestation, and
  authorization;
- upload and download are separate evidence fields; roundtrip remains
  immutable false; cleanup stays evidence-orthogonal.

## Failure semantics

A response that exists before the baseline or authorization, arrives after an
engine restart or generation change, appears at any other path, or fails any
validation can never set download evidence. Invalid bytes at the exact
expected namespace path terminate the operation as a conflict. Cancellation,
view exit, refresh, app or engine restart, binding or credential change,
timeout, and rate limits stay terminal; after an accepted upload every
terminal outcome preserves the upload field and is presented as a partial
result that no late artifact can upgrade. An app restart destroys the active
correlation and nothing resumes.

## Compatibility and rollback

The helper wire surface is unchanged: helper 2.0.2 already implements the
dormant response foundation, and no helper, Relay, Trigger v1, APNs, or
StoreKit change ships with this milestone. Old or downgraded helpers yield
capability unavailable without fallback. App or helper rollback preserves
credentials, namespace authorization, opaque artifact copies, backups,
versions, conflicts, history, tombstones, mappings, and user data. The
request, attestation, and response artifacts are synchronized opaque files
and may remain in peers, backups, versions, conflict copies, and tombstones;
retained copies never regain validity.

## Verification

All Xcode results and derived data are outside the repository under `/tmp`.
The local gate for this milestone includes:

- production Swift response protocol bound to the cross-language
  `diagnostics-response-m6.json` golden vectors, including full-chain
  validation and per-byte tamper rejection;
- the M6 controlled-download runtime suite: stale pre-baseline responses,
  tampered artifacts at the exact path, engine-generation changes,
  cancellation during the download leg, and restart non-resumption never set
  evidence, while the exact fresh chain sets upload then download;
- the M5 foreground upload runtime suite re-run with the download leg,
  including partial-result preservation and rate limiting;
- `TestDiagnosticsDownloadThroughTwoEphemeralSyncthingInstances` in the
  isolated no-network Linux container: the exact upload chain propagates
  app→helper through real Syncthing, the real helper response foundation
  creates the one signed response artifact, those exact bytes propagate
  helper→app and validate through the full D024 chain, and a helper restart
  replays idempotently without rewriting the artifact;
- the complete iOS plan, a Release-configuration simulator build,
  design-token lint, string-key parity, and the sync-proof privacy lint.

The signed owner-device test was not executed — owner-approved
physical-device waiver (2026-07-15). Simulator and isolated local Syncthing
evidence substitute for it; no hardware keychain behavior, real APNs
delivery, real background waking, or TestFlight installation on hardware is
claimed, and simulator evidence is never described as real-device evidence.

Decision 024 remains the unchanged canonical contract. The next milestone may
derive the causal roundtrip only from this operation's upload and download
legs after this PR and its review/CI gates complete.

# M7 causal-roundtrip readiness

**Status:** Unreleased app source. The causal roundtrip derivation of
[Decision 024](decisions/024-canonical-correlated-roundtrip-contract-and-threat-model.md)
step 10 is implemented on top of the M5 upload and M6 controlled-download
legs. No new message type, endpoint, helper, bridge, Relay, or wire change
ships with this milestone. VaultSync 2.0 remains NO-GO until the release and
rollout gates complete.

## Derivation rule

`roundtrip confirmed` is set in exactly one place: the download acceptance of
the same active operation. That acceptance has already validated the request,
attestation, authorization, response signature, app/helper keys, epochs,
homeserver/folder bindings, operation ID, nonces, digests, payloads, and TTL
for one explicit tuple, so the roundtrip derives from exactly this
upload-then-download chain and from nothing else.

- No timestamp, HTTP status, Relay observation, APNs, scan/index/idle state,
  capability reachability, cleanup result, or tombstone can set it.
- A stale, replayed, copied, tampered, or foreign-operation response ends the
  operation without download or roundtrip evidence; a response artifact copied
  from another operation fails chain validation and terminates as conflict.
- Cancellation, restart, generation change, timeout, and rate limits keep the
  partial upload field visible and never derive a roundtrip.
- The claim is scoped causal propagation for one operation. It is never
  global sync health, future-delivery evidence, byte accounting, or a
  direct-peer claim.

## Compatibility and rollback

The helper wire surface stays byte-identical to helper 2.0.2; capability
negotiation, pairing, namespace, upload, and response behavior are unchanged.
Old or downgraded helpers yield capability unavailable without fallback. App
or helper rollback preserves credentials, namespace authorization, opaque
artifact copies, backups, versions, conflicts, history, tombstones, mappings,
and user data. Retained copies never regain validity and cannot derive a
late roundtrip.

## Verification

All Xcode results and derived data are outside the repository under `/tmp`.
The local gate for this milestone includes:

- the M5/M6 runtime suites re-run with the derivation: the exact fresh chain
  ends `roundtrip confirmed` with all three evidence fields set, and every
  stale, tampered, generation-changed, cancelled, restarted, rate-limited,
  and cross-operation-replay scenario keeps the roundtrip field false;
- the cross-operation replay property: a valid response artifact stolen from
  a completed operation and republished at a second operation's exact path
  is rejected by chain validation and ends as conflict;
- cross-language golden vectors with per-byte tamper rejection (unchanged);
- both isolated two-instance Syncthing E2E tests (upload and response
  transport with the real helper foundation), re-run at this head;
- the complete iOS plan, a Release-configuration simulator build,
  design-token lint, string-key parity, and the sync-proof privacy lint.

The signed owner-device test was not executed — owner-approved
physical-device waiver (2026-07-15). Simulator and isolated local Syncthing
evidence substitute for it; no hardware keychain behavior, real APNs
delivery, real background waking, or TestFlight installation on hardware is
claimed, and simulator evidence is never described as real-device evidence.

Decision 024 remains the unchanged canonical contract. Cleanup remains
evidence-orthogonal and a later milestone.

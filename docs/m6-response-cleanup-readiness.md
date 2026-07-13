# M6 dormant response and authenticated cleanup readiness

## Scope truth

M6 implements only the dormant, test-only Decision 024 helper foundation for
message types 6–9:

- app-signed `response_authorization`;
- helper-signed immutable `response_artifact`;
- app-signed `cleanup_request`; and
- helper-signed `cleanup_ack`.

It does not authorize or implement a listener, endpoint, advertised capability,
automatic discovery, trust adoption, namespace creation, App callsite,
download evidence, causal roundtrip, packaging, publication, deployment, or
rollout. Decision 024 is unchanged and remains the canonical contract.

## Implemented foundation

- The parser accepts only the exact deterministic-CBOR schemas, Ed25519
  signature domains, key IDs, epochs, homeserver/folder bindings, operation ID,
  digest chains, TTL, and clock-skew bounds for types 6–9.
- A helper response is possible only after the confined namespace contains the
  exact signed operation request and helper upload attestation and an app-signed
  authorization binds both message digests.
- The helper generates a fresh nonzero 32-byte response nonce and exactly 256
  random response payload bytes, signs their full causal digest chain, and
  atomically persists the immutable response before returning a fixed accepted
  in-process result.
- The exact persisted response wins across duplicate calls, concurrency, and
  helper restart. A different authorization cannot overwrite or reuse it.
- Cleanup accepts one to three sorted, unique message digests. The helper maps
  them only against the three fixed operation artifact paths; the request never
  supplies a path.
- Every present cleanup candidate is reopened through the M4 confined handle
  and checked for canonical content, signature, tuple, operation, file identity,
  and digest immediately before deletion.
- Helper-authored attestation/response artifacts may be removed after the exact
  app-signed cleanup request. A live app-authored request is retained; the
  helper may remove it only after expiry plus the allowed clock-skew boundary.
- An expired cleanup request cannot mutate the namespace: it is rejected before
  candidate reads or deletions because no live acknowledgement can be issued.
- An unambiguous missing exact artifact returns signed `already_absent`. Changed, invalid,
  symlinked, hard-linked, swapped, or otherwise unverifiable files are retained
  as conflict. Root, README, manifests, authorizations, credentials, parent
  paths, user files, backups, versions, and tombstones are never targets.
- A crash after deletion but before the acknowledgement is safe: an explicit
  retry returns `already_absent`. There is no background scan, permanent poll,
  separate operation database, or cleanup-derived evidence.
- This foundation does not schedule the app's fixed cleanup retry delays or a
  helper startup scan; both remain part of a later runtime milestone.
- Response authorization and cleanup share the existing bounded per-app and
  helper-wide direct-request coordinator, including invalid bodies.

## Strongest proof

The strongest M6 proof is local/test-only: an exact M5 request and upload
attestation are read through the M4 boundary; an exact app-signed authorization
causes one helper-signed 256-byte response to be atomically persisted; an exact
signed cleanup request removes only the validated helper-owned artifacts and
returns a signed per-target acknowledgement. Crash, restart, replay, conflict,
rate, tuple, and two-instance races remain fail closed.

This proves no App download. The response has not traversed Syncthing back to an
iPhone, no post-authorization cursor/nanosecond/generation baseline exists, and
no exact `ItemFinished` plus local response verification has occurred. Download
and roundtrip therefore remain unset.

## Compatibility and privacy

- Existing app/helper and Trigger v1 behavior are unchanged.
- No runtime entrypoint constructs the foundation.
- No contract identifier, key, binding, operation, nonce, digest, payload,
  result, or cleanup state enters Relay, APNs, StoreKit, logs, telemetry, crash
  annotations, support bundles, UserDefaults, Keychain, or a durable helper
  operation store.
- Test fixtures contain deterministic test keys and random-looking test payloads
  only. They are not production credentials.
- Live cleanup cannot erase backup, Syncthing version, remote-history, or
  tombstone copies. Such opaque retention remains disclosed and never becomes
  valid evidence again.

## Rollback and next gate

M6 is dormant and has no runtime artifacts. Source rollback is a normal code
revert and changes no credentials, mappings, namespace roots, Trigger v1, Relay
wire, or user data.

The next separately owner-gated milestone remains helper runtime and packaging
readiness. Until its fixed endpoints, TLS/SPKI/application authentication,
explicit configuration, least-privilege packaging, upgrade, downgrade, and both
rollback directions are proven, no helper publication or App runtime is
authorized.

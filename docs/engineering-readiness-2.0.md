# Engineering readiness audit — VaultSync 2.0

**Scope:** Final engineering-readiness audit for the 2.0.0 release train,
covering the Decision 021–025 diagnostics milestones, the helper 2.0.2
publication, the Relay observability rollout, and the app release gates.
This audit records evidence states; it grants no release by itself. The
release decision follows the separate release-readiness checklist and the
staged-rollout runbook below.

## Decision 021–025 gap matrix

| Decision | Requirement | State |
|---|---|---|
| 021 — capability-negotiated contract | Two independent cryptographically bound evidence legs for one active operation; no global flag; weaker evidence never fills a stronger field | Implemented: M5 upload attestation and M6 controlled download; M7 derives the roundtrip only from both acceptances of one operation. Claim language matches the 021 table including every "explicitly not claimed" limit. |
| 022 — credentials and mutual pairing | Explicit QR/paste pairing, Ed25519 identities, epochs, rotation, revocation, recovery; no bootstrap from StoreKit/APNs/Relay/folder possession | Implemented and released source-side (M3 control plane); device replacement requires re-pairing; no automatic trust or recovery. |
| 023 — namespace and least privilege | Visible `VaultSync Diagnostics` namespace, operator-created, helper confined to it; no vault-wide access; no automatic Syncthing configuration change | Implemented; helper runtime confined via bind mount and ACL model proven for the Docker host-bind deployment row; collisions are never adopted. |
| 024 — canonical contract and threat model | Deterministic CBOR subset, Ed25519 domains, field registry, TTL/rate/size limits, evidence state machine, failure/threat matrices, test plan | Implemented for types 1–7 (capability, pairing, request, query, attestation, authorization, response). Cleanup types 8/9 exist dormant helper-side only. Decision 024 text unchanged at blob `f41f597d3ceca73da102e5e447382dfae07d2e08`. |
| 025 — owner approval of design gates | Owner approved D022.1–D024.7 choices; gates binding; skipping a gate needs a new explicit owner decision | Recorded 2026-07-12. The physical-device evidence gate was explicitly owner-waived on 2026-07-15 for this completion run; the waiver and its substitute evidence are recorded in each milestone readiness document and below. |

## Evidence state — per leg, never merged

| Leg | State | Strongest proof | Not claimed |
|---|---|---|---|
| Upload | Implemented, unreleased | Exact type-5 paired-helper attestation over the pinned channel for the active request/query, after full tuple/namespace/signature/digest/TTL/engine/folder/peer rechecks | Byte counts, direct transport, per-block peer provenance |
| Download | Implemented, unreleased | Fresh post-authorization `ItemFinished` apply of the exact response path inside an unchanged engine generation, plus complete type-7 chain validation | Direct helper-to-iPhone route, APNs, background execution, block provenance |
| Roundtrip | Implemented, unreleased | Derived solely from the two acceptances above for one operation; scoped causal propagation | Global sync health, future delivery, byte accounting, direct peer |
| Cleanup | App workflow **not implemented** (deliberate later milestone); helper foundation implemented, tested, dormant, idempotent | Helper-side bounded authenticated cleanup with signed acknowledgements exists but no app carrier calls it | Any evidence effect — cleanup is evidence-orthogonal by contract |

Cleanup residue assessment: each explicit check leaves at most three immutable
opaque files (≈1–2 KiB each) in the diagnostics namespace, capped by the hard
rate limits (three starts/hour, twelve/day per folder). The retention is
user-visible, disclosed in `PRIVACY.md` and the consent flow, and bounded;
retained copies never regain validity. This is accepted for 2.0 and an app
cleanup carrier remains a separately gated milestone.

## Compatibility matrix

| App | Helper | Relay | Result |
|---|---|---|---|
| Released 1.8.2 | Old helper (<2.0) | Existing Relay (any) | Existing behavior only; Trigger v1 unchanged; no diagnostics state. |
| Released 1.8.2 | Helper 2.0.2 | New Relay (1.3.0+) | Helper stays dormant for the old app; Trigger v1 and Relay v1 byte-identical; no artifact, key, or namespace. |
| 2.0.0 app | Old helper (<2.0) | Any Relay v1 | `Capability unavailable`; no fallback, probe, namespace creation, or v1 change. |
| 2.0.0 app | Helper 2.0.2 | Old Relay (pre-observability) | Diagnostics control plane and transfer legs fully available (Relay is never a carrier); Relay waiting-state shows no helper last-seen data and never fabricates it. |
| 2.0.0 app | Helper 2.0.2 | New Relay (1.3.0+) | Full surface: diagnostics evidence legs plus StoreKit-verified Relay observability, with Relay observation, local wake-up, and sync progress kept as separate honest states. |
| 2.0.0 app downgraded to 1.8.x | Helper 2.0.2 | Any | Old app ignores additive records; helper dormant for it; credentials, namespace, opaque copies, and user data preserved. |
| 2.0.0 app | Helper downgraded to 1.8.x | Any | Capability becomes unavailable; no new operation, no fallback success, no deletion of credentials, root, or mappings. |
| Helper re-upgrade | 2.0.0 app | Any | Pairing epochs, namespace ownership, and capability revalidate from scratch; no old proof resumes. |

Wire review: Trigger v1, Relay v1 provisioning/status, APNs payloads, StoreKit
products, and folder/device mappings are byte-for-byte unchanged across all
rows. The diagnostics contract is additive, capability-gated, and fails closed
on unknown versions, suites, or flags. Cloud Relay is never a carrier or
observer of diagnostics evidence.

## Security, privacy, and secret review

- All diagnostics messages are deterministic-CBOR, domain-separated Ed25519,
  with re-encode equality, bounded allocation, and fail-closed unknown-field
  handling; cross-language golden vectors pin the byte contract and per-byte
  tamper loops reject mutations.
- The pinned transport allows exactly six fixed paths, TLS 1.3 with SPKI pin,
  no identifiers in URLs, fixed error categories, no raw-body logging.
- The sync-proof privacy lint, forbidden-sink scan (now covering the
  controller and response protocol), and log-sanitization suites pass; no
  operation value reaches UserDefaults, Keychain, logs, telemetry, crash
  reports, Relay, APNs, or StoreKit.
- Secret scan (GitGuardian) green on every PR in the train; no secret, token,
  or signing material is committed; helper release assets ship with SHA-256
  digests and SBOM.
- Failure semantics reviewed against the D024 matrices: conflict is reserved
  for unexpected authenticated namespace content, unsupported for
  authenticated protocol mismatches, unavailable/interrupted for
  capability/runtime loss, with partial upload preservation after acceptance.

## Existing-user, migration, and rollback review

Existing users upgrading to 2.0.0 see no behavior change without explicit
action: launch, Settings inspection, Relay/APNs activity, and ordinary or
background sync create no key, pairing, trust, namespace, peer, share,
artifact, rescan, or configuration change. There is no persisted-state
migration in the app release; all diagnostics state is additive and scoped to
the dedicated Keychain service and namespace artifacts. Rollback in every
direction (app, helper, relay) preserves credentials, namespace root,
authorization records, opaque copies, backups, `.stversions`, conflict
copies, remote history, tombstones, mappings, and user data. Forward
recovery always starts from a fresh capability check and never resumes an
old proof.

## Test and evidence inventory (fresh at the audited heads)

- Complete iOS plan: 431 tests / 438 parameterized runs, zero failed or
  skipped, on the iPhone 17 Pro simulator at each merge head of #126–#128.
- Release-configuration simulator builds at each merge head.
- M5/M6/M7 runtime suites: exact chains, stale/tampered/generation-changed/
  cancelled/restarted/rate-limited boundaries, cross-operation replay,
  byte-identical retransmission, TTL and poll exhaustion, partial-result
  preservation.
- Cross-language golden vectors (contract, pairing, namespace, upload,
  response) with fuzzed decoder inputs and per-byte tamper rejection.
- Two isolated two-instance Syncthing E2E tests (upload and response legs)
  in a no-network, read-only, cap-dropped container, driving the real helper
  attestor and response foundation with byte-exact namespace verification and
  idempotent restart replay — re-run at each milestone head.
- Go bridge suite against real Syncthing instances; notify suite with vet
  and gofmt; design-token, strings-parity (884 keys), privacy, and plist
  lints; all remote CI, security, and publish-safety gates green on every
  PR and post-merge run.
- Physical-device evidence: **not executed — owner-approved physical-device
  waiver (2026-07-15)**. No hardware keychain behavior, real APNs delivery,
  real background waking, or TestFlight installation on hardware is claimed
  anywhere in the train; simulator evidence is never described as
  real-device evidence.

## Helper and relay rollout / downgrade matrix

| Component | Forward | Backward | Evidence |
|---|---|---|---|
| Helper 2.0.2 (`notify-v2.0.2`, source `42cadb2`) | Installer upgrade old→new: pass | Installer downgrade new→old: pass; forward recovery old→new: pass | Immutable publication with index digest `sha256:97d866c5…39a9d`, per-binary SHA-256 sums, SBOM, and recorded rollout evidence; credential state byte-identical across the cycle; TLS SPKI stable across forward recovery. |
| Relay (target: current `main` with observability + sanitized APNs logs) | Server-side image rebuild from verified commit, with pre-deploy backup tag | `docker compose` rollback to the tagged backup image | Production currently healthy on 1.3.0; deploy and rollback follow the established repo procedure with health/log verification. |
| App 2.0.0 / build 36 | App Store staged (phased) release | Phased-release halt; users who installed keep a safe dormant surface; no server-side dependency regresses 1.8.2 users | Release gates in the release-readiness checklist; abort criteria below. |

Rollout order is binding: helper (published) → relay (verified deploy) →
app (staged). Each direction of helper and relay rollback was proven or is
procedurally covered before the app ships.

## App staged-rollout and abort plan

1. Submit 2.0.0 (build 36) with phased release enabled.
2. Monitoring window: Relay health endpoint, Relay APNs error logs, crash
   feedback via App Store sources, and support channels.
3. Abort criteria — any of: a data-safety regression report, a reproducible
   crash loop in the release build, Relay health degradation attributable to
   the new app, or a diagnostics flow creating artifacts without explicit
   consent. On abort: halt the phased release immediately; the shipped app's
   diagnostics surface stays dormant without pairing, so no server-side
   rollback is required for existing users; helper and relay rollbacks
   follow their matrices independently.
4. No abort path deletes credentials, namespace roots, artifacts, backups,
   versions, tombstones, or user data.

## Audit result

All Decision 021–025 gates required for the 2.0 release train are satisfied
or explicitly owner-waived (physical-device evidence) with recorded
substitute evidence. The app-side cleanup carrier is deliberately deferred
and its residue is bounded and disclosed. No global success claim is made
anywhere stronger than the per-leg evidence above. Remaining before release:
the release-readiness checklist (version/build bump, changelog, store
metadata, XCFramework rebuild, full verification), the relay production
deploy, and the staged app rollout with its monitoring window.

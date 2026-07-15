# M5 foreground upload-only readiness

## Status and claim boundary

M5 implements the owner-authorized, explicit foreground upload leg of Decision
024 in unreleased app source. It uses the already published helper 2.0.2
runtime. It does not implement controlled download, causal roundtrip, app-side
authenticated cleanup, background execution, automatic discovery, or any
Relay carrier. VaultSync 2.0 remains **NO-GO** until the later milestones and
release gates complete.

| Evidence field | M5 state | Strongest permitted claim |
|---|---|---|
| Upload | Implemented in production app source; local product, cross-language, Linux helper, and isolated two-Syncthing-instance tests pass | Fresh app-authored random request bytes became readable to the exactly paired helper in the selected logical folder only after the app accepts the exact helper-signed attestation for its active byte-identical pinned query. |
| Download | Unset | No response authorization, response baseline, response-file acceptance, or fresh exact `ItemFinished` exists in this milestone. |
| Roundtrip | Unset | No same-chain controlled download exists, so no roundtrip can be derived. |
| Authenticated correlation | Implemented for upload only | Exact app/helper keys and epochs, homeserver/folder binding, namespace authorization, operation, request/payload/query digests, nonces, TTL, signatures, and active query are validated. |
| Cleanup | Evidence-orthogonal helper foundation only | M5 neither derives evidence from cleanup nor adds the later app cleanup workflow. Live and retained copies may remain. |

There is no global success flag. Upload, download, and roundtrip are separate
fields. A helper signature proves helper authorship and the signed causal
bindings, not a transport route, direct peer, exact network bytes, block
provenance, future delivery, or global sync health.

## Explicit product flow

The only product entry point is the Controlled Diagnostics view. Opening the
view, installing or upgrading the app, checking capability, receiving a Relay
wake-up, or running ordinary/background sync creates no operation. One upload
operation requires a separate user tap and a localized confirmation that
discloses the 256 random bytes and possible retained opaque copies.

Before creating an artifact the app requires all of the following:

- one active D022 pairing and current D023 namespace authorization;
- a fresh helper-signed capability response with all Decision 024 flags;
- the exact settled existing folder mapping selected during pairing;
- exactly one designated, connected, unpaused peer;
- an unpaused, healthy `sendreceive` folder with no path overlap;
- a running unchanged embedded Syncthing engine generation;
- the real Syncthing ignore matcher allowing the fixed namespace and all three
  exact operation filenames;
- existing, non-symlink fixed namespace directories and no request,
  attestation, or response collision for the fresh operation; and
- the tuple, process-concurrency, hourly, daily, and direct-request limits.

Failures before that boundary create no artifact. The preflight is read-only:
it never adds a peer/share/folder, creates a namespace, changes mode, pause
state, paths, ignores, discovery, trust, or Syncthing configuration.

The active operation then:

1. generates a fresh nonzero 32-byte operation ID, request nonce, query nonce,
   and exactly 256 random payload bytes in memory;
2. signs deterministic Decision 024 types 3 and 4 and derives the request
   filename internally as lowercase unpadded base32;
3. walks the already existing namespace with descriptor-relative
   `O_NOFOLLOW` opens, creates the request with `O_EXCL`, writes and fsyncs the
   exact bytes, verifies regular-file identity, single link, size, and bytes,
   then fsyncs the parent;
4. requests one rescan of only the already selected folder;
5. retransmits the one signed query byte-for-byte on at most eight fixed polls
   (`2, 4, 8, 16, 30, 60, 120, 120` seconds);
6. before every poll revalidates the complete persisted pairing record, engine,
   folder, peer, mapping, ignores, authenticated namespace, and exact persisted
   request bytes; and
7. sets only `upload observed` after the pinned TLS endpoint returns an exact
   canonical helper-signed type-5 attestation binding the active request,
   payload digest, query nonce/digest, keys, epochs, homeserver/folder, operation,
   and active clock window.

HTTP 200/202, reachability, request creation, rescan, index activity, folder
completion, timestamps, `idle`, and a synchronized attestation copy are not
upload evidence. The product path contains no Decision 024 type 6–9 domains.

## Lifecycle, limits, and terminal truth

Active correlation is memory-only. Leaving the view, explicit cancellation,
controller refresh, app restart, engine-generation change, mapping/peer/mode/
ignore/access change, credential transition, protected-data loss, timeout, or
conflict ends the operation. A run token prevents a cancelled or pre-refresh
task from writing into later controller state. Cancellation is rechecked after
every asynchronous endpoint call, so a late valid attestation cannot upgrade a
terminal operation.

The app enforces one active operation per exact
app/homeserver/folder/helper/key/namespace-authorization tuple, two process-wide,
three starts/hour and twelve/day per app/folder while the process remains
alive, thirty direct requests/minute per paired record, and 120/minute
process-wide. The helper independently enforces the same per-app/folder start
windows plus its sixty-start/day and eight-active-operation limits. No network
input can raise a limit.

The request is immutable and is deliberately not removed on an ambiguous
failure. A crash after its exclusive publication can therefore leave a valid
or partial collision; a later operation uses a fresh ID and never resumes that
proof. The helper rejects incomplete or conflicting bytes. This is safer than
overwriting or broadly deleting an unverified synchronized entry.

## Compatibility and existing-user behavior

The contract is additive and Trigger v1/Relay v1 remain unchanged.

| App | Helper | Result |
|---|---|---|
| Released old app | Old helper | Existing sync and Relay behavior only. |
| Released old app | Published helper 2.0.2 | Helper remains dormant for the app; no operation or namespace is created automatically. |
| M5-capable app | Old or disabled helper | `Capability unavailable`; no artifact, fallback, or weaker evidence. |
| M5-capable app | Enabled helper 2.0.2 but unpaired/unauthorized | Explicit pairing/namespace guidance only; no artifact or trust adoption. |
| M5-capable app | Paired and namespace-authorized helper 2.0.2, ineligible folder/peer | `Unsupported` or `unavailable`; ordinary sync is unchanged. |
| M5-capable app | Paired and namespace-authorized helper 2.0.2, exact eligible target | A user may start one foreground upload-only operation. Download and roundtrip stay unset. |
| App downgrade | Helper 2.0.2 | Old app ignores additive state; no operation resumes and no credential, namespace, mapping, or user data is deleted. |
| Helper downgrade | M5-capable app | Capability becomes unavailable; no new request and no fallback success. |
| Re-upgrade | Capable app/helper | Fresh capability, pairing epochs, mapping, and namespace authorization are revalidated; no old operation resumes. |

An existing-user app upgrade performs only the prior read-only credential
inspection when the user opens Controlled Diagnostics. It does not create a
key, pair, trust, namespace, peer, share, artifact, rescan, or configuration
change without the explicit actions above.

## Namespace, retention, privacy, and rollback

Operation IDs, nonces, random payloads, digests, message bytes, per-operation
status, and evidence are not written to UserDefaults, Keychain, logs,
telemetry, crash annotations, support bundles, Relay, APNs, StoreKit, or durable
diagnostic history. Pairing records retain no operation state. Fixed endpoint
paths contain no identifier.

The request and the helper's attestation are visible synchronized files in
`VaultSync Diagnostics`. Peers, backups, `.stversions`, conflict copies,
remote history, deletion records, and tombstones may retain opaque copies after
TTL, cancellation, rollback, or live cleanup. Such copies never regain
validity and cannot set evidence. App rollback stops the entry point but does
not delete credentials, authorization history, namespace content, mappings, or
user data. Helper rollback makes capability unavailable and likewise preserves
those objects. Forward recovery starts from a fresh capability check and never
from an old operation.

## Verification

All Xcode results and derived data are outside the repository under `/tmp`.
The current local gate includes:

- `cd go && go test -tags noassets ./bridge -count=1`: passed, including the
  real Syncthing ignore/access/collision preflight;
- `go test ./... -count=1` in `notify/` on macOS: passed;
- the same complete Notify suite in a digest-pinned, read-only Linux container
  with no network and all capabilities dropped: passed;
- `TestDiagnosticsUploadThroughTwoEphemeralSyncthingInstances` in that isolated
  Linux container: passed with two fresh Syncthing homes, exact request
  propagation, confined helper read, persisted exact signed attestation, pinned
  mock-channel upload acceptance, and rejection of the synchronized
  attestation copy as upload evidence;
- focused production Swift suites on an iPhone 17 Pro simulator: 12 passed,
  zero failed/skipped;
- the complete iOS test plan on that simulator: 429 tests / 436 parameterized
  test runs passed, zero failed/skipped; and
- generic iOS Simulator product build, a Release-configuration simulator
  build, design-token lint, localized string-key parity, and localized plist
  validation: passed.

The Swift product runner tests cover exact Go golden bytes, canonical and
signature tampering, exclusive/symlink/hard-link/collision handling, explicit
preflight, byte-identical polling, upload-only evidence, late response after
cancellation, refresh non-resumption, three/hour rate rejection before a fourth
write, finite eight-poll timeout, and no artifact/request after an ambiguous
peer preflight.

The signed owner-device test was not executed — owner-approved
physical-device waiver (2026-07-15). For the remaining 2.0 completion run the
owner explicitly replaced the real-device merge gate with fresh exact-head
substitute evidence: the complete iOS plan and the focused upload suites
re-run on the iPhone 17 Pro simulator, a Release-configuration simulator
build, and the isolated two-instance Syncthing E2E above. No hardware
keychain behavior, real APNs delivery, real background waking, or TestFlight
installation on hardware is claimed; simulator evidence is never described as
real-device evidence.

Decision 024 remains the unchanged canonical contract. The next milestone may
add controlled download only after this upload-only PR, its review/CI gates,
and its owner-waived substitute evidence gates are complete. Roundtrip
remains a still later, separate derivation.

# App capability, pairing, and namespace readiness

**Status:** App source readiness for the explicit Decision 022/023 control
plane is implemented and locally verified. It is not an App Store release or a
transfer milestone. The published helper baseline is `notify-v2.0.2`; the app
change remains unreleased until its own PR and later release gates complete.
This document records the M3 control-plane boundary. The later unreleased M5
source adds only the explicit foreground upload leg documented in
[M5 foreground upload-only readiness](m5-upload-attestation-readiness.md);
download and roundtrip remain unset.

## User-controlled scope

Controlled Diagnostics is a separate Settings surface. Opening it performs one
read-only inspection of protected app storage and the dedicated diagnostics
Keychain service. An app upgrade, launch, settings visit, ordinary sync, Relay
wake-up, or background run does not create a key, marker, pairing, endpoint
request, namespace, artifact, Syncthing share, peer, trust decision, or folder
configuration.

The first mutation requires all of the following explicit actions:

1. The user selects one already configured homeserver Device ID and one folder
   already shared with that device.
2. The user accepts localized pairing consent.
3. The user scans or pastes an operator-generated, five-minute D022 invitation.
4. The app validates the exact target digests, fixed endpoint, helper key, TLS
   SPKI pin, canonical CBOR, HMAC, signature suite, nonce, epoch, and clock.
5. The app and local operator compare the same 12-hex transcript fingerprint.
6. Only the user's confirmation advances the persisted types 3, 5, and 7.

No helper is discovered automatically. The app never uses mDNS, UPnP, public
port defaults, Cloud Relay, APNs, Syncthing discovery, or a Relay tunnel for
this control plane. It never creates or adopts Syncthing trust, a peer, a share,
or a namespace.

## Credential and transport boundary

The app stores only its diagnostics installation seed and scoped pairing
records in the dedicated generic-password service
`eu.vaultsync.app.diagnostics.v1`. Items are non-synchronizable and use
`WhenUnlockedThisDeviceOnly`; no shared access group or cloud escrow is used.
A separate complete-protection marker binds the Keychain item to this app
container. A missing or mismatched half is `re-pair required`, never silent key
adoption.

Each record is scoped to the app installation, homeserver binding, folder
binding, helper, TLS pin, and current app/helper epochs. It retains the exact
latest outgoing/incoming control bytes needed for byte-identical retry, but no
QR secret, folder path, folder/vault name, note content, user filename,
operation payload, upload/download result, or proof history.

The fixed local/LAN/VPN endpoint uses an ephemeral URL session with TLS 1.3 as
both minimum and maximum, an exact P-256 leaf-SPKI SHA-256 pin, no redirects,
cookies, cache, compression, query, or fragment, fixed CBOR media types and
body limits, and mutually authenticated application signatures. Network
errors become `capability unavailable`; authenticated protocol, tuple, or
mandatory-flag mismatches become `unsupported`. Neither state falls back to a
weaker success. M3 accepts only its four fixed pairing, capability,
namespace-enablement, and namespace-authorization paths. The separate M5
source additionally permits only the fixed Decision 024 attestation path; it
does not permit response-authorization or cleanup calls. A successful
capability response can authorize the next explicit action only through its
exact signed expiry; it is invalidated on restart, error, or credential
transition.

Every persisted pending D022/D023 operation also carries an app-local
`mach_continuous_time` deadline bound to its signed wall-clock window. Restart
reconstruction requires both clocks to remain within the original interval;
rolling the wall clock back cannot extend a pairing, namespace, rotation, or
revocation attempt beyond five elapsed minutes. Completed immutable namespace
records remain separately verifiable after that local network-attempt deadline.

App-key, helper-key, and TLS-pin changes are explicit signed D022 transitions.
The old credential remains authoritative until the terminal acknowledgement
and an exact capability response under the proposed state both validate. A
new app-key generation is selected once in the installation Keychain and reused
for every separately staged folder authorization. Another generation is blocked
until every non-revoked authorization is stable on that selected key; there is
no cross-folder atomicity claim. A
pre-commit transition can be explicitly aborted with signed types 23/24; an
expired pre-commit transition can be discarded only after its signed expiry
and clock-skew window. A type-21 finalization that may have reached the helper
is never silently rolled back. A
completed credential change makes an existing namespace unavailable until the
app and helper append its next immutable D023 authorization epoch. Revocation
is scoped to this app authorization. Lost-key recovery removes only this app's
local diagnostics records and instructs the operator to revoke the surviving
helper authorization separately.

## Separate namespace enablement

Pairing and capability checks create no synchronized content. Namespace
enablement is a second explicit app action and remains only a signed request
until the helper operator separately runs the supported installer, confirms
the exact existing folder/path, and accepts visibility and retention.

The app never creates or adopts `VaultSync Diagnostics`. After the operator
step, it reads only fixed D023 paths beneath the app's existing settled folder
through descriptor-relative, `O_NOFOLLOW` opens. It requires regular,
single-link, size-bounded immutable files, validates the root/helper epoch
chain, and sends the exact app-signed authorization candidate to the pinned
helper. The namespace becomes active only after the helper-countersigned file
arrives through Syncthing and validates against that exact candidate. Rotation
uses append-only authorization epochs 2 through 9.

The visible namespace and its opaque records can remain on peers, in backups,
Syncthing versions, conflict copies, remote history, and tombstones. Disabling,
revoking, downgrading, or resetting app credentials does not delete those
copies, the namespace root, helper state, a share, or user data.

## Compatibility matrix

The diagnostics contract is additive. Trigger v1 and Relay v1 are unchanged.

| App | Helper | Relay | Honest result |
|---|---|---|---|
| Released old app | Old helper | Existing Relay | Existing behavior only; no diagnostics state. |
| Released old app | Published helper 2.0.2 | Existing or new Relay | Helper remains dormant unless separately configured; the old app makes no diagnostics calls. |
| M3-capable app | Old helper | Any Relay v1 | `Capability unavailable`; no pairing fallback, namespace, or artifact. |
| M3-capable app | Helper 2.0.2, diagnostics unset | Any Relay v1 | `Capability unavailable`; Trigger v1 remains unchanged. |
| M3-capable app | Helper 2.0.2, enabled but unpaired | Any Relay v1 | Explicit QR pairing is offered; no trust or namespace is inherited. |
| M3-capable app | Helper 2.0.2, paired but namespace absent | Any Relay v1 | Authenticated capability can succeed; upload, download, and roundtrip remain unset. |
| M3-capable app | Helper 2.0.2, explicitly namespace-authorized | Existing or new Relay v1 | D022/D023 control plane active; no transfer artifact exists in this milestone. |
| App downgrade | Helper 2.0.2 | Any Relay v1 | Old app ignores the additive records; helper stays dormant for it; credentials and namespace copies are retained. |
| App re-upgrade | Helper 2.0.2 | Any Relay v1 | Read-only reconstruction, fresh capability, and current namespace authorization are required; no operation resumes. |

## Evidence boundary

The strongest app-side proof in this milestone is exact production-code
decoding of all D022 types 0–24, byte-exact D024 capability-query generation,
mutually signed capability-response validation, exact D023 golden-chain
validation/generation, device-only credential persistence, and a restart-safe
explicit pairing state machine. This is control-plane evidence only.

| Claim | State |
|---|---|
| Authenticated capability | Implemented in production app source; cross-language vectors and a deterministic pinned-transport harness pass. Real-device/helper deployment evidence remains unset. |
| Pairing | Explicit, fingerprint-confirmed, scoped, restart-safe D022 state machine. |
| Namespace | Explicit app request plus separate operator creation and helper-countersigned D023 authorization. |
| Upload | Unset; no request artifact is created by this milestone. |
| Download | Unset; no response artifact or fresh `ItemFinished` baseline exists. |
| Roundtrip | Unset; no same-chain directional evidence exists. |
| Cleanup | No app cleanup runtime in this milestone; helper foundation remains evidence-orthogonal. |

Signatures prove authorship and exact causal bindings. They do not prove a
transport route, direct peer, byte provenance, future delivery, or global sync
health.

## Local verification

All Xcode result bundles must be written below `/tmp`. The milestone gate runs:

```sh
cd ios
xcodegen generate
./scripts/strings-key-parity.sh
xcodebuild -project VaultSync.xcodeproj -scheme VaultSync \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro,OS=26.5' \
  -derivedDataPath /tmp/vaultsync-m3-derived CODE_SIGNING_ALLOWED=NO test \
  -resultBundlePath /tmp/vaultsync-m3-tests.xcresult
```

The focused runtime suite covers production D022/D023/D024 golden vectors,
canonical parser rejection, signature/mandatory-flag tampering, exact target
bindings, endpoint literal canonicalization, device-only Keychain query
attributes, existing-user no-mutation, explicit fingerprint gating, persistence,
restart reconstruction, capability expiry, namespace/operator state isolation,
app/helper/TLS rotation, signed pre-commit abort, revocation, persisted monotonic
deadlines with wall-clock rollback, fixed transport paths, arithmetic boundaries,
and honest unavailable/unsupported states. The
pre-PR local run passed all 424 iOS tests; Go bridge and Notify suites, Go Vet,
design-token lint, strings parity, and localized plist lint also passed. These
results are local engineering evidence, not real-device, rollout, or Store
evidence.

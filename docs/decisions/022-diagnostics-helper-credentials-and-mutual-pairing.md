# 022 — Diagnostics helper credentials and mutual pairing

**Status:** Proposed design; not implemented, not independently reviewed, and not approved for runtime use. Human security and product approval is required. This decision authorizes no endpoint, key, pairing record, namespace, probe, Relay change, installer permission change, or rollout.

## Scope and hard invariants

This decision supplies the credential and pairing milestone required by [Decision 021](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md). It applies only to a future diagnostics capability. Existing Syncthing pairing, Trigger v1, Cloud Relay provisioning/status, APNs, StoreKit, folder mappings, and the published helper remain unchanged.

The invariants, bootstrap prohibitions, and human-approval choices in this document bind only the diagnostics capability. They neither authorize nor constrain any future, separately decided capability (for example, one that introduces devices or provisions folders on the homeserver); such a capability requires its own decision with its own threat model, and this document is not precedent for or against its design choices.

- A Syncthing Device ID is a local binding input, not a helper credential.
- StoreKit JWS, APNs tokens, Relay registrations, API keys, and possession of a synchronized folder cannot bootstrap helper trust.
- Pairing is explicit, mutually authenticated, scoped to one app installation, one homeserver, and selected folders, and is never inferred or repeated automatically.
- An old, unpaired, unreachable, downgraded, or revoked helper yields `capability unavailable` or `re-pair required`; it never creates a general sync error or weaker proof.
- Pairing alone creates no synchronized namespace and no upload, download, or roundtrip evidence.

## Cryptographic suite

The first protocol version has one suite and no algorithm negotiation. An unknown suite or version fails closed.

| Purpose | Proposed primitive | Rule |
|---|---|---|
| App request signatures | Ed25519 | One random 32-byte seed per app installation; never reused for TLS, Syncthing, Relay, or StoreKit. |
| Helper protocol signatures | Ed25519 | One random 32-byte seed per helper installation; separate from the app and TLS keys. |
| Pairing TLS identity | ECDSA P-256 certificate key | TLS 1.3 only; the app pins the SHA-256 digest of the DER SubjectPublicKeyInfo delivered out of band. Certificate renewal may keep the pinned key; key rotation follows the explicit rotation flow below. |
| One-time pairing proof | HMAC-SHA-256 | A cryptographically random 32-byte secret, single use, five-minute maximum lifetime. |
| Digests and key IDs | SHA-256 | Full 32-byte output; no truncation for protocol identifiers. |
| Nonces and opaque bindings | CSPRNG | 32 bytes each; never derived from time, identifiers, paths, accounts, or transactions. |

Ed25519 is available in Go's standard library and as `Curve25519.Signing` in CryptoKit. The iOS key is stored as opaque generic-password data because CryptoKit's Curve25519 signing key is not a native `SecKey`. The design follows [RFC 8032](https://www.rfc-editor.org/info/rfc8032), [TLS 1.3](https://www.rfc-editor.org/info/rfc8446), and Apple's [CryptoKit Keychain guidance](https://developer.apple.com/documentation/cryptokit/storing-cryptokit-keys-in-the-keychain).

## Canonical pairing encoding and signature domains

Pairing and credential-lifecycle messages use the RFC 8949 core deterministic CBOR encoding. The accepted subset is definite-length maps with unsigned-integer labels, unsigned integers, byte strings, and restricted ASCII text constants. Floats, tags, indefinite lengths, duplicate keys, non-shortest integers, unknown fields, invalid UTF-8, and non-deterministic map order are rejected before signature verification. A decoder re-encodes and byte-compares the accepted body. Maps have at most 32 entries, arrays at most eight entries, nesting depth at most four, and encoded/decoded bodies at most 16 KiB.

Every signature input is the exact ASCII domain including its trailing NUL byte, followed by the deterministic CBOR body without the signature field:

| Message | Signature domain |
|---|---|
| App pairing request | `eu.vaultsync.helper-pairing/v1/app-request\0` |
| Helper pending pairing acceptance | `eu.vaultsync.helper-pairing/v1/helper-accept\0` |
| App pairing finalize | `eu.vaultsync.helper-pairing/v1/pairing-finalize\0` |
| Helper finalize acknowledgement | `eu.vaultsync.helper-pairing/v1/pairing-finalize-ack\0` |
| App pairing receipt | `eu.vaultsync.helper-pairing/v1/pairing-receipt\0` |
| Helper ready acknowledgement | `eu.vaultsync.helper-pairing/v1/pairing-ready-ack\0` |
| App activation confirmation | `eu.vaultsync.helper-pairing/v1/pairing-activate\0` |
| Helper active acknowledgement | `eu.vaultsync.helper-pairing/v1/pairing-active-ack\0` |
| App pairing abort | `eu.vaultsync.helper-pairing/v1/pairing-abort\0` |
| Helper abort acknowledgement | `eu.vaultsync.helper-pairing/v1/pairing-abort-ack\0` |
| App-key rotation request, signed by old app key | `eu.vaultsync.helper-pairing/v1/app-key-rotation-request\0` |
| App-key rotation proof, signed by new app key | `eu.vaultsync.helper-pairing/v1/app-key-rotation-new-proof\0` |
| App-key rotation acceptance, signed by helper | `eu.vaultsync.helper-pairing/v1/app-key-rotation-accept\0` |
| Helper-key rotation proposal, signed by old helper key | `eu.vaultsync.helper-pairing/v1/helper-key-rotation-propose\0` |
| Helper-key rotation proof, signed by new helper key | `eu.vaultsync.helper-pairing/v1/helper-key-rotation-new-proof\0` |
| Helper-key rotation confirmation, signed by app | `eu.vaultsync.helper-pairing/v1/helper-key-rotation-confirm\0` |
| TLS-pin rotation proposal, signed by current helper | `eu.vaultsync.helper-pairing/v1/tls-pin-rotation-propose\0` |
| TLS-pin rotation confirmation, signed by app | `eu.vaultsync.helper-pairing/v1/tls-pin-rotation-confirm\0` |
| Revocation request, signed by app when available | `eu.vaultsync.helper-pairing/v1/revocation-request\0` |
| Revocation record, signed by helper | `eu.vaultsync.helper-pairing/v1/revocation-record\0` |
| Lifecycle finalize, signed by app | `eu.vaultsync.helper-pairing/v1/lifecycle-finalize\0` |
| Lifecycle active acknowledgement, signed by helper | `eu.vaultsync.helper-pairing/v1/lifecycle-active-ack\0` |
| Lifecycle abort, signed by app | `eu.vaultsync.helper-pairing/v1/lifecycle-abort\0` |
| Lifecycle abort acknowledgement, signed by helper | `eu.vaultsync.helper-pairing/v1/lifecycle-abort-ack\0` |

The bootstrap HMAC is non-circular: it is `HMAC-SHA-256(key=bootstrap_secret, message="eu.vaultsync.helper-pairing/v1/bootstrap-hmac\0" || deterministic_app_request_without_labels_21_and_255)`. The app then inserts the 32-byte HMAC as label `21` and signs the app-request domain plus that deterministic map with only label `255` omitted. A valid signature or HMAC from one domain is invalid in every other domain.

### Byte-exact identifiers and bootstrap transcript

The exact pairing capability string is `eu.vaultsync.diagnostics.helper-pairing/1`; protocol and suite are unsigned integer `1`. The following derivations are fixed:

- `app_key_id` and `helper_key_id` are `SHA-256("eu.vaultsync.key-id/ed25519/v1\0" || raw_public_key)`, where the public key is the exact 32-byte RFC 8032 compressed key.
- `tls_spki_pin` is SHA-256 over the exact DER SubjectPublicKeyInfo of the P-256 TLS key, with no certificate bytes or textual encoding.
- `device_id_digest` is `SHA-256("eu.vaultsync.binding/syncthing-device/v1\0" || raw_device_id)`. `raw_device_id` is the exact 32 bytes obtained only after the standard Syncthing Device ID parser validates the check digits; no display-string form is hashed.
- `folder_id_digest` is `SHA-256("eu.vaultsync.binding/syncthing-folder/v1\0" || uint32_be(length) || folder_id_utf8)`. The UTF-8 bytes must exactly equal the non-empty ID returned by both local Syncthing APIs; there is no trim, case folding, or Unicode normalization. More than 255 bytes is unsupported.
- A signed-message digest is SHA-256 over that message's exact signature domain plus its deterministic map with the signature omitted.

The QR is deterministic CBOR wrapped in unpadded base64url and contains only the capability, protocol/suite, helper-generated invitation nonce, fixed HTTPS host and port, TLS SPKI pin, helper public key and key ID, homeserver/folder bindings, both identifier digests, issue/expiry seconds, and the 32-byte one-time secret. Host is either a lowercase ASCII DNS name or a canonical IP literal; userinfo, path, query, fragment, zone identifier, redirects, and an out-of-range port are forbidden. The app always calls the fixed path `POST /api/v1/diagnostics/pairing`.

The bootstrap field registry is exact; label `255` is always a 64-byte Ed25519 signature when present:

| Label | Field | Type and length |
|---:|---|---|
| `1` | capability | exact ASCII text `eu.vaultsync.diagnostics.helper-pairing/1` |
| `2`, `3` | protocol, suite | uint, exactly `1` |
| `4` | message_type | uint: `0=QR`, `1=app_request`, `2=helper_accept`, `3=finalize`, `4=finalize_ack`, `5=receipt`, `6=ready_ack`, `7=activate`, `8=active_ack`, `9=abort`, `10=abort_ack` |
| `5` | invitation_nonce | bstr, 32 bytes |
| `6` | endpoint_host | restricted ASCII text, 1–253 bytes |
| `7` | endpoint_port | uint, 1–65535 |
| `8` | tls_spki_pin | bstr, 32 bytes |
| `9`, `10` | helper_public_key, helper_key_id | bstr, 32 bytes each |
| `11`, `12` | homeserver_binding, folder_binding | bstr, 32 bytes each |
| `13`, `14` | device_id_digest, folder_id_digest | bstr, 32 bytes each |
| `15`, `16` | issued_at, expires_at | uint Unix seconds |
| `17` | bootstrap_secret | bstr, 32 bytes; QR only |
| `18`, `19` | app_public_key, app_key_id | bstr, 32 bytes each |
| `20` | app_nonce | bstr, 32 bytes |
| `21` | bootstrap_hmac | bstr, 32 bytes |
| `22` | app_request_digest | bstr, 32 bytes |
| `23`, `24` | app_epoch, helper_epoch | uint; initial app epoch is `1` |
| `25` | helper_nonce | bstr, 32 bytes |
| `26` | prior_message_digest | bstr, 32 bytes |
| `255` | signature | bstr, 64 bytes |

The QR contains labels `1`–`17` plus `24` and no signature. The app request contains `1`–`16`, `18`–`21`, `23`, `24`, and `255`; its values through label `16` byte-equal the invitation, while label `17` is forbidden. The pending helper acceptance contains `1`–`5`, `9`–`16`, `18`–`20`, `22`–`25`, and `255`; it uses fresh issue/expiry bounds no later than the invitation expiry. Types `3`–`10` contain `1`–`5`, `9`–`16`, `18`–`20`, `22`–`26`, and `255`. For types `3` through `8`, label `26` is respectively the digest of type `2`, `3`, `4`, `5`, `6`, or `7`; type `9` binds the latest pending/active message and type `10` binds type `9`. Odd types `3`, `5`, `7`, and `9` are app-signed; even types `4`, `6`, `8`, and `10` are helper-signed. Every other field is forbidden for that message type.

For bootstrap and lifecycle messages, `expires_at` is greater than `issued_at` by at most 300 seconds. Each side also enforces a 300-second monotonic deadline and permits at most ±120 seconds of wall-clock skew; wall-clock skew never extends the local deadline.

The app pairing request echoes every non-secret QR field, adds the app public key/key ID and a fresh 32-byte app nonce, and contains the HMAC and app signature constructed above. It never echoes the one-time secret. The pending helper acceptance binds the complete app-request digest, both public keys/key IDs, bindings, protocol/suite, app/helper epochs, invitation/app nonces, issue/expiry bounds, and a fresh 32-byte helper nonce under the helper-accept domain.

The first normative bootstrap-HMAC golden vector uses `bootstrap_secret = 0xa5` repeated 32 times, helper public key `0x09` repeated 32 times, app public key `0x12` repeated 32 times, invitation nonce `0x05` repeated 32 times, `helper.test:443`, issue/expiry `1700000000`/`1700000300`, app/helper epoch `1`, and the other fixed 32-byte fields visible in the canonical body. Labels `21` and `255` are absent:

```text
canonical_body_hex=b501782965752e7661756c7473796e632e646961676e6f73746963732e68656c7065722d70616972696e672f310201030104010558200505050505050505050505050505050505050505050505050505050505050505066b68656c7065722e74657374071901bb085820080808080808080808080808080808080808080808080808080808080808080809582009090909090909090909090909090909090909090909090909090909090909090a5820b8933818a6a0f5d9a030a0b2d8ed226ff197f5f9fd6e5fcf16db6fd39e14c5620b58200b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0c58200c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0d58200d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0e58200e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0f1a6553f100101a6553f22c1258201212121212121212121212121212121212121212121212121212121212121212135820dcc27b99cfce3366f5975bfe29062bc3a0d6b0aa6ed24288273fad64a7fcfa3114582014141414141414141414141414141414141414141414141414141414141414141701181801
expected_hmac_hex=c65ea12ee5902b58a833688aff7db0caa74b782a3fd9a794e99c67e6c08d9497
```

Go and Swift fixtures must reproduce both exact byte strings before any pairing code can pass review.

The transcript fingerprint is the first six bytes of `SHA-256("eu.vaultsync.helper-pairing/v1/transcript-fingerprint\0" || app_request_digest || helper_accept_digest)`, displayed as exactly 12 uppercase hexadecimal characters. It is comparison UI, not a credential, and never substitutes for QR possession, TLS pinning, HMAC, or both signatures.

Every lifecycle transition above is a separate signed deterministic map, not a multi-signature field with ambiguous ordering. The registry is:

| Label | Field | Type and length |
|---:|---|---|
| `1` | capability | exact pairing capability text |
| `2`, `3` | protocol, suite | uint, exactly `1` |
| `4` | message_type | uint: `11`–`13` app-key rotation, `14`–`16` helper-key rotation, `17`–`18` TLS-pin rotation, `19`–`20` revocation, `21`–`24` finalize/ack/abort/abort-ack |
| `5`, `6` | homeserver_binding, folder_binding | bstr, 32 bytes each |
| `7`, `8` | current_app_public_key, current_app_key_id | bstr, 32 bytes each |
| `9`, `10` | proposed_app_public_key, proposed_app_key_id | bstr, 32 bytes each |
| `11`, `12` | current_helper_public_key, current_helper_key_id | bstr, 32 bytes each |
| `13`, `14` | proposed_helper_public_key, proposed_helper_key_id | bstr, 32 bytes each |
| `15`, `16` | current_tls_spki_pin, proposed_tls_spki_pin | bstr, 32 bytes each |
| `17`, `18` | current_app_epoch, proposed_app_epoch | uint |
| `19`, `20` | current_helper_epoch, proposed_helper_epoch | uint |
| `21`, `22` | issued_at, expires_at | uint Unix seconds |
| `23` | message_nonce | bstr, 32 bytes |
| `24` | prior_message_digest | bstr, 32 bytes |
| `25` | revocation_reason | uint: `1=user_request`, `2=lost_app`, `3=folder_removed`, `4=suspected_compromise` |
| `26` | current_credential_state_digest | bstr, 32 bytes |
| `27` | revocation_origin | uint: `1=signed_app`, `2=local_helper_admin` |
| `28` | transition_digest | first request/proposal digest, bstr, 32 bytes |
| `29` | transition_kind | uint: `1=app_key`, `2=helper_key`, `3=tls_pin` |
| `255` | signature | bstr, 64 bytes |

Every lifecycle message contains `1`–`8`, `11`, `12`, `15`, `17`, `19`, `21`–`23`, `26`, and `255`. App-key rotation types `11`–`13` additionally contain `9`, `10`, and `18`; types `12` and `13` also contain `24`. Helper-key rotation types `14`–`16` additionally contain `13`, `14`, and `20`; types `15` and `16` also contain `24`. TLS-pin types `17` and `18` additionally contain `16`, and type `18` contains `24`. Revocation request type `19` additionally contains `18`, `25`, and `27=1`; revocation record type `20` contains the same fields, plus `24` only for origin `1`, while origin `2` has no preceding request.

Type `11` is signed by the current app key, type `12` by the proposed app key, and type `13` by the current helper key. Type `14` is signed by the current helper key, type `15` by the proposed helper key, and type `16` by the current app key. Type `17` is signed by the current helper key and type `18` by the current app key. Type `19` is signed by the current app key; type `20` is always signed by the current helper key, including after a local-admin action. Each `prior_message_digest` is the signed-message digest of the immediately preceding type.

Types `21`–`24` contain the common lifecycle fields, `24`, `28`, `29`, and the proposed fields required by their transition kind: `9`, `10`, `18` for app-key; `13`, `14`, `20` for helper-key; or `16` for TLS-pin. Type `21` finalizes the pending type `13`, `16`, or `18`; type `22` binds type `21`; type `23` aborts the latest pending message; and type `24` binds type `23`. Type `21` is signed by the proposed app key for app-key rotation and the current app key otherwise. Type `22` is signed by the proposed helper key for helper-key rotation and the current helper key otherwise. Types `23` and `24` are signed by the still-current app and helper keys. Every proposed epoch is exactly current epoch plus one. All other fields are forbidden.

Types `13`, `16`, and `18` create only expiring pending transitions; they never switch an active key, epoch, or pin. After durably storing that pending message, the app sends type `21`. The helper commits locally, retains the old credential solely for bounded reconciliation/rollback, and returns deterministic type `22`; the app switches only after validating and durably storing type `22`. A lost acknowledgement is recovered by replaying the identical type `21`. Until a capability query signed/transported under the proposed state succeeds, the helper permits only finalize/abort/reconciliation and no operation or artifact; TLS rotation serves the old and proposed pins only inside this bounded state. The successful query makes the transition terminal and retires the old credential according to the explicit confirmation/rollback policy.

Before helper commit, type `23` followed by type `24` deletes only pending transition state. Expiry, cancellation, crash, or network loss has the same effect after bounded replay. An abort after helper commit is not a silent rollback: it requires a new reverse rotation or revocation. Revocation type `20` is immediately fail-closed and may safely be one-sided because it can only remove authority. No flow claims cross-device atomicity.

`current_credential_state_digest` is the signed-message digest of type `8` for a fresh pairing, type `22` after its proposed-state capability confirmation succeeds, or type `20` after revocation. Missing steps, a signer in the wrong role, reused nonces, non-increasing epochs, mismatched state/transition digests, and unknown fields fail closed. Lifecycle applies to one exact app/helper/homeserver/folder authorization; a key spanning several folder authorizations is staged separately and remains capability-unavailable wherever confirmation is incomplete. Cross-language golden bytes for every lifecycle and pending/finalize/abort type are required before runtime implementation.

## Credential storage

### iOS app

- Store the Ed25519 seed and pairing records under a diagnostics-specific Keychain service, not the existing APNs helper API.
- Use [`kSecAttrAccessibleWhenUnlockedThisDeviceOnly`](https://developer.apple.com/documentation/security/ksecattraccessiblewhenunlockedthisdeviceonly), `kSecAttrSynchronizable = false`, and no shared access group. A protected-data lock makes diagnostics unavailable/interrupted rather than weakening accessibility.
- Store only key material, public-key IDs, opaque homeserver/folder bindings, pairing and rotation epochs, the locally selected Device/folder mapping, endpoint and TLS pin, and revocation state. Store no folder/vault names, paths, operation values, or proof history.
- Keep a random installation marker in app-owned protected storage. If the app container is lost but a Keychain item survives reinstall, do not silently reuse it; show an explicit recover/revoke/re-pair choice.
- `ThisDeviceOnly` means device replacement and restore to another device require a new pairing. There is no iCloud Keychain sync or Cloud Relay escrow.

### Helper

- Introduce a dedicated diagnostics state directory or Docker volume, separate from `config.xml`, the synchronized folder, and logs. Directory mode is `0700`; files are `0600`; writes are create-new or atomic temp-file/fsync/rename operations.
- Store separate helper-signing and TLS private keys, opaque binding records, authorized app public keys and scopes, monotonically increasing epochs, revocation tombstones, and locally trusted folder-ID-to-mount aliases. Never store an app private key or pairing secret after bootstrap.
- Docker uses a dedicated state volume and a read-only container root. Host/NAS/macOS/Windows packages use an OS account and ACL that deny other non-administrator users. A platform that cannot provide this isolation is unsupported for the capability.
- An operator may explicitly back up the complete helper credential state in an encrypted, access-controlled backup. There is no automatic backup, cloud escrow, or partial key export. Restoring that state is recovery of the same helper identity, not a new pairing.
- Old helpers ignore the new state. Upgrade, downgrade, or app rollback never auto-deletes credentials or the future namespace root.

## Homeserver and folder bindings

At helper-identity initialization, the helper creates one random 32-byte homeserver binding and pins it locally to the full Device ID read from its trusted Syncthing API. It remains stable for that helper identity across app installations, key/TLS rotation, restarts, upgrades, downgrades, and folder authorization changes. A folder receives one independent random 32-byte binding the first time the operator selects it; that binding is durably reserved to the local Syncthing folder ID and approved mount alias before an invitation is shown and is reused by every later installation. Timeout, abort, revocation, or removal never regenerates or reassigns an already-reserved binding. Only an explicit new helper identity may create a new homeserver/folder binding set; old values remain rejected tombstones and are never reused.

The pairing QR carries the byte-exact domain-separated Device and folder digests above, not their raw values. The app computes the same digests from its already-local mapping and refuses a mismatch. After pairing, both sides store the full local mapping privately but protocol artifacts carry only the random bindings and key IDs.

Before capability acceptance and before every evidence transition:

1. the helper's current Device ID must equal its pinned local value;
2. the app's selected homeserver/folder mapping must equal its paired record;
3. the folder binding must remain authorized to that app key and helper key epoch; and
4. the locally configured mount must still resolve to the operator-approved binding.

A path, folder ID, Device ID, or binding supplied by a network request never selects a filesystem target. A binding is only a lookup key into local trusted state.

## Explicit trust bootstrap

Pairing is disabled by default. The helper exposes no diagnostics listener until the operator configures an explicit local/LAN or VPN endpoint and starts a local privileged pairing command for one existing Syncthing folder ID.

1. The local command verifies the helper state, current Device ID, selected folder ID, and future access preconditions without creating a namespace. It loads the stable homeserver/folder bindings above, durably reserving the folder binding only if this is its first invitation, then creates one in-memory pending record, a 32-byte one-time secret, a helper-generated invitation nonce, and a five-minute monotonic deadline.
2. It displays the exact deterministic QR envelope defined above. The QR is sensitive bootstrap material; it is never logged, persisted, copied to Cloud Relay, or placed in the synchronized folder.
3. The app user selects the already-known homeserver/folder, scans the QR, verifies both identifier digests, creates its per-installation Ed25519 key, and connects with TLS 1.3 while pinning the exact SPKI. DNS or public-CA trust alone is insufficient.
4. The app sends the exact deterministic CBOR request above to the fixed pairing endpoint. No identifier appears in its path, query, headers, certificate, or access log.
5. The helper verifies the TLS session, HMAC, signature, exact pending record, scope, nonce, expiry, and unused secret; consumes the secret; persists an expiring **pending** authorization; and returns the deterministic type-`2` signed acceptance. It does not activate the app. Byte-identical type-`1` retries return the same acceptance; a different request fails closed.
6. The app verifies and durably stores the pending acceptance. Both sides display the exact 12-character transcript fingerprint. A mismatch or cancellation sends type `9`; the helper removes only pending state and returns type `10`. If abort is lost, expiry has the same no-authorization result.
7. After explicit fingerprint confirmation, the app durably records its intent and sends type `3`. The helper persists that digest as `finalize pending` and returns type `4`; neither side exposes capability discovery yet. Exact retries at either step return the same signed bytes.
8. After durably storing type `4`, the app sends type `5`. The helper records `awaiting activation` and returns type `6`; the app stores type `6` as `ready to activate`, not as proof or permission to create a namespace/artifact.
9. The app sends type `7`. The helper atomically promotes that exact scoped record to active and returns type `8`; only after validating and durably storing type `8` does the app mark pairing active or issue a capability query. A lost type `8` is recovered by replaying identical type `7`; the helper retains the deterministic terminal reply for 24 hours without accepting altered/expired authority.
10. A crash or network loss resumes only from the exact durably stored pending message and idempotently replays the next step. Pre-activation states expire after the same five-minute monotonic deadline and become inactive tombstones. Type `9` before promotion removes pending state; if it races after promotion, the helper records an immediate revocation and returns type `10` rather than deleting history. There is no claim of cross-device atomic commit: the helper is already active before type `8` can activate the app, and a helper-only record cannot create evidence because every operation is app-initiated and signed.

At most one pending pairing per folder and four per helper may exist. The endpoint accepts bodies only up to 16 KiB, at most ten requests per invitation, and at most 30 requests per minute helper-wide; it returns fixed error categories and never trusts a source IP as identity. Exceeding an invitation limit invalidates only that pending secret. Restart before durable pending state, timeout, cancellation, a second secret use, or validation failure destroys only the pending secret/state and produces no active pairing.

No mDNS, UPnP, Cloud Relay tunnel, unauthenticated synchronized file, StoreKit transaction, or Syncthing TLS key participates in bootstrap. Remote access requires an operator-controlled VPN or separately reviewed reverse-proxy configuration; the standard installer opens no public port.

## Mutual authentication after pairing

- Every app request carries the app key ID, helper key ID, homeserver/folder bindings, pairing epoch, protocol version, fresh request nonce, issue/expiry bounds, and an app signature.
- Every helper response covers the complete request digest plus the same bindings/epochs and has its own nonce and helper signature.
- TLS protects transport and the pinned SPKI prevents endpoint substitution; application signatures remain authoritative and survive proxy/library behavior. HTTP status is transport diagnostics only.
- Authenticated capability discovery is additive. A pre-pairing version string can say only that pairing might be supported; it cannot authorize an operation.
- Fixed endpoint paths and disabled body/access logging prevent operation or binding values from entering server or reverse-proxy logs.

## Multiple app installations

Every iPhone/iPad installation has a distinct app key, pairing epoch, folder scopes, quotas, and revocation state. The helper never copies authorization from one installation to another. Adding another installation requires a new one-time pairing and creates a separate immutable authorization record under the same authenticated helper/folder ownership. Evidence and cleanup for one app key never upgrade or delete another app's operation.

The helper UI/CLI identifies installations only by a user-confirmed local label stored outside protocol artifacts plus a short public-key fingerprint. Labels never enter signed artifacts, logs, Cloud Relay, or the synchronized namespace.

## Rotation, revocation, recovery, and loss

| Event | Required behavior |
|---|---|
| App key rotation with old key available | App creates a new Ed25519 key; old- and new-key messages bind the incremented epoch; helper acceptance remains pending. Use types `21`/`22` and the proposed-key capability confirmation before either side enables operations or retires the old key. |
| App key lost or app moved to another device | No automatic recovery. Pair again as a new installation, then explicitly revoke the lost key from the helper. |
| Helper signing key rotation while old key is trusted | Helper cross-signs the new key and incremented epoch with both old and new keys; every app stages, finalizes, and confirms it independently through types `21`/`22` and a proposed-key capability query. Until terminal for one app/folder authorization, capability is unavailable there and the old key remains only for reconciliation/rollback. |
| Suspected helper-key compromise or helper state loss | Cross-signing is insufficient. Generate a new helper identity and explicitly re-pair every installation/folder. Old credentials and namespace content are never adopted. |
| TLS certificate renewal | Allowed with the same pinned P-256 key. A new TLS key requires a helper-key-signed pending pin rotation, explicit types `21`/`22`, and a successful query over the proposed pin before the old pin retires; suspected compromise requires new pairing. |
| App revocation | A signed app request or local helper-admin command marks the app key revoked and increments the authorization epoch. No new operations/evidence are accepted. |
| Folder unauthorization | Remove the binding from the app scope after bounded authenticated cleanup. Do not remove the Syncthing share, folder mapping, credentials for other folders, or namespace root. |
| App/helper downgrade | Preserve credentials. The capability becomes unavailable/dormant; no automatic re-pair, deletion, or trust conversion occurs. |

Revoked public keys and epochs remain as minimal tombstones for at least the maximum artifact TTL plus 24 hours so stale files can be rejected and authenticated cleanup can finish. Private-key retirement or credential deletion is always an explicit security action, never an upgrade/rollback side effect.

## Replay and downgrade defense

- Pairing secrets and nonces are single use; all messages bind protocol/suite, message domain, both public-key IDs, both opaque bindings, both epochs, issue/expiry bounds, and the prior transcript/request digest.
- The app and helper accept only the current locally stored epoch and exact active request. Old, copied, cross-folder, cross-homeserver, cross-installation, and out-of-order messages fail closed.
- Maximum clock skew is defined by the canonical contract; a local monotonic deadline is always at least as strict as wall-clock expiry.
- Unknown algorithms, versions, mandatory fields, or a lower advertised capability than the pinned minimum produce `capability unavailable`/`unsupported`, never fallback pairing.
- The app never treats Syncthing peer authentication as a helper signature, and the helper never exports or reuses Syncthing's TLS private key.

## Compatibility and rollback matrix

| App | Helper | Credential behavior |
|---|---|---|
| Existing app | Existing helper | No diagnostics credential, endpoint, pairing, or namespace. Trigger v1 unchanged. |
| Existing app | New dormant helper | Helper keys may exist only after explicit operator setup; no app authorization or namespace is created automatically. Existing behavior is unchanged. |
| New app | Existing helper | `capability unavailable`; passive local-progress evidence remains available. No pairing attempt mutates existing setup. |
| New app | New unpaired helper | Pairing is offered only after explicit local bootstrap. Until completed, no probe or general error. |
| New paired app | New paired helper | Only exact authorized folder bindings may negotiate the later contract. Pairing itself proves no transfer. |
| App downgrade | New helper | Helper stays dormant for that installation; credentials and namespace root remain; bounded expiry cleanup only. |
| New app | Helper downgrade | Capability becomes unavailable; app starts no operations and never converts old artifacts/timestamps into proof. |

## Privacy and operational logging

Logs, telemetry, crash reports, support bundles, and Cloud Relay must not contain private/public key bytes, pairing secrets, QR payloads, TLS pins, Device/folder identifiers or digests, opaque bindings, nonces, transcript fingerprints, signed bodies, paths, or credential records. Allowed logs are fixed pairing state categories, protocol major version, bounded counts, coarse durations, and fixed remediation actions.

Pairing data travels only over the pinned local HTTPS channel and local visual bootstrap. Cloud Relay receives no capability request, credential, correlation, hash, result, or cleanup state. `PRIVACY.md` must be updated before any runtime credential or pairing transport exists.

## Required tests before implementation approval

- RFC 8032 vectors and cross-language Go/CryptoKit sign/verify fixtures for every signature domain, key ID, SPKI pin, binding digest, request/acceptance digest, the normative bootstrap HMAC body/key/result above, and transcript fingerprint.
- Deterministic-CBOR golden vectors plus rejection of duplicates, non-shortest forms, reordered maps, unknown fields, malformed lengths, truncation, and arbitrary bytes.
- Wrong/rotated/revoked keys, wrong helper/app/binding/epoch, replay, duplicate, expiry/skew, out-of-order, QR reuse, and first-request races.
- TLS pin mismatch, certificate renewal, TLS-key rotation, endpoint substitution, proxy/body logging, unavailable endpoint, and LAN/VPN loss.
- Two and eight app installations paired independently to the same folder must receive the same stable homeserver/folder bindings but separate keys/epochs/authorizations/quotas; also test multiple folders/homeservers, independent revocation/rotation, app reinstall with orphaned Keychain state, helper restore, helper loss, and both downgrades.
- Crash/network loss before and after every pairing/rotation message, exact replay, pending expiry, abort/commit races, lost active acknowledgements, one-sided stored state, and proof that no capability operation is accepted until terminal confirmation.
- Keychain accessibility/backup migration tests and helper state permission/atomicity/crash tests on Docker, Linux host/NAS, macOS, and Windows.
- Privacy snapshots proving every forbidden value is absent from logs, persistence outside approved credential stores, Cloud Relay, Trigger v1, and crash annotations.
- Property/fuzz tests for transcripts, epoch transitions, authorization lookup, and arbitrary message ordering.

## Human approval required

Review must explicitly approve or reject each of these choices before implementation:

1. Ed25519 application signatures, separate P-256 TLS identity, HMAC-SHA-256 bootstrap, the byte-exact identifier/transcript derivations, and deterministic CBOR domains.
2. `WhenUnlockedThisDeviceOnly` app storage and a non-synchronizing, non-migrating per-installation key.
3. A dedicated helper state store with optional operator-controlled encrypted whole-state backup and no escrow.
4. Explicit QR + pinned-TLS LAN/VPN bootstrap, with no automatic discovery, public listener, Relay tunnel, or TOFU.
5. Per-installation/per-folder authorization, explicit rotation/revocation, and re-pair-only recovery after key loss or suspected compromise.
6. The downgrade rule that preserves credentials and only makes the capability unavailable/dormant.
7. The exact lifecycle field registry, signer roles, digest chain, per-folder staging, and cross-language golden-byte requirement.
8. Stable helper/folder binding reuse across installations and the explicit pending/finalize/abort/replay protocol instead of any cross-device atomicity claim.

Approval of this document still does not approve a namespace, access widening, probe, canonical operation contract, helper rollout, or app implementation.

## Result

The proposed design can attribute future messages to one explicitly paired app installation and helper while binding them to the locally known homeserver and folder. It intentionally chooses fail-closed re-pairing over silent recovery and keeps Cloud Relay and Trigger v1 outside the trust path. Until human review accepts it and the separate namespace/access and canonical-contract gates are also accepted, Decision 021 remains blocked.

## Links

- [Decision 019 — Relay evidence stays layered](019-relay-proof-hierarchy.md)
- [Decision 020 — Sync-path proof requires correlated evidence](020-sync-path-proof-requires-correlated-evidence.md)
- [Decision 021 — Capability-negotiated helper contract](021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md)

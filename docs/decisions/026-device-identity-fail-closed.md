# 026 — Device identity loads fail closed, never regenerate

- Context: The bridge used upstream `LoadOrGenerateCertificate`, which generates a fresh key pair whenever loading `cert.pem`/`key.pem` fails. A field report (#135) showed a crash-looping device burning five identities in one day — each new device ID invalidates every peer pairing and appears server-side as a new pending device.
- Decision: The bridge generates an identity only when both files are verifiably absent (first launch). Corrupt, partial, or unreadable identity state fails the engine start with an error and never rewrites the files (`go/bridge/identity.go`).
- Why: The device ID is the user's pairing trust anchor. A visible failed start is recoverable; a silently replaced identity is not — it orphans server entries, resets sync tracking, and can strand share state on every peer.
- Rejected alternative: Keeping upstream's regenerate-on-error fallback ("the app always gets a working engine"), because availability of the engine is worth less than continuity of the identity; also rejected treating a stat error as "file absent", since an iOS file-protection outage would then still mint a new identity.
- Links: issue #135, `go/bridge/identity_test.go`.

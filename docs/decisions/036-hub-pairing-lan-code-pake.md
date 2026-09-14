# 036 — Hub pairing: LAN discovery, short code, SPAKE2

**Context.** The Hub must let a device join without anyone reading a 56-character device ID or opening a Web UI. The only thing a human should carry between devices is a short code.

**Decision.** Pairing protocol v1 (`hub/pairing.go`): the device finds Hubs by UDP broadcast on the pairing port, then runs SPAKE2 (RFC 9382, edwards25519, `hub/pake`) with the code as password. Both confirmation MACs are exchanged before any key is used; all later messages are AES-256-GCM boxes bound to the session and direction. Codes are `WORD-WORD-NN` (256-word list, ~22.6 bits), valid 24 h, locked after 5 wrong attempts; starts are rate-limited per address. Transport is plain HTTP on the LAN — no TLS, no PKI, no TOFU. The pairing port must never be forwarded. The Hub stores only the password scalar, never the code.

**Why.** A PAKE turns a low-entropy code into mutual authentication: a passive observer cannot search the code offline, an active party without the code fails key confirmation, and the Hub counts every failed online guess. LAN-only pairing (broadcast does not route) plus the lockout makes 22 bits sufficient. Syncthing's own TLS with self-certifying device IDs takes over after the ~100 bytes of IDs are exchanged, so nothing else needs protecting.

**Threat model.** Passive sniffing → PAKE. Active MITM / fake Hub → key confirmation fails. Online guessing → 5 attempts then lockout, per-IP throttle. Shoulder-surfed code within 24 h → accepted residual risk on a home LAN; every paired device is visible in `status`. Compromised Hub → out of scope (trust anchor). WAN exposure → not forwarded, no unauthenticated fallback.

**Rejected alternatives.** mDNS (Docker bridge networks and NAS images make it unreliable; adds a dependency). QR with pinned TLS as in decision 022 (works for phones, not for a code typed on a desktop; 022 explicitly scopes itself to diagnostics). Long high-entropy codes without PAKE (not typable). SPAKE2+ (verifier storage buys little for a 24-hour secret; plain SPAKE2 is simpler to audit against the RFC).

**Links.** `hub/pake`, `hub/pairing.go`, `hub/discovery.go`; decision 022 (scoped to diagnostics, explicitly not precedent).

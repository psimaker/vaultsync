# 019 — Relay evidence stays layered

- Context: Relay reachability, accepted v1 signals, local silent-push receipt, and sync progress answer different questions; collapsing them made setup status overclaim success (#91).
- Decision: Model StoreKit verification, verified provisioning, backend reachability, per-homeserver v1 observation, local wake-up receipt, background-sync start, and observed sync progress as independent evidence.
- Decision: A valid debounced v1 signal may update Relay observation, but cannot set APNs delivery or sync success.
- Decision: Normal UI uses plain-language waiting states; Diagnostics names the technical boundary and keeps multi-homeserver results separate.
- Why: v1 sender identity is not cryptographically authenticated, APNs can defer/drop silent pushes, and a wake-up can start without completing data exchange.
- Rejected alternative: Treat `/health`, provisioning, or Relay observation as “active” delivery, because each skips a stronger downstream leg.
- Rejected alternative: Treat silent-push receipt as completed sync, because the later sync roundtrip remains unproven.
- Links: #91.

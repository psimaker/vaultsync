# 016 — A sync success needs peer-exchange evidence

- Context: Both last-sync detectors accepted `state == idle && needFiles == 0`. A freshly accepted, still-empty folder satisfies that after its first local scan with the peer offline — the app claimed "first sync done" (header, stale warning, vault row, Cloud Relay upsell) during a stall (#94).
- Decision: A folder's idle state records a sync only while a connected remote peer shares the folder; the relay upsell additionally requires a non-empty global index (floor for pre-#94 persisted last-sync dates, which are never retracted).
- Why: The "first sync done" moment gates the paid-product pitch and suppresses the stale-sync warning — claiming it without any peer exchange masks the stall exactly when the user needs to see it.
- Rejected alternative: `globalFiles > 0` as the detector — global counts include local files, so a locally populated vault that never reached any peer would still fake a sync, and a genuinely empty vault syncing with an online peer would never register.
- Links: #94.

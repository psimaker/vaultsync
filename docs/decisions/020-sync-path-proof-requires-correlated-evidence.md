# 020 — Sync-path proof requires correlated evidence

- Context: Engine reachability, scans, index updates, `idle`, and completion can occur without transfer and cannot prove a controlled roundtrip.
- Decision: Keep background start, local data progress, upload, download, and full-roundtrip proof as independent fields; never derive a global success flag.
- Decision: This milestone may set only fresh local data progress after a successful file `ItemFinished` newer than both the check cursor and nanosecond start time within one stable engine generation.
- Decision: Manual results are in-memory and isolated per folder and its sole connected peer; partial, unsupported, offline, stale, cancelled, and interrupted states remain explicit.
- Decision: The check is finite, passive, user-started from Relay Diagnostics, lifecycle-bound, and creates no probe, rescan, file/configuration write, or path mutation.
- Decision: Upload and controlled download remain unset; a roundtrip requires upload followed by download with one artifact correlation that the current helper does not provide.
- Why: Temporal proximity can scope an observation to a check window, but cannot prove that the check caused it, that network bytes moved, or that one peer supplied every block.
- Rejected: Treating local/remote index activity, `idle`, 100% completion, Relay HTTP 200, or v1 trigger observation as transfer proof.
- Follow-up: Design a helper-first, capability-negotiated diagnostics namespace and additive request/response contract before creating any probe.
- Compatibility: Relay/Notify v1, provisioning, APNs, StoreKit, folder mappings, subscriptions, and existing wake-up history remain unchanged.

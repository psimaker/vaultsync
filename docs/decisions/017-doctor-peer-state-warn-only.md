# 017 — Doctor peer-state checks are WARN-only and doctor-only

- **Context**: #88 point 3 — a connectivity-green `--doctor` can still mean "nothing will ever sync": no remote device connected, or devices connected but no folder shared with them. The "subscribed but nothing arrives" case needed this visible.
- **Decision**: `--doctor` gains two peer-state checks (`/rest/system/connections`, `/rest/config/folders`). They print `WARN`, never `FAIL` — the exit code stays 0 — and any error inside them (an old Syncthing without the endpoint, a mid-run API hiccup) also downgrades to `WARN`. `--healthcheck` excludes them entirely (`preflightMode.IncludePeerState`).
- **Why**: a peer that is off or away is everyday state, not a setup failure; the healthcheck feeds Docker's `HEALTHCHECK`, so peer state there would flap container health (and anything monitoring it) on legitimately offline peers.
- **Rejected**: FAIL semantics — false alarms on normal offline peers would train users to ignore the doctor. Peer state in `--healthcheck` — flappy container health. A new dedicated flag — undiscoverable; `--doctor` is the documented entry point everywhere.
- **Links**: #88, `notify/doctor.go`, `docs/troubleshooting.md` ("How to read `--doctor`").

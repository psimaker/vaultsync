# 032 — Receive-side sync is read-only in 2.0.2

- Context: Receive-side automation can otherwise delete or replace a unique local version before conflict recovery is safe (#150/#167).
- Decision: In 2.0.2, existing send-receive, receive-only, and receive-encrypted folders are read-only from database open through runtime; Send Only retains its existing behavior.
- Runtime boundary: Protected folders do not scan, watch, pull remote data, recheck received paths, clean versions, mutate vault metadata or bytes, or mutate their local file index; authenticated peer reads remain available.
- Protocol boundary: Authenticated remote indexes may persist Need and change only its derived local global/needed flags and resulting count buckets; required structural protocol metadata remains allowed.
- Startup boundary: A pending protected-folder database migration stops before mutation, unclassified orphan databases remain untouched, and each existing folder type is immutable for the process lifetime.
- Signal requirement: Rejected operations expose one stable path-free safety code, omit folder, path, vault, device, sentinel, and error details, and never take a success shape.
- Restart requirement: The same home preserves identity, configuration, remote Need, protected vault bytes, and the safety stop without repair, migration, reacceptance, or automatic pause.
- Why: The boundary stops VaultSync- and Syncthing-owned receive mutations before they can race external vault editors or atomic saves; the separate app-private database assumption is recorded in decision 034.
- Rejected alternative: Conflict-shaped path rechecks, rename-first retention, or retain-until-limit, because none establishes a mutation barrier before user choice.
- Rejected alternative: Automatic quarantine, snapshot, repair, migration, or pause, because their safety and override semantics are not proven.
- Recovery boundary: Decision 033 keeps 2.0.2 inspection-only; mutating recovery requires a separately approved and proven doctrine.
- Links: issues [#150](https://github.com/psimaker/vaultsync/issues/150) and [#167](https://github.com/psimaker/vaultsync/issues/167); decisions 002, 004, 027, 028, 033, and 034.

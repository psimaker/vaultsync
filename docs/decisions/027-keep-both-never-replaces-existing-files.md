# 027 — Keep Both never replaces an existing file

- Context: `KeepBothConflict` derived one destination name and could replace an existing regular file at that destination (#144), losing a previously preserved conflict copy.
- Decision: Keep Both uses atomic no-replace semantics. Existing destination bytes are never replaced; a collision either produces an actually unique destination or returns an error, and success means every involved content remains present.
- Why: “Keep Both” is a preservation promise. A crash or I/O failure between steps must prefer an extra copy over lost bytes, and cleanup never deletes user files automatically.
- Rejected alternative: Checking with `Stat`/`fileExists` before a normal rename, because another operation can occupy the destination between check and rename; also rejected overwrite-then-repair, because overwritten bytes cannot be reconstructed safely.
- Links: issue #144; `go/bridge/conflicts.go`, `go/bridge/conflicts_test.go`.

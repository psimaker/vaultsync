# 041 — Conflict-file reads are bounded at 1 MiB in both layers; larger notes are compared in Obsidian

**Context.** `ReadFileContent` read a conflict note of any size into memory and handed it across the bridge; the app rendered it as one `Text` and line-diffed it with `difference(from:)`. A large note froze the UI or got the app killed by the watchdog (#184).

**Decision.** The Go bridge refuses a file above `maxReadFileBytes` (1 MiB) before reading it (`error:too large:<bytes>`) and never reads past the bound. The app reads the bound from the bridge (`MaxReadFileBytes`), re-checks the bytes it received, and shows a "Too Large to Compare" state that names both sizes; the resolution actions stay available, so the choice remains manual (decision 028) and only the comparison moves to Obsidian.

**Why.** A bound enforced in one layer only is a bound a stale binary can bypass; a diff over a multi-megabyte note has no value on a phone screen and a real cost in memory and main-thread time.

**Rejected alternative.** Streaming or truncating the note and diffing a prefix. A truncated comparison shows a difference that may not be the one that matters and invites a wrong resolution.

**Links.** #184, #151, decision 028.

# 004 — Fork changes live as regenerated patches, not a fork repository

**Context:** The embedded Syncthing (and go-stun) need iOS-specific modifications that must survive upstream version bumps for years, maintained by one person.

**Decision:** All fork changes live in `go/patches/*.patch`; `make patch` (via `go/vendor-patch.sh`) deletes and regenerates `go/_syncthing_patched/` and `go/_go-stun_patched/` from the module cache plus these patches. The generated trees are gitignored and never edited directly.

**Why:** A patch set keeps the delta to upstream explicit, reviewable, and minimal; full regeneration makes silent divergence between the patches and the built tree impossible; an upstream bump is a re-apply with visible conflicts, not an open-ended merge.

**Rejected alternative:** Maintaining full fork repositories — a fork hides the actual delta, drifts silently, and turns every upstream security update into a manual merge chore.

**Links:** `go/patches/`, `go/vendor-patch.sh`, `go/Makefile`.

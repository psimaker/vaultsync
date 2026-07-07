# 007 — A share never merges into a non-empty directory without explicit consent

**Context:** The accept path guarded only against overlap with *configured* folders (#45). An unconfigured directory that already held content — a label-named folder under the root, the vault-as-root case, or a suffix candidate — was synced into silently: two content sets merged and the mix pushed to every peer (#54). The manual picker (#52) already refused non-empty targets.

**Decision:** No accept ever syncs into an existing target holding anything beyond `.obsidian` unless the user explicitly confirmed the merge in a dialog that names the consequence (contents combined and synced to the other devices). The automatic pass never merges — it parks the share as "needs attention". A recorded manual target (#52) stays exempt: it is recorded consent (006). Enforced in two layers that must decide emptiness identically (mirror tests): the Swift decision core (`resolveAcceptPath`) and the Go hard floor (`AcceptPendingFolder` refuses non-empty targets unless the confirmation travels through `allowNonEmpty`).

**Why:** 002 — an automatic actor may not make a data-mixing decision; only the user knows whether existing content is the share's own earlier data (legitimate resume after remove + re-accept) or a different vault. Hard refusal instead of confirmation would leave that legitimate re-accept no path at all.

**Rejected alternative:** Silently diverting to a numeric-suffix folder ("<name> (2)") — splits a legitimate re-link (the local copy never reconnects) and hides a data-placement decision from the user, the same reason 006 rejected silent fallback.

**Links:** issue #54, issues #45/#52, decisions 002/006.

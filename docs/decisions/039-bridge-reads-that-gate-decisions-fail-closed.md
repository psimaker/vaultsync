# 039 — Bridge reads that gate a decision fail closed

**Context.** Two bridge reads answered a failure with a default. `AcceptPendingFolder` treated a failed read of the pending offers like "no offer" and created a local-only folder that no peer shares; `GetFolderIgnores` returned an empty list for an unreadable `.stignore`, so the Sync Filters screen showed "no filters" while filters existed (#182).

**Decision.** A read whose result gates a user-visible decision or a write never substitutes a default on failure. The error reaches the caller: the accept is refused with the read error; the ignores read returns `{"error": …}` (OS error only, no path), which the app renders as "unavailable" and which every read-modify-write flow treats as "do not write". A successful read that finds nothing — no offer, no `.stignore` — keeps its existing meaning.

**Why.** A default that looks like a legitimate answer hides the failure exactly when the user decides or the app writes, and the wrong write then propagates: an unshared vault that looks accepted, an emptied `.stignore`.

**Rejected alternative.** Keeping the defaults and logging the error. Logs never reach the decision; the UI still claims a state that is not true.

**Links.** #182, #151, decisions 026, 028.

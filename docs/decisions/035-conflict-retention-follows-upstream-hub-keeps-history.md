# 035 — Conflict retention follows upstream; the Hub keeps history

**Context.** Issues #150/#167 proved that Syncthing's `maxConflicts: 10` can discard the oldest conflict copies of one file. The 2.0.2 candidate on PR #168 answered by freezing every receive-capable vault, disabling vault creation and share acceptance, and turning every recovery action into inspection-only (#169 asked the owner to decide the customer impact).

**Decision.** PR #168 is withdrawn and not merged. The shipped app keeps upstream Syncthing's retention behaviour on devices (`maxConflicts: 10`, as every Syncthing user has). Durable history lives on the VaultSync Hub: Hub folders use `maxConflicts: -1` (no conflict copy is ever pruned) and staggered versioning (overwritten and deleted versions are kept for 30 days, then cleaned by Syncthing). `main` stays the 2.0.2 bugfix line (build 38) without the containment changes.

**Why.** A sync app that can neither receive nor create vaults protects data by making it unreachable — that is not the product. The loss window (>10 conflict copies of the same file, oldest pruned) is narrow and well understood; the right place to close it is the always-on server with cheap disk, not the phone. This also matches the 3.0 direction: the Hub is the trust anchor and the archive.

**Rejected alternative.** Patching `folder_sendrecv.go` to never prune plus fail-closed UI (the #167 plan): months of work, a permanently divergent fork of the retention path, and still no versioning on devices. Setting `maxConflicts: -1` on iOS: unbounded growth on the device with the least storage.

**Links.** #150, #167, #168, #169; decisions 028, 032, 033 remain as recorded history of the withdrawn line and do not bind `main`.

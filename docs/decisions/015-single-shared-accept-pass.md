# 015 — One shared accept pass for every mount point

- Context: The automatic pending-share accept pass lived in ContentView; while onboarding was mounted no accept trigger existed, so setup step 3's "accepted automatically" promise was false (#92).
- Decision: Every share-accept entry point (automatic pass, manual accept/retry, merge confirmation, manual-target accept) runs through the single `ShareAcceptCoordinator`; host views only wire triggers and present its one-shot messages. The gates (settled paths — 008, Obsidian access, auto-accept eligibility — 002/#52, merge consent — 007) live inside the coordinator, never in a host view.
- Why: A per-view copy of the pass is a per-view chance to drop a gate; #92 showed the mount point itself had silently become a gate nobody chose.
- Rejected alternative: the honest-copy quick fix (tell the user to leave onboarding first) — it keeps the first-run flow hostage to the view hierarchy and leaves adding a second, subtly different accept path tempting for the next session.
- Links: #92, #56/decision 008, #54/decision 007, #52.

# 025 — Owner approval of diagnostics design gates

**Status:** Owner-approved on 2026-07-12; no independent human review is claimed.

- Context: Decisions 022–024 remain formally proposed designs and their text and status are unchanged by this record.
- Approval: The project owner explicitly approved every enumerated choice D022.1–D022.8, D023.1–D023.7, and D024.1–D024.7.
- Scope: Decision 022's bootstrap and recovery rules apply only to the diagnostics capability and set no precedent for another capability; device replacement or restore requires diagnostics re-pairing.
- Product: `VaultSync Diagnostics` remains a non-localized constant; before enablement, consent in en/de/es/zh-Hans must explain visibility in Obsidian, Files, peers, backups, versions, and tombstones unambiguously.
- Deployment: Docker with an explicit host bind mount is the first model to prove; named-volume Docker, NAS, macOS, Windows, and other deployments remain unsupported until their isolation and rollback are demonstrated.
- Retention: Backup, `.stversions`, remote-history, and tombstone retention beyond live TTL is accepted and must be disclosed before enablement and in `PRIVACY.md`.
- Gates: Decision 024's compatibility, failure, privacy, property/fuzz/model, local-E2E, rollout, and downgrade matrices are binding release gates; skipping one requires a new explicit owner decision.
- Authorization: This approval authorizes no runtime implementation, probe, namespace creation, credential, helper release or rollout, production deployment, Phase B, or app release.
- Sequence: Separately reviewed repository milestones and helper-first implementation, packaging, rollout, and rollback remain binding before app runtime work.
- Consistency: Decision 024 is unchanged; an implementation that cannot satisfy it must stop and report the contradiction rather than alter or bypass it.
- Links: [Decision 022](022-diagnostics-helper-credentials-and-mutual-pairing.md) · [Decision 023](023-diagnostics-namespace-and-least-privilege-access.md) · [Decision 024](024-canonical-correlated-roundtrip-contract-and-threat-model.md)

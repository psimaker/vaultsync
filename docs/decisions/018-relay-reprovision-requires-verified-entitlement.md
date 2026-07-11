# 018 — Relay re-provision requires a verified entitlement

- Context: Relay 1.2.0 requires current signed StoreKit evidence, while existing installs retain registrations created by older app versions.
- Decision: Every provision request requires a locally verified, active Relay entitlement and its signed transaction; no placeholder is sent when that evidence is unavailable.
- Decision: Existing local and remote registration evidence is preserved across verification, network, and partial multi-homeserver failures.
- Decision: Verified migration success is persisted per homeserver; pre-migration success flags mean migration required, not verified.
- Why: This fails closed without interrupting existing wake-ups or forcing users through onboarding and vault setup again.
- Rejected alternative: Clearing registrations before migration, because a transient failure would disable an otherwise working paid setup.
- Rejected alternative: One global migration flag, because one homeserver failure would hide or roll back another homeserver's success.

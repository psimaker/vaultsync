# 011 — Consent dialogs use .alert, never .confirmationDialog

- **Context:** On iOS 26, SwiftUI's `.confirmationDialog` renders as a centered popover WITHOUT its cancel-role button (dismissal only by tapping outside); iOS 18 renders an action sheet with a visible Cancel. The merge-consent dialog (the #54 consent gate) and the vault-removal dialog therefore showed the destructive action as their only visible choice; the device-removal dialog declared no Cancel at all (1.8.0 UI audit, #64, verified empirically on both OS versions).
- **Decision:** Safety- and consent-relevant decisions are presented as `.alert` (or a purpose-built sheet with an explicit Cancel action) — never `.confirmationDialog`. All four existing instances converted (ContentView ×2, DeviceDetailView, ConflictDiffView).
- **Why:** A consent gate whose only visible button is the destructive action funnels users into exactly the action the gate exists to slow down.
- **Rejected alternative:** Keeping `.confirmationDialog` with shortened text so the popover renders smaller — the missing Cancel is independent of text length, and the audit rated the consequence texts as exemplary; shortening trades away clarity for nothing.
- **Links:** #64.

import SwiftUI

struct DeviceDetailView: View {
    let device: SyncthingManager.DeviceInfo
    let syncthingManager: SyncthingManager

    @State private var editedName = ""
    @State private var nameSaved = false
    @State private var showRemoveConfirm = false
    @State private var alertMessage: String?
    @State private var showAlert = false
    @Environment(\.dismiss) private var dismiss

    private var isConnecting: Bool {
        !device.connected && !device.paused
            && syncthingManager.isWithinReconnectGrace(deviceID: device.deviceID)
    }

    /// Same escalation as the device-list row: calm "Connecting…" during
    /// the reconnect grace window, "Paused" for intentionally disabled peers,
    /// neutral "Offline" after the grace — no ✕ for a state that is normal
    /// when the other device is simply not running.
    private var presence: (status: SyncStatus, label: String, symbol: String) {
        if isConnecting {
            return (.starting, L10n.tr("Connecting…"), "hourglass")
        }
        if device.paused {
            return (.paused, L10n.tr("Paused"), "pause.circle.fill")
        }
        if device.connected {
            return (.synced, L10n.tr("Connected"), "checkmark.circle.fill")
        }
        return (.paused, L10n.tr("Offline"), "moon.zzz.fill")
    }

    var body: some View {
        VaultPage {
            StatusChip(text: presence.label, tone: presence.status.tone, systemImage: presence.symbol)
                .accessibilityHint(L10n.tr("Shows whether this Syncthing device is currently reachable."))

            VaultSectionHeader(L10n.tr("Name"))
            VaultCardGroup {
                HStack(spacing: VaultSpacing.m) {
                    TextField(L10n.tr("Device name"), text: $editedName)
                        .foregroundStyle(Color.vaultLabel)
                        .submitLabel(.done)
                        .onSubmit {
                            saveName()
                        }
                    // Transient saved confirmation, mirroring MonoField's
                    // copy feedback — the rename otherwise commits invisibly.
                    if nameSaved {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundStyle(Color.statusSuccess)
                            .transition(.scale.combined(with: .opacity))
                            .accessibilityLabel(L10n.tr("Name saved"))
                    }
                }
                .padding(.horizontal, VaultSpacing.l)
                .frame(minHeight: VaultMetrics.rowMinHeight)
            }

            VaultSectionHeader(L10n.tr("Device ID"))
            MonoField(text: device.deviceID, accessibilityName: L10n.tr("Device ID"))
                .padding(VaultSpacing.m)
                .vaultCard()

            Button(role: .destructive) {
                showRemoveConfirm = true
            } label: {
                Label(L10n.tr("Remove Device"), systemImage: "trash")
            }
            .buttonStyle(.vault(.destructive))
            .padding(.top, VaultSpacing.s)
        }
        .navigationTitle(device.name.isEmpty ? L10n.tr("Unnamed Device") : device.name)
        .navigationBarTitleDisplayMode(.large)
        .onAppear {
            editedName = device.name
            #if DEBUG
            // LAB: present the removal consent immediately for the UI-audit
            // fixture run (#64); reachable only via launch argument.
            if UIAuditFixture.active == UIAuditFixture.deviceRemovalConsent {
                showRemoveConfirm = true
            }
            #endif
        }
        .onDisappear {
            saveName()
        }
        // Consent decisions are presented as .alert, never .confirmationDialog:
        // on iOS 26 the latter renders without a visible Cancel — and this
        // dialog declared none at all, offering "Remove" as the only choice
        // (#64, decision 011).
        .alert(
            "Remove this device?",
            isPresented: $showRemoveConfirm
        ) {
            Button("Remove", role: .destructive) {
                removeDevice()
            }
            Button("Cancel", role: .cancel) { }
        } message: {
            Text("The device will be disconnected and removed from all shared folders.")
        }
        .alert("Error", isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
    }

    private func saveName() {
        let trimmed = editedName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed != device.name else { return }
        if let err = syncthingManager.renameDevice(id: device.deviceID, newName: trimmed) {
            let mapped = SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Rename Failed"))
            alertMessage = mapped.userVisibleDescription
            showAlert = true
            editedName = device.name
            return
        }
        withAnimation(.snappy) { nameSaved = true }
        Task {
            try? await Task.sleep(for: .seconds(1.5))
            withAnimation(.snappy) { nameSaved = false }
        }
    }

    private func removeDevice() {
        if let err = syncthingManager.removeDevice(id: device.deviceID) {
            let mapped = SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Remove Failed"))
            alertMessage = mapped.userVisibleDescription
            showAlert = true
        } else {
            dismiss()
        }
    }
}

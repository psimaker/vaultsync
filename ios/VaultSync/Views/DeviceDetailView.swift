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

    var body: some View {
        List {
            Section("Device") {
                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    Text("Device ID")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    MonoField(text: device.deviceID, accessibilityName: L10n.tr("Device ID"))
                }
                .padding(.vertical, VaultSpacing.xs)

                HStack {
                    Text("Name")
                    Spacer()
                    TextField("Device name", text: $editedName)
                        .multilineTextAlignment(.trailing)
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

                LabeledContent("Status") {
                    // Mirrors the device-list row: calm "Connecting…" during
                    // the reconnect grace window, neutral "Offline" after it —
                    // no ✕ glyph for a state that is normal when the other
                    // device is simply not running.
                    HStack(spacing: 6) {
                        if isConnecting {
                            ProgressView()
                                .controlSize(.small)
                                .tint(Color.statusStarting)
                                .accessibilityHidden(true)
                            Text(L10n.tr("Connecting…"))
                        } else if device.connected {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundStyle(Color.statusSuccess)
                                .accessibilityHidden(true)
                            Text(L10n.tr("Connected"))
                        } else {
                            Image(systemName: "moon.zzz.fill")
                                .foregroundStyle(Color.statusInactive)
                                .accessibilityHidden(true)
                            Text(L10n.tr("Offline"))
                        }
                    }
                    .accessibilityHint("Shows whether this Syncthing device is currently reachable.")
                }
            }

            Section {
                Button(role: .destructive) {
                    showRemoveConfirm = true
                } label: {
                    Label("Remove Device", systemImage: "trash")
                }
            }
        }
        .navigationTitle(device.name.isEmpty ? L10n.tr("Unnamed Device") : device.name)
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            editedName = device.name
        }
        .onDisappear {
            saveName()
        }
        .confirmationDialog(
            "Remove this device?",
            isPresented: $showRemoveConfirm,
            titleVisibility: .visible
        ) {
            Button("Remove", role: .destructive) {
                removeDevice()
            }
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

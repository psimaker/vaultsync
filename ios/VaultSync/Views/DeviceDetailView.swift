import SwiftUI

struct DeviceDetailView: View {
    let device: SyncthingManager.DeviceInfo
    let syncthingManager: SyncthingManager

    @State private var editedName = ""
    @State private var showRemoveConfirm = false
    @State private var alertMessage: String?
    @State private var showAlert = false
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        List {
            Section("Device") {
                LabeledContent("Device ID") {
                    Text(device.deviceID)
                        .font(.system(.caption2, design: .monospaced))
                        .textSelection(.enabled)
                        .lineLimit(2)
                        .multilineTextAlignment(.trailing)
                }

                HStack {
                    Text("Name")
                    Spacer()
                    TextField("Device name", text: $editedName)
                        .multilineTextAlignment(.trailing)
                        .onSubmit {
                            saveName()
                        }
                }

                LabeledContent("Status") {
                    HStack(spacing: 6) {
                        Image(systemName: device.connected ? "checkmark.circle.fill" : "xmark.circle.fill")
                            .foregroundStyle(device.connected ? .green : .secondary)
                            .accessibilityHidden(true)
                        Text(device.connected ? L10n.tr("Connected") : L10n.tr("Disconnected"))
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel(device.connected ? L10n.tr("Connected") : L10n.tr("Disconnected"))
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

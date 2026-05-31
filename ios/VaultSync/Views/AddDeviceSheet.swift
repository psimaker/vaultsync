import SwiftUI

/// The "add a Syncthing device" form. Shared by the main app (Devices tab) and
/// onboarding so the two stay in lockstep instead of drifting as two copies.
/// Owns its own field state, dismisses itself on success, and surfaces add
/// failures through the provided `onError` handler.
struct AddDeviceSheet: View {
    let syncthingManager: SyncthingManager
    /// Called with a user-visible message when adding the device fails.
    var onError: (String) -> Void

    @State private var deviceID = ""
    @State private var name = ""
    @State private var showQRScanner = false
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Form {
                Section("Device ID") {
                    TextField("XXXXXXX-XXXXXXX-...", text: $deviceID)
                        .font(.system(.body, design: .monospaced))
                        .textInputAutocapitalization(.characters)
                        .autocorrectionDisabled()

                    Button {
                        showQRScanner = true
                    } label: {
                        Label("Scan QR Code", systemImage: "qrcode.viewfinder")
                    }
                }
                Section("Name (optional)") {
                    TextField("e.g. My Laptop", text: $name)
                }
            }
            .sheet(isPresented: $showQRScanner) {
                QRScannerView { scannedCode in
                    deviceID = scannedCode
                }
            }
            .navigationTitle("Add Device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") { addDevice() }
                        .disabled(deviceID.isEmpty)
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func addDevice() {
        let id = deviceID.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if let err = syncthingManager.addDevice(id: id, name: trimmedName) {
            onError(SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Could Not Add Device")).userVisibleDescription)
        } else {
            dismiss()
        }
    }
}

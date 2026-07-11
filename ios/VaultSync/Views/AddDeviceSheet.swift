import SwiftUI

/// The "add a Syncthing device" form. Shared by the main app (Devices tab) and
/// onboarding so the two stay in lockstep instead of drifting as two copies.
/// Owns its own field state, dismisses itself on success, and surfaces add
/// failures through the provided `onError` handler.
struct AddDeviceSheet: View {
    let syncthingManager: SyncthingManager
    /// Called with a user-visible message when adding the device fails.
    var onError: (String) -> Void
    /// Called after the device was added successfully, just before the sheet
    /// dismisses itself — the host presents the "confirm this iPhone on your
    /// computer" hint from its onDismiss (#95), because the most common
    /// pairing stall is the desktop never confirming the new device.
    var onAdded: (() -> Void)? = nil

    @State private var deviceID = ""
    @State private var name = ""
    @State private var showQRScanner = false
    @State private var showInvalidQRAlert = false
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
                    // Reject any non-Syncthing QR payload before it reaches the
                    // bridge; a valid scan fills the field in canonical form (#93).
                    if let canonical = SyncthingDeviceID.canonicalize(scannedCode) {
                        deviceID = canonical
                    } else {
                        showInvalidQRAlert = true
                    }
                }
            }
            .alert(L10n.tr("Not a Device QR Code"), isPresented: $showInvalidQRAlert) {
                Button("OK") { }
            } message: {
                Text(L10n.tr("This is not a Syncthing device QR code. On the other device, open Syncthing → Actions → Show ID and scan the QR code shown there."))
            }
            .navigationTitle("Add Device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") { addDevice() }
                        .disabled(deviceID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func addDevice() {
        let id = deviceID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !id.isEmpty else { return }
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if let err = syncthingManager.addDevice(id: id, name: trimmedName) {
            onError(SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Could Not Add Device")).userVisibleDescription)
        } else {
            onAdded?()
            dismiss()
        }
    }
}

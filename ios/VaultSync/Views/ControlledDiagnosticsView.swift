import SwiftUI

struct ControlledDiagnosticsView: View {
    let syncthingManager: SyncthingManager

    @Environment(\.scenePhase) private var scenePhase
    @State private var controller = DiagnosticsPairingController()
    @State private var selectedDeviceID = ""
    @State private var selectedFolderID = ""
    @State private var pastedInvitation = ""
    @State private var showScanner = false
    @State private var showConsent = false
    @State private var consentAction: ConsentAction = .scan
    @State private var showRecoveryConfirmation = false
    @State private var missingFolderRecordID: String?
    @State private var pendingUploadRecordID: String?
    @State private var showUploadConsent = false

    private enum ConsentAction {
        case scan
        case paste
    }

    var body: some View {
        List {
            explanationSection
            if controller.notice == .recoveryRequired {
                recoverySection
            }
            existingPairingsSection
            newPairingSection
            compatibilitySection
        }
        .navigationTitle(L10n.tr("Controlled Diagnostics"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            controller.refresh()
            chooseInitialTarget()
        }
        .onDisappear {
            controller.cancelAllForegroundUploads()
        }
        .onChange(of: scenePhase) { _, phase in
            if phase != .active {
                controller.cancelAllForegroundUploads()
            }
        }
        .onChange(of: selectedDeviceID) { _, _ in
            if !eligibleFolders.contains(where: { $0.id == selectedFolderID }) {
                selectedFolderID = eligibleFolders.first?.id ?? ""
            }
        }
        .sheet(isPresented: $showScanner) {
            QRScannerView(
                title: L10n.tr("Scan Helper Pairing QR"),
                deniedMessage: L10n.tr("VaultSync needs camera access only to scan the helper pairing QR you chose. You can paste the invitation instead."),
                unavailableMessage: L10n.tr("The camera could not be started. Return and paste the helper pairing invitation instead."),
                manualButtonTitle: L10n.tr("Paste Invitation Instead")
            ) { code in
                Task { await startPairing(with: code) }
            }
        }
        .alert(L10n.tr("Authorize diagnostics pairing?"), isPresented: $showConsent) {
            Button(L10n.tr("Cancel"), role: .cancel) { }
            Button(L10n.tr("Continue")) {
                switch consentAction {
                case .scan:
                    showScanner = true
                case .paste:
                    let invitation = pastedInvitation
                    pastedInvitation = ""
                    Task { await startPairing(with: invitation) }
                }
            }
        } message: {
            Text(L10n.tr("Pairing authorizes only controlled diagnostics for the selected server and folder. It does not create a namespace, transfer files, change Syncthing trust, or use Cloud Relay. Namespace setup remains a separate explicit app and operator action."))
        }
        .alert(L10n.tr("Reset local diagnostics credentials?"), isPresented: $showRecoveryConfirmation) {
            Button(L10n.tr("Cancel"), role: .cancel) { }
            Button(L10n.tr("Reset and Re-pair"), role: .destructive) {
                controller.resetOrphanedCredentialsForRepair()
            }
        } message: {
            Text(L10n.tr("This removes only this app's local diagnostics credentials. It does not revoke the old helper authorization. Re-pair with a new QR, then ask the helper operator to revoke the lost app fingerprint."))
        }
        .alert(L10n.tr("Start controlled upload and download check?"), isPresented: $showUploadConsent) {
            Button(L10n.tr("Cancel"), role: .cancel) {
                pendingUploadRecordID = nil
            }
            Button(L10n.tr("Start Upload and Download Check")) {
                startPendingUpload()
            }
        } message: {
            Text(L10n.tr("VaultSync will create one signed request with 256 random bytes in the already authorized diagnostics namespace and rescan only the selected folder. Only an exact signed reply from the pinned helper can mark upload observed. After an accepted upload, VaultSync authorizes exactly one signed helper response file with 256 random bytes in the same namespace; only its fresh synchronized arrival with full validation can mark download observed. Roundtrip remains unobserved. Opaque copies may remain in peers, backups, versions, conflicts, or tombstones."))
        }
    }

    private var explanationSection: some View {
        Section {
            Label(L10n.tr("Explicit local or VPN pairing only"), systemImage: "lock.shield")
            Label(L10n.tr("TLS 1.3 with an exact QR-pinned key"), systemImage: "checkmark.seal")
            Label(L10n.tr("No discovery, trust adoption, Relay tunnel, or automatic namespace"), systemImage: "hand.raised")
            Text(L10n.tr("Pairing and capability checks create no upload, download, or roundtrip evidence. Only a separate explicit check may mark upload observed and, after it, download observed; roundtrip remains independent. The diagnostics namespace is visible to synchronized peers and may remain in backups, versions, conflict copies, and tombstones."))
                .font(.caption)
                .foregroundStyle(.secondary)
        } header: {
            Text(L10n.tr("Security boundary"))
        }
    }

    @ViewBuilder
    private var recoverySection: some View {
        Section {
            Label(L10n.tr("Re-pair required"), systemImage: "exclamationmark.triangle.fill")
                .foregroundStyle(Color.statusAttention)
            Text(L10n.tr("Protected app storage and the diagnostics Keychain no longer match. VaultSync will not reuse the surviving key automatically."))
                .font(.caption)
                .foregroundStyle(.secondary)
            Button(L10n.tr("Reset Local Credentials"), role: .destructive) {
                showRecoveryConfirmation = true
            }
        } header: {
            Text(L10n.tr("Recovery"))
        }
    }

    @ViewBuilder
    private var existingPairingsSection: some View {
        Section {
            if controller.records.isEmpty {
                Text(L10n.tr("No diagnostics helper is paired."))
                    .foregroundStyle(.secondary)
            } else {
                ForEach(controller.records) { record in
                    pairingRow(record)
                }
            }
        } header: {
            Text(L10n.tr("Paired targets"))
        }
    }

    private func pairingRow(_ record: DiagnosticsPairingRecord) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                    Text(deviceName(record.homeserverDeviceID))
                        .font(.headline)
                    Text(folderName(record.folderID))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Text(stateLabel(record.state))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(stateColor(record.state))
            }

            if record.state == .acceptanceReceived, let fingerprint = record.transcriptFingerprint {
                Text(L10n.tr("Compare this fingerprint with the helper operator:"))
                    .font(.caption)
                Text(fingerprint)
                    .font(.system(.title3, design: .monospaced).weight(.bold))
                    .accessibilityLabel(L10n.fmt("Pairing fingerprint %@", fingerprint))
                Button(L10n.tr("Fingerprint Matches — Activate")) {
                    Task { await controller.confirmFingerprintAndActivate(recordID: record.id) }
                }
                .buttonStyle(.borderedProminent)
            }

            preparedPairingAction(record)
            pendingCancellationActions(record)
            activeActions(record)

            if missingFolderRecordID == record.id, folderPath(record.folderID) == nil {
                Label(
                    L10n.tr("The selected folder was renamed or removed. Restore it in Syncthing before retrying."),
                    systemImage: "exclamationmark.folder"
                )
                .font(.caption)
                .foregroundStyle(Color.statusAttention)
            }

            if let error = controller.lastError {
                Label(errorLabel(error), systemImage: "exclamationmark.circle")
                    .font(.caption)
                    .foregroundStyle(error == .unavailable ? Color.statusAttention : Color.statusError)
            }
        }
        .padding(.vertical, VaultSpacing.xs)
    }

    @ViewBuilder
    private func preparedPairingAction(_ record: DiagnosticsPairingRecord) -> some View {
        switch record.state {
        case .requestPrepared, .finalizePrepared, .receiptPrepared, .activatePrepared:
            Button(L10n.tr("Retry Exact Pairing Step")) {
                Task { await controller.retryPreparedPairing(recordID: record.id) }
            }
        case .finalizeAcknowledged, .readyAcknowledged:
            Button(L10n.tr("Continue Verified Pairing")) {
                Task { await controller.confirmFingerprintAndActivate(recordID: record.id) }
            }
        case .lifecyclePending:
            Button(L10n.tr("Continue Credential Rotation")) {
                Task { await controller.continueLifecycle(recordID: record.id) }
            }
            Button(L10n.tr("Abort Pending Credential Rotation"), role: .destructive) {
                Task { await controller.abortLifecycle(recordID: record.id) }
            }
            if controller.canDiscardExpiredLifecycle(record) {
                Button(L10n.tr("Discard Expired Credential Rotation"), role: .destructive) {
                    controller.discardExpiredLifecycle(recordID: record.id)
                }
            }
        case .revocationPrepared:
            Button(L10n.tr("Retry Exact Revocation")) {
                Task { await controller.retryRevocation(recordID: record.id) }
            }
        case .abortPrepared:
            Button(L10n.tr("Retry Exact Pairing Cancellation")) {
                Task { await controller.cancelPendingPairing(recordID: record.id) }
            }
        default:
            EmptyView()
        }
    }

    @ViewBuilder
    private func pendingCancellationActions(_ record: DiagnosticsPairingRecord) -> some View {
        let cancellableStates: Set<DiagnosticsPairingRecord.State> = [
            .requestPrepared, .acceptanceReceived, .finalizePrepared,
            .finalizeAcknowledged, .receiptPrepared, .readyAcknowledged,
        ]
        if cancellableStates.contains(record.state) {
            Button(L10n.tr("Cancel Pending Pairing"), role: .destructive) {
                Task { await controller.cancelPendingPairing(recordID: record.id) }
            }
        }
        if controller.canDiscardExpiredPairing(record) {
            Button(L10n.tr("Discard Expired Pairing"), role: .destructive) {
                controller.discardExpiredPendingPairing(recordID: record.id)
            }
        }
    }

    @ViewBuilder
    private func activeActions(_ record: DiagnosticsPairingRecord) -> some View {
        if isOperational(record.state) {
            let capability = controller.capabilityStates[record.id] ?? .notChecked
            if isStableAuthorization(record.state) {
                HStack {
                    Text(L10n.tr("Capability"))
                        .font(.caption)
                    Spacer()
                    Text(capabilityLabel(capability))
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(capabilityColor(capability))
                }
                Button(L10n.tr("Check Authenticated Capability")) {
                    Task { await controller.checkCapability(recordID: record.id) }
                }
            }

            namespaceAction(record, capability: capability)

            if isStableAuthorization(record.state) {
                NavigationLink {
                    DiagnosticsCredentialMaintenanceView(
                        controller: controller,
                        recordID: record.id
                    )
                } label: {
                    Label(L10n.tr("Rotation, Revocation & Recovery"), systemImage: "key.horizontal")
                }
            }
        }
    }

    @ViewBuilder
    private func namespaceAction(
        _ record: DiagnosticsPairingRecord,
        capability: DiagnosticsPairingController.CapabilityState
    ) -> some View {
        switch record.state {
        case .active where capability == .available:
            Button(L10n.tr("Request Diagnostics Namespace")) {
                Task { await controller.requestNamespaceEnablement(recordID: record.id) }
            }
        case .namespaceEnablementPrepared, .namespaceAwaitingOperator, .namespaceAuthorizationPrepared:
            Text(L10n.tr("The helper operator must explicitly create the exact “VaultSync Diagnostics” namespace for this folder. VaultSync never creates or adopts it automatically."))
                .font(.caption)
                .foregroundStyle(.secondary)
            Button(L10n.tr("Check Explicit Operator Step")) {
                guard let path = folderPath(record.folderID) else {
                    missingFolderRecordID = record.id
                    return
                }
                missingFolderRecordID = nil
                Task { await controller.continueNamespace(recordID: record.id, currentFolderPath: path) }
            }
        case .namespaceAuthorizationRefreshRequired, .namespaceAuthorizationRefreshPrepared:
            Text(L10n.tr("The credential changed. The existing namespace remains unavailable until its next immutable authorization epoch is explicitly completed."))
                .font(.caption)
                .foregroundStyle(Color.statusAttention)
            Button(L10n.tr("Authorize Next Namespace Epoch")) {
                guard let path = folderPath(record.folderID) else {
                    missingFolderRecordID = record.id
                    return
                }
                missingFolderRecordID = nil
                Task {
                    await controller.continueNamespaceAuthorizationRefresh(
                        recordID: record.id,
                        currentFolderPath: path
                    )
                }
            }
        case .namespaceActive:
            Label(L10n.tr("Namespace authorized"), systemImage: "checkmark.shield")
                .font(.caption)
                .foregroundStyle(Color.statusSuccess)
            Text(L10n.fmt(
                "Upload target: %@ · designated peer: %@",
                folderName(record.folderID),
                deviceName(record.homeserverDeviceID)
            ))
            .font(.caption)
            .foregroundStyle(.secondary)
            if let status = controller.uploadStatuses[record.id] {
                Label(uploadStatusLabel(status), systemImage: uploadStatusSymbol(status.phase))
                    .font(.caption)
                    .foregroundStyle(uploadStatusColor(status.phase))
                Text(L10n.tr("Upload, download, and roundtrip are separate evidence fields. Cleanup never upgrades any field."))
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            if let status = controller.uploadStatuses[record.id],
               [.preflighting, .checking, .uploadObserved].contains(status.phase) {
                Button(L10n.tr("Cancel Controlled Check"), role: .cancel) {
                    controller.cancelForegroundUpload(recordID: record.id)
                }
            } else if capability == .available {
                Button(L10n.tr("Start Foreground Upload and Download Check")) {
                    pendingUploadRecordID = record.id
                    showUploadConsent = true
                }
                .buttonStyle(.borderedProminent)
            } else {
                Text(L10n.tr("Check authenticated capability immediately before starting an upload check."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        default:
            EmptyView()
        }
    }

    private var newPairingSection: some View {
        Section {
            if syncthingManager.devices.isEmpty || syncthingManager.folders.isEmpty {
                Text(L10n.tr("Add the existing homeserver device and folder in Syncthing first."))
                    .foregroundStyle(.secondary)
            } else {
                Picker(L10n.tr("Homeserver"), selection: $selectedDeviceID) {
                    ForEach(syncthingManager.devices) { device in
                        Text(device.name.isEmpty ? device.deviceID : device.name)
                            .tag(device.deviceID)
                    }
                }
                Picker(L10n.tr("Folder"), selection: $selectedFolderID) {
                    ForEach(eligibleFolders) { folder in
                        Text(folder.label.isEmpty ? folder.id : folder.label)
                            .tag(folder.id)
                    }
                }

                Button {
                    consentAction = .scan
                    showConsent = true
                } label: {
                    Label(L10n.tr("Scan Helper Pairing QR"), systemImage: "qrcode.viewfinder")
                }
                .disabled(selectedDeviceID.isEmpty || selectedFolderID.isEmpty)

                SecureField(L10n.tr("Paste pairing invitation"), text: $pastedInvitation)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .privacySensitive()
                Button(L10n.tr("Use Pasted Invitation")) {
                    consentAction = .paste
                    showConsent = true
                }
                .disabled(
                    selectedDeviceID.isEmpty || selectedFolderID.isEmpty || pastedInvitation.isEmpty
                )
            }
        } header: {
            Text(L10n.tr("New explicit pairing"))
        } footer: {
            Text(L10n.tr("The helper must first be configured by its operator for a private local, LAN, or VPN endpoint and an exact existing folder. Public defaults and automatic discovery are not supported."))
        }
    }

    private var compatibilitySection: some View {
        Section {
            Text(L10n.tr("Old helper: capability unavailable; no pairing or namespace mutation."))
            Text(L10n.tr("New helper with an old app: diagnostics stays dormant."))
            Text(L10n.tr("Cloud Relay is not used for pairing, capability, namespace, or evidence."))
        } header: {
            Text(L10n.tr("Compatibility"))
        }
        .font(.caption)
        .foregroundStyle(.secondary)
    }

    private var eligibleFolders: [SyncthingManager.FolderInfo] {
        syncthingManager.folders.filter { $0.deviceIDs.contains(selectedDeviceID) }
    }

    private func chooseInitialTarget() {
        if selectedDeviceID.isEmpty {
            selectedDeviceID = syncthingManager.devices.first?.deviceID ?? ""
        }
        if selectedFolderID.isEmpty {
            selectedFolderID = eligibleFolders.first?.id ?? ""
        }
    }

    private func startPairing(with invitation: String) async {
        guard !invitation.isEmpty, !selectedDeviceID.isEmpty, !selectedFolderID.isEmpty else { return }
        await controller.beginPairing(
            qr: invitation,
            homeserverDeviceID: selectedDeviceID,
            folderID: selectedFolderID
        )
        pastedInvitation = ""
    }

    private func deviceName(_ id: String) -> String {
        guard let device = syncthingManager.devices.first(where: { $0.deviceID == id }) else {
            return L10n.tr("Unavailable homeserver")
        }
        return device.name.isEmpty ? id : device.name
    }

    private func folderName(_ id: String) -> String {
        guard let folder = syncthingManager.folders.first(where: { $0.id == id }) else {
            return L10n.tr("Unavailable folder")
        }
        return folder.label.isEmpty ? id : folder.label
    }

    private func folderPath(_ id: String) -> String? {
        syncthingManager.folders.first(where: { $0.id == id })?.path
    }

    private func isOperational(_ state: DiagnosticsPairingRecord.State) -> Bool {
        [.active, .namespaceEnablementPrepared, .namespaceAwaitingOperator,
         .namespaceAuthorizationPrepared, .namespaceActive,
         .namespaceAuthorizationRefreshRequired, .namespaceAuthorizationRefreshPrepared].contains(state)
    }

    private func isStableAuthorization(_ state: DiagnosticsPairingRecord.State) -> Bool {
        state == .active || state == .namespaceActive
    }

    private func stateLabel(_ state: DiagnosticsPairingRecord.State) -> String {
        switch state {
        case .requestPrepared, .finalizePrepared, .finalizeAcknowledged,
             .receiptPrepared, .readyAcknowledged, .activatePrepared, .abortPrepared:
            return L10n.tr("Pairing pending")
        case .acceptanceReceived: return L10n.tr("Confirmation required")
        case .active: return L10n.tr("Paired")
        case .namespaceEnablementPrepared, .namespaceAwaitingOperator:
            return L10n.tr("Operator action required")
        case .namespaceAuthorizationPrepared, .namespaceAuthorizationRefreshPrepared:
            return L10n.tr("Authorization pending")
        case .namespaceAuthorizationRefreshRequired:
            return L10n.tr("Reauthorization required")
        case .namespaceActive: return L10n.tr("Namespace authorized")
        case .lifecyclePending: return L10n.tr("Rotation pending")
        case .revocationPrepared: return L10n.tr("Revocation pending")
        case .revoked: return L10n.tr("Revoked")
        }
    }

    private func stateColor(_ state: DiagnosticsPairingRecord.State) -> Color {
        switch state {
        case .active, .namespaceActive: return Color.statusSuccess
        case .revoked: return .secondary
        case .requestPrepared, .finalizePrepared, .receiptPrepared, .activatePrepared,
             .abortPrepared, .lifecyclePending, .revocationPrepared:
            return Color.statusError
        default: return Color.statusAttention
        }
    }

    private func capabilityLabel(_ state: DiagnosticsPairingController.CapabilityState) -> String {
        switch state {
        case .notChecked: return L10n.tr("Not checked")
        case .checking: return L10n.tr("Checking…")
        case .available: return L10n.tr("Available")
        case .unavailable: return L10n.tr("Unavailable")
        case .unsupported: return L10n.tr("Unsupported")
        }
    }

    private func capabilityColor(_ state: DiagnosticsPairingController.CapabilityState) -> Color {
        switch state {
        case .available: return Color.statusSuccess
        case .unsupported: return Color.statusError
        case .unavailable: return Color.statusAttention
        default: return .secondary
        }
    }

    private func startPendingUpload() {
        guard let recordID = pendingUploadRecordID,
              let record = controller.records.first(where: { $0.id == recordID }) else {
            pendingUploadRecordID = nil
            return
        }
        pendingUploadRecordID = nil
        controller.beginForegroundUpload(
            recordID: record.id,
            preflight: { installationComponent, operationComponent, requireEmptySlot in
                syncthingManager.diagnosticsUploadPreflight(
                    folderID: record.folderID,
                    peerID: record.homeserverDeviceID,
                    installationComponent: installationComponent,
                    operationComponent: operationComponent,
                    requireEmptySlot: requireEmptySlot
                )
            },
            rescan: {
                syncthingManager.rescanFolder(id: record.folderID) == nil
            },
            events: { sinceID in
                // Read events before the generation: if the engine restarts
                // in between, the newer generation fails the caller's
                // continuity check instead of tagging new-engine events with
                // the pre-restart generation.
                let json = SyncBridgeService.getEventsSince(lastID: Int(sinceID))
                let generation = SyncBridgeService.eventStreamGeneration()
                return DiagnosticsResponseProtocol.eventSnapshot(
                    generation: generation,
                    json: json
                )
            }
        )
    }

    private func uploadStatusLabel(_ status: DiagnosticsPairingController.UploadStatus) -> String {
        if status.evidence.uploadObserved {
            switch status.phase {
            case .uploadObserved:
                return L10n.fmt(
                    "Upload observed — download response pending after %d of 8 polls",
                    status.completedResponsePolls
                )
            case .downloadObserved:
                return L10n.tr("Upload and download observed — roundtrip remains unobserved")
            default:
                return L10n.tr("Partial: upload observed, download unobserved — no late result can upgrade it")
            }
        }
        switch status.phase {
        case .preflighting:
            return L10n.tr("Checking exact upload preconditions — no artifact created")
        case .checking:
            return L10n.fmt("Upload pending after %d of 8 exact polls", status.completedPolls)
        case .uploadObserved, .downloadObserved:
            return L10n.tr("Upload and download observed — roundtrip remains unobserved")
        case .cancelled:
            return L10n.tr("Upload check cancelled — no late result can upgrade it")
        case .timedOut:
            return L10n.tr("Upload check timed out — upload unobserved")
        case .interrupted:
            return L10n.tr("Upload check interrupted — upload unobserved")
        case .conflict:
            return L10n.tr("Upload check found an immutable conflict — upload unobserved")
        case .rateLimited:
            return L10n.tr("Upload check rate limited — upload unobserved")
        case .unsupported:
            return L10n.tr("Upload check unsupported for this exact folder and peer")
        case .unavailable:
            return L10n.tr("Upload capability unavailable — no upload evidence")
        }
    }

    private func uploadStatusSymbol(_ phase: DiagnosticsPairingController.UploadPhase) -> String {
        switch phase {
        case .uploadObserved: return "arrow.up.circle.fill"
        case .downloadObserved: return "arrow.down.circle.fill"
        case .preflighting, .checking: return "hourglass"
        case .cancelled, .timedOut, .interrupted, .unavailable: return "exclamationmark.circle"
        case .conflict, .rateLimited, .unsupported: return "xmark.shield"
        }
    }

    private func uploadStatusColor(_ phase: DiagnosticsPairingController.UploadPhase) -> Color {
        switch phase {
        case .uploadObserved, .downloadObserved: return Color.statusSuccess
        case .preflighting, .checking: return Color.statusAttention
        case .cancelled, .timedOut, .interrupted, .unavailable: return Color.statusAttention
        case .conflict, .rateLimited, .unsupported: return Color.statusError
        }
    }

    private func errorLabel(_ error: DiagnosticsProtocolError) -> String {
        switch error {
        case .invalidMessage: return L10n.tr("The authenticated protocol response was invalid.")
        case .expired: return L10n.tr("The signed step expired. Start again with a fresh operator action.")
        case .unavailable: return L10n.tr("Capability unavailable. Nothing was created or transferred.")
        case .unsupported: return L10n.tr("This target is unsupported for controlled diagnostics.")
        case .conflict: return L10n.tr("A conflicting immutable state was found. VaultSync did not adopt or overwrite it.")
        case .rateLimited: return L10n.tr("The helper rate limit was reached. Wait before retrying the exact step.")
        case .protectedDataUnavailable: return L10n.tr("Unlock this device to use protected diagnostics credentials.")
        case .recoveryRequired: return L10n.tr("Re-pair required. Surviving credentials are never trusted automatically.")
        }
    }
}

private struct DiagnosticsCredentialMaintenanceView: View {
    @Bindable var controller: DiagnosticsPairingController
    let recordID: String

    @State private var helperProposal = ""
    @State private var helperProof = ""
    @State private var tlsProposal = ""
    @State private var showAppRotationConfirmation = false
    @State private var showRevocationConfirmation = false

    var body: some View {
        List {
            Section {
                Button(L10n.tr("Rotate App Signing Key")) {
                    showAppRotationConfirmation = true
                }
                Text(L10n.tr("The current app key authorizes the next key. The old key is not retired until the helper acknowledgement and a capability query under the new key both succeed."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Text(L10n.tr("App-key rotations reuse one staged installation key across folder authorizations. Each folder advances separately, and another key generation stays blocked until all non-revoked authorizations catch up."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } header: {
                Text(L10n.tr("App key"))
            }

            Section {
                SecureField(L10n.tr("Helper rotation proposal"), text: $helperProposal)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .privacySensitive()
                SecureField(L10n.tr("Helper new-key proof"), text: $helperProof)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .privacySensitive()
                Button(L10n.tr("Validate and Confirm Helper Key")) {
                    let proposal = helperProposal
                    let proof = helperProof
                    helperProposal = ""
                    helperProof = ""
                    Task {
                        await controller.startHelperKeyRotation(
                            recordID: recordID,
                            proposal: proposal,
                            proof: proof
                        )
                    }
                }
                .disabled(helperProposal.isEmpty || helperProof.isEmpty)
            } header: {
                Text(L10n.tr("Helper key"))
            } footer: {
                Text(L10n.tr("Paste the two values from the explicit local helper-admin rotation. Both old- and new-key signatures must validate."))
            }

            Section {
                SecureField(L10n.tr("TLS pin rotation proposal"), text: $tlsProposal)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .privacySensitive()
                Button(L10n.tr("Validate and Confirm TLS Pin")) {
                    let proposal = tlsProposal
                    tlsProposal = ""
                    Task {
                        await controller.startTLSPinRotation(recordID: recordID, proposal: proposal)
                    }
                }
                .disabled(tlsProposal.isEmpty)
            } header: {
                Text(L10n.tr("TLS pin"))
            } footer: {
                Text(L10n.tr("A certificate renewal with the same pinned key needs no change. A new TLS key requires this signed transition."))
            }

            Section {
                Button(L10n.tr("Revoke This App Authorization"), role: .destructive) {
                    showRevocationConfirmation = true
                }
                Text(L10n.tr("If this app key is lost, pair the replacement as a new installation and revoke the old fingerprint locally on the helper. There is no cloud escrow or trust recovery."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } header: {
                Text(L10n.tr("Revocation and recovery"))
            }
        }
        .navigationTitle(L10n.tr("Credential Maintenance"))
        .navigationBarTitleDisplayMode(.inline)
        .alert(L10n.tr("Rotate the app signing key?"), isPresented: $showAppRotationConfirmation) {
            Button(L10n.tr("Cancel"), role: .cancel) { }
            Button(L10n.tr("Rotate")) {
                Task { await controller.startAppKeyRotation(recordID: recordID) }
            }
        } message: {
            Text(L10n.tr("This is scoped to the selected app, homeserver, and folder. Any authorized namespace will require a new immutable authorization epoch."))
        }
        .alert(L10n.tr("Revoke this app authorization?"), isPresented: $showRevocationConfirmation) {
            Button(L10n.tr("Cancel"), role: .cancel) { }
            Button(L10n.tr("Revoke"), role: .destructive) {
                Task { await controller.revoke(recordID: recordID, reason: .userRequest) }
            }
        } message: {
            Text(L10n.tr("Revocation immediately prevents new controlled diagnostics operations for this exact authorization. It does not delete Syncthing data, namespace history, backups, versions, conflicts, or tombstones."))
        }
    }
}

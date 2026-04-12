import SwiftUI

struct ContentView: View {
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    @State private var showAddDevice = false
    @State private var showSettings = false
    @State private var showObsidianPicker = false
    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var pendingShareFailures: [String: SyncUserError] = [:]
    @State private var pendingShareInFlight: Set<String> = []
    @State private var isRescanning = false

    var body: some View {
        NavigationStack {
            List {
                dashboardSection
                syncIssuesSection
                obsidianStatusSection
                pendingSharesSection
                vaultsSection
                devicesSection
            }
            .navigationTitle("VaultSync")
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        showSettings = true
                    } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityLabel("Open Settings")
                    .accessibilityHint("Opens discovery, relay, and notification settings.")
                }
            }
            .alert("Error", isPresented: $showAlert) {
                Button("OK") { }
            } message: {
                Text(alertMessage ?? "")
            }
            .sheet(isPresented: $showAddDevice) {
                addDeviceSheet
            }
            .sheet(isPresented: $showSettings) {
                SettingsView(syncthingManager: syncthingManager, vaultManager: vaultManager, subscriptionManager: subscriptionManager)
            }
            .sheet(isPresented: $showObsidianPicker) {
                FolderPicker(initialDirectoryURL: vaultManager.obsidianDirectoryURL, onCancel: {
                    showObsidianPicker = false
                }) { url in
                    showObsidianPicker = false
                    if let err = vaultManager.grantAccess(url: url) {
                        alertMessage = mappedError(err, fallbackTitle: L10n.tr("Obsidian Folder Connection Failed")).userVisibleDescription
                        showAlert = true
                    }
                }
            }
            .onChange(of: syncthingManager.pendingFolders, initial: true) { _, pending in
                autoAcceptPendingShares(pending)
            }
        }
    }

    // MARK: - Auto-Accept Pending Shares

    private func autoAcceptPendingShares(_ pending: [SyncthingManager.PendingFolderInfo]) {
        let pendingIDs = Set(pending.map(\.id))
        pendingShareFailures = pendingShareFailures.filter { pendingIDs.contains($0.key) }
        pendingShareInFlight = pendingShareInFlight.intersection(pendingIDs)

        guard vaultManager.isAccessible else { return }

        for folder in syncthingManager.actionablePendingFolders where pendingShareFailures[folder.id] == nil {
            guard !pendingShareInFlight.contains(folder.id) else { continue }
            performPendingShareAccept(folder: folder, source: .automatic)
        }
    }

    private enum PendingShareAcceptSource {
        case automatic
        case manual
    }

    private func performPendingShareAccept(
        folder: SyncthingManager.PendingFolderInfo,
        source: PendingShareAcceptSource
    ) {
        pendingShareInFlight.insert(folder.id)

        let err = vaultManager.acceptPendingShare(folder: folder, syncthingManager: syncthingManager)
        pendingShareInFlight.remove(folder.id)

        if let err {
            let userError = mappedError(err, fallbackTitle: L10n.tr("Could Not Accept Share"))
            pendingShareFailures[folder.id] = userError
            if source == .automatic {
                alertMessage = L10n.fmt(
                    "Could not accept share '%@'.\n\n%@",
                    folder.label.isEmpty ? folder.id : folder.label,
                    userError.userVisibleDescription
                )
                showAlert = true
            }
            return
        }

        pendingShareFailures.removeValue(forKey: folder.id)
        syncthingManager.unignorePendingFolder(id: folder.id)
    }

    private func retryPendingShare(_ folder: SyncthingManager.PendingFolderInfo) {
        pendingShareFailures.removeValue(forKey: folder.id)
        performPendingShareAccept(folder: folder, source: .manual)
    }

    private func ignorePendingShare(_ folder: SyncthingManager.PendingFolderInfo) {
        syncthingManager.ignorePendingFolder(id: folder.id)
        pendingShareFailures.removeValue(forKey: folder.id)
    }

    // MARK: - Dashboard Section

    private var dashboardSection: some View {
        Section {
            HStack(spacing: 12) {
                Image(systemName: syncStatusIcon)
                    .font(.title2)
                    .foregroundStyle(syncStatusColor)
                    .symbolEffect(.pulse, isActive: isSyncing)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 2) {
                    Text(syncStatusText)
                        .font(.headline)
                    if let lastSync = syncthingManager.lastSyncTime {
                        Text("\(L10n.tr("Last sync:")) \(lastSync, style: .relative) \(L10n.tr("ago"))")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    if let staleWarning = syncthingManager.staleSyncWarning {
                        Text(staleWarning)
                            .font(.caption2)
                            .foregroundStyle(.orange)
                    }
                    if let backgroundOutcome = syncthingManager.lastBackgroundSyncOutcome,
                       backgroundOutcome.result.shouldSurfaceIssue {
                        Text(L10n.fmt("Background sync: %@", backgroundOutcome.result.issueTitle))
                            .font(.caption2)
                            .foregroundStyle(.orange)
                    }
                }

                Spacer()

                HStack(spacing: 4) {
                    Image(systemName: syncthingManager.isRunning ? "checkmark.circle.fill" : "xmark.circle.fill")
                        .foregroundStyle(syncthingManager.isRunning ? .green : .secondary)
                        .accessibilityHidden(true)
                    Text(syncthingManager.isRunning ? L10n.tr("Running") : L10n.tr("Stopped"))
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
                .accessibilityLabel(syncthingManager.isRunning ? L10n.tr("Sync engine running") : L10n.tr("Sync engine stopped"))
                .accessibilityHint(L10n.tr("Shows whether Syncthing is currently active."))
            }
            .accessibilityElement(children: .combine)

            if subscriptionManager.isRelaySubscribed {
                HStack {
                    Image(systemName: "antenna.radiowaves.left.and.right")
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                    Text("Cloud Relay active")
                        .font(.subheadline)
                        .foregroundStyle(.green)
                }
                .accessibilityElement(children: .combine)
            }

            if syncthingManager.isRunning {
                let connected = syncthingManager.devices.filter(\.connected).count
                let total = syncthingManager.devices.count
                HStack {
                    Image(systemName: "network")
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                    if total == 0 {
                        Text("No devices configured")
                            .foregroundStyle(.secondary)
                    } else {
                        Text(L10n.fmt("%d of %d devices connected", connected, total))
                            .foregroundStyle(connected > 0 ? Color.primary : Color.orange)
                    }
                }
                .font(.subheadline)
                .accessibilityElement(children: .combine)
            }

            if let error = currentSyncError {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                        .accessibilityHidden(true)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(error.title)
                            .font(.subheadline.weight(.semibold))
                        Text(error.message)
                            .font(.caption)
                        Text(error.remediation)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    .foregroundStyle(.red)
                }
                .accessibilityElement(children: .combine)
                if let url = troubleshootingURL(for: error) {
                    Link("Learn how to fix", destination: url)
                        .font(.caption2)
                }
            }

            let errorFolders = foldersWithErrors
            if !errorFolders.isEmpty {
                ForEach(errorFolders, id: \.self) { folderID in
                    let folder = syncthingManager.folders.first { $0.id == folderID }
                    let folderError = syncthingManager.folderUserError(folderID: folderID)
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.circle.fill")
                            .foregroundStyle(.orange)
                            .accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(folder?.label ?? folderID)
                                .font(.subheadline.weight(.semibold))
                            Text(folderError?.message ?? L10n.tr("Folder is currently in an error state."))
                                .font(.caption)
                            if let remediation = folderError?.remediation {
                                Text(remediation)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            if let folderError,
                               let url = troubleshootingURL(for: folderError) {
                                Link("Learn how to fix", destination: url)
                                    .font(.caption2)
                            }
                        }
                    }
                    .accessibilityElement(children: .combine)
                }
            }
        }
    }

    private var isSyncing: Bool {
        syncthingManager.folderStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }
    }

    private var syncStatusIcon: String {
        if currentSyncError != nil { return "exclamationmark.triangle.fill" }
        if !syncthingManager.isRunning { return "arrow.triangle.2.circlepath" }
        if !foldersWithErrors.isEmpty { return "exclamationmark.circle" }
        if isSyncing { return "arrow.triangle.2.circlepath" }
        return "checkmark.circle.fill"
    }

    private var syncStatusColor: Color {
        if currentSyncError != nil { return .red }
        if !syncthingManager.isRunning { return .gray }
        if !foldersWithErrors.isEmpty { return .orange }
        if isSyncing { return .blue }
        return .green
    }

    private var syncStatusText: String {
        if currentSyncError != nil { return L10n.tr("Error") }
        if !syncthingManager.isRunning { return L10n.tr("Starting…") }
        if !foldersWithErrors.isEmpty { return L10n.tr("Sync Issue") }
        if isSyncing { return L10n.tr("Syncing…") }
        if syncthingManager.folders.isEmpty { return L10n.tr("Ready") }
        return L10n.tr("All Synced")
    }

    private var foldersWithErrors: [String] {
        syncthingManager.folderIDsWithErrors
    }

    private var currentSyncError: SyncUserError? {
        if let userError = syncthingManager.userError {
            return userError
        }
        if let error = syncthingManager.error {
            return mappedError(error)
        }
        return nil
    }

    // MARK: - Obsidian Status Section

    @ViewBuilder
    private var obsidianStatusSection: some View {
        if !vaultManager.isAccessible {
            Section {
                VStack(alignment: .leading, spacing: 14) {
                    Label(
                        vaultManager.needsReconnect ? "Obsidian access expired" : "Obsidian folder not connected",
                        systemImage: "folder.badge.questionmark"
                    )
                        .foregroundStyle(.orange)

                    if let issue = vaultManager.accessIssue {
                        Text(issue.message)
                            .font(.caption)
                            .foregroundStyle(.orange)
                        Text(issue.remediation)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                        if let url = SyncUserError.troubleshootingURL(anchor: vaultManager.needsReconnect ? "bookmark-access-expired" : "obsidian-folder-not-found") {
                            Link("Learn how to fix", destination: url)
                                .font(.caption2)
                        }
                    } else {
                        Text("VaultSync needs one-time access to your Obsidian folder before it can accept shares.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }

                    Button {
                        showObsidianPicker = true
                    } label: {
                        HStack(spacing: 8) {
                            Image(systemName: vaultManager.needsReconnect ? "arrow.clockwise" : "folder.badge.plus")
                            Text(vaultManager.needsReconnect ? "Reconnect Obsidian Folder" : "Connect Obsidian Folder")
                        }
                        .frame(maxWidth: .infinity, alignment: .center)
                    }
                    .buttonStyle(.borderedProminent)
                    .frame(maxWidth: .infinity)

                    Text("In the picker, choose \"On My iPhone\" → \"Obsidian\", then tap Open.")
                        .font(.caption)
                        .foregroundStyle(.tertiary)

                    DisclosureGroup("Can't find the Obsidian folder?") {
                        Text("Install Obsidian from the App Store and open it once. The folder appears after Obsidian creates it.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 4)
                    }
                    .font(.caption)
                    .foregroundStyle(.secondary)
                }
                .padding(.vertical, 6)
            }
        }
    }

    // MARK: - Pending Shares Section

    @ViewBuilder
    private var pendingSharesSection: some View {
        let pendingFolders = syncthingManager.actionablePendingFolders
        let ignoredFolders = syncthingManager.ignoredPendingFolders
        if !pendingFolders.isEmpty || !ignoredFolders.isEmpty {
            Section("Pending Shares") {
                PendingSharesView(
                    pendingFolders: pendingFolders,
                    ignoredFolders: ignoredFolders,
                    failureByFolderID: pendingShareFailures,
                    inFlightFolderIDs: pendingShareInFlight,
                    obsidianAccessible: vaultManager.isAccessible,
                    onAccept: { folder in
                        performPendingShareAccept(folder: folder, source: .manual)
                    },
                    onRetry: { folder in
                        retryPendingShare(folder)
                    },
                    onIgnore: { folder in
                        ignorePendingShare(folder)
                    },
                    onRestoreIgnored: { folder in
                        syncthingManager.unignorePendingFolder(id: folder.id)
                    },
                    onReconnectObsidian: {
                        showObsidianPicker = true
                    }
                )
            }
        }
    }

    // MARK: - Sync Issues Section

    @ViewBuilder
    private var syncIssuesSection: some View {
        let issues = syncthingManager.unresolvedIssues
        if !issues.isEmpty {
            Section("Sync Issues") {
                SyncIssuesView(
                    issues: issues,
                    syncthingManager: syncthingManager,
                    onRescanFailedFolders: rescanFailedVaults,
                    onOpenAddDevice: { showAddDevice = true },
                    onAcceptFirstPendingShare: acceptFirstPendingShareFromIssues,
                    onRescanAllVaults: rescanAllVaults
                )
            }
        }
    }

    private func acceptFirstPendingShareFromIssues() {
        guard let first = syncthingManager.actionablePendingFolders.first else { return }
        performPendingShareAccept(folder: first, source: .manual)
    }

    private func rescanFailedVaults() {
        rescanFolders(ids: syncthingManager.folderIDsWithErrors)
    }

    private func rescanAllVaults() {
        rescanFolders(ids: syncthingManager.folders.map(\.id))
    }

    private func rescanFolders(ids: [String]) {
        let uniqueIDs = Array(Set(ids)).sorted()
        guard !uniqueIDs.isEmpty else { return }

        var failures: [String] = []
        for id in uniqueIDs {
            if let err = syncthingManager.rescanFolder(id: id) {
                let folderName = syncthingManager.folders.first(where: { $0.id == id })?.label ?? id
                let userError = mappedError(err, fallbackTitle: L10n.tr("Rescan Failed"))
                failures.append(L10n.fmt("%@: %@", folderName, userError.message))
            }
        }

        if !failures.isEmpty {
            alertMessage = failures.joined(separator: "\n")
            showAlert = true
        }
    }

    // MARK: - Vaults Section

    private var vaultsSection: some View {
        Section("Obsidian Vaults") {
            if syncthingManager.folders.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    if vaultManager.isAccessible {
                        if vaultManager.detectedVaults.isEmpty {
                            Label("No vaults found", systemImage: "folder.badge.questionmark")
                                .foregroundStyle(.secondary)
                            Text("Create a vault in Obsidian first. VaultSync will detect it automatically.")
                                .font(.caption)
                                .foregroundStyle(.tertiary)
                        } else {
                            Label("No folders syncing yet", systemImage: "arrow.triangle.2.circlepath")
                                .foregroundStyle(.secondary)
                            Text("Share a folder from your desktop Syncthing — it will be accepted automatically.")
                                .font(.caption)
                                .foregroundStyle(.tertiary)
                        }
                    } else {
                        Label("Connect to Obsidian first", systemImage: "folder.badge.gearshape")
                            .foregroundStyle(.secondary)
                    }
                }
                .padding(.vertical, 4)
            } else {
                ForEach(syncthingManager.folders) { folder in
                    NavigationLink {
                        vaultDetailView(folder)
                    } label: {
                        folderRow(folder)
                    }
                }
            }
        }
    }

    private func folderRow(_ folder: SyncthingManager.FolderInfo) -> some View {
        let status = syncthingManager.folderStatuses[folder.id]
        let conflicts = syncthingManager.conflictFiles[folder.id] ?? []
        return HStack {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(folder.label.isEmpty ? folder.id : folder.label)
                        .font(.body)
                    if !conflicts.isEmpty {
                        Text("\(conflicts.count)")
                            .font(.caption2.bold())
                            .foregroundStyle(.white)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 1)
                            .background(.orange, in: Capsule())
                            .accessibilityLabel(L10n.fmt("%d conflicts", conflicts.count))
                    }
                }
                if let status {
                    HStack(spacing: 4) {
                        Text(localizedState(status.state))
                            .font(.caption2)
                        if status.completionPct < 100, status.completionPct > 0 {
                            Text(L10n.fmt("(%d%%)", Int(status.completionPct)))
                                .font(.caption2)
                        }
                    }
                    .foregroundStyle(stateColor(status.state))
                }
            }
            Spacer()
            Image(systemName: stateIcon(status?.state ?? "unknown"))
                .foregroundStyle(stateColor(status?.state ?? "unknown"))
                .font(.caption2)
                .accessibilityHidden(true)
        }
        .accessibilityElement(children: .combine)
    }

    private func isFolderSyncing(_ status: SyncthingManager.FolderStatusInfo?) -> Bool {
        guard let state = status?.state else { return false }
        return state == "syncing" || state == "scanning"
    }

    private func stateIcon(_ state: String) -> String {
        switch state {
        case "idle": "checkmark.circle.fill"
        case "scanning", "syncing": "arrow.triangle.2.circlepath"
        case "error": "exclamationmark.circle.fill"
        default: "questionmark.circle"
        }
    }

    private func stateColor(_ state: String) -> Color {
        switch state {
        case "idle": .green
        case "scanning", "syncing": .blue
        case "error": .red
        default: .gray
        }
    }

    // MARK: - Vault Detail

    private func vaultDetailView(_ folder: SyncthingManager.FolderInfo) -> some View {
        let status = syncthingManager.folderStatuses[folder.id]
        let conflicts = syncthingManager.conflictFiles[folder.id] ?? []
        return List {
            Section("Vault") {
                LabeledContent("Name", value: folder.label.isEmpty ? folder.id : folder.label)
                LabeledContent("Path", value: folder.path)
            }

            Section("Sync Status") {
                LabeledContent("State") {
                    HStack(spacing: 6) {
                        Image(systemName: stateIcon(status?.state ?? "unknown"))
                            .foregroundStyle(stateColor(status?.state ?? "unknown"))
                            .font(.caption2)
                            .accessibilityHidden(true)
                        Text(localizedState(status?.state ?? "unknown"))
                    }
                    .accessibilityElement(children: .combine)
                }
                if let status {
                    LabeledContent("Completion", value: "\(Int(status.completionPct))%")
                    LabeledContent("Local Files", value: "\(status.localFiles)")
                    LabeledContent("Global Files", value: "\(status.globalFiles)")
                    if status.state == "error",
                       let folderError = syncthingManager.folderUserError(folderID: folder.id) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(folderError.message)
                                .font(.caption)
                            Text(folderError.remediation)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                            if let url = troubleshootingURL(for: folderError) {
                                Link("Learn how to fix", destination: url)
                                    .font(.caption2)
                            }
                        }
                    }
                }
            }

            if !conflicts.isEmpty {
                Section {
                    NavigationLink {
                        ConflictListView(
                            folderID: folder.id,
                            conflicts: conflicts,
                            syncthingManager: syncthingManager
                        )
                    } label: {
                        HStack {
                            Label("Conflicts", systemImage: "exclamationmark.triangle")
                                .foregroundStyle(.orange)
                            Spacer()
                            Text("\(conflicts.count)")
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }

            Section("Shared With") {
                ForEach(syncthingManager.devices) { device in
                    let isShared = folder.deviceIDs.contains(device.deviceID)
                    Button {
                        toggleDeviceSharing(folderID: folder.id, deviceID: device.deviceID, isShared: isShared)
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(device.name.isEmpty ? L10n.tr("Unnamed") : device.name)
                                    .font(.body)
                                Text(device.deviceID)
                                    .font(.system(.caption2, design: .monospaced))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                            Spacer()
                            Label(isShared ? L10n.tr("Shared") : L10n.tr("Not Shared"), systemImage: isShared ? "checkmark.circle.fill" : "circle")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(isShared ? .blue : .secondary)
                                .accessibilityHidden(true)
                        }
                    }
                    .tint(.primary)
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel(device.name.isEmpty ? L10n.tr("Unnamed device") : device.name)
                    .accessibilityValue(isShared ? L10n.tr("Shared") : L10n.tr("Not shared"))
                    .accessibilityHint(isShared ? "Double-tap to stop sharing this vault with this device." : "Double-tap to share this vault with this device.")
                }
                if syncthingManager.devices.isEmpty {
                    Text("No devices configured")
                        .foregroundStyle(.secondary)
                }
            }

            Section {
                Button {
                    isRescanning = true
                    if let err = syncthingManager.rescanFolder(id: folder.id) {
                        alertMessage = mappedError(err, fallbackTitle: L10n.tr("Rescan Failed")).userVisibleDescription
                        showAlert = true
                        isRescanning = false
                    } else {
                        Task {
                            try? await Task.sleep(for: .seconds(2))
                            isRescanning = false
                        }
                    }
                } label: {
                    HStack {
                        Text(isRescanning ? "Rescanning…" : "Rescan Vault")
                        Spacer()
                        if isRescanning {
                            ProgressView()
                                .controlSize(.small)
                        }
                    }
                }
                .disabled(isRescanning)
            }
        }
        .onDisappear {
            isRescanning = false
        }
        .navigationTitle(folder.label.isEmpty ? folder.id : folder.label)
        .navigationBarTitleDisplayMode(.inline)
    }

    private func toggleDeviceSharing(folderID: String, deviceID: String, isShared: Bool) {
        let result: String?
        if isShared {
            result = syncthingManager.unshareFolderFromDevice(folderID: folderID, deviceID: deviceID)
        } else {
            result = syncthingManager.shareFolderWithDevice(folderID: folderID, deviceID: deviceID)
        }
        if let err = result {
            alertMessage = mappedError(err).userVisibleDescription
            showAlert = true
        }
    }

    // MARK: - Devices Section

    private var devicesSection: some View {
        Section {
            if syncthingManager.devices.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    Label("No devices connected", systemImage: "laptopcomputer.and.iphone")
                        .foregroundStyle(.secondary)
                    Text("Add a device using its Syncthing Device ID. Find it in the Syncthing web UI under Actions > Show ID.")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
                .padding(.vertical, 4)
            } else {
                ForEach(syncthingManager.devices) { device in
                    NavigationLink {
                        DeviceDetailView(
                            device: device,
                            syncthingManager: syncthingManager
                        )
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(device.name.isEmpty ? L10n.tr("Unnamed") : device.name)
                                    .font(.body)
                                Text(device.deviceID)
                                    .font(.system(.caption2, design: .monospaced))
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                            Spacer()
                            HStack(spacing: 4) {
                                Image(systemName: device.connected ? "checkmark.circle.fill" : "xmark.circle.fill")
                                    .foregroundStyle(device.connected ? .green : .secondary)
                                    .accessibilityHidden(true)
                                Text(device.connected ? L10n.tr("Connected") : L10n.tr("Offline"))
                                    .font(.caption2.weight(.semibold))
                                    .foregroundStyle(.secondary)
                            }
                            .accessibilityElement(children: .combine)
                        }
                    }
                }
            }
        } header: {
            HStack {
                Text("Devices")
                Spacer()
                if syncthingManager.isRunning {
                    Button {
                        showAddDevice = true
                    } label: {
                        Image(systemName: "plus")
                    }
                    .accessibilityLabel("Add Device")
                    .accessibilityHint("Opens the form to add a Syncthing device.")
                }
            }
        }
    }

    // MARK: - Add Device Sheet

    @State private var newDeviceID = ""
    @State private var newDeviceName = ""
    @State private var showQRScanner = false

    private var addDeviceSheet: some View {
        NavigationStack {
            Form {
                Section("Device ID") {
                    TextField("XXXXXXX-XXXXXXX-...", text: $newDeviceID)
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
                    TextField("e.g. My Laptop", text: $newDeviceName)
                }
            }
            .sheet(isPresented: $showQRScanner) {
                QRScannerView { scannedCode in
                    newDeviceID = scannedCode
                }
            }
            .navigationTitle("Add Device")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        resetAddDeviceForm()
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") {
                        addDevice()
                    }
                    .disabled(newDeviceID.isEmpty)
                }
            }
        }
        .presentationDetents([.medium])
    }

    private func addDevice() {
        let id = newDeviceID.trimmingCharacters(in: .whitespacesAndNewlines)
        let name = newDeviceName.trimmingCharacters(in: .whitespacesAndNewlines)

        if let err = syncthingManager.addDevice(id: id, name: name) {
            alertMessage = mappedError(err, fallbackTitle: L10n.tr("Could Not Add Device")).userVisibleDescription
            showAlert = true
        } else {
            resetAddDeviceForm()
        }
    }

    private func resetAddDeviceForm() {
        newDeviceID = ""
        newDeviceName = ""
        showAddDevice = false
    }

    // MARK: - Error Helpers

    private func mappedError(_ error: String, fallbackTitle: String = L10n.tr("Sync Error")) -> SyncUserError {
        SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle)
    }

    private func troubleshootingURL(for error: SyncUserError) -> URL? {
        SyncUserError.troubleshootingURL(for: error)
    }

    private func localizedState(_ state: String) -> String {
        switch state.lowercased() {
        case "idle":
            return L10n.tr("Idle")
        case "scanning":
            return L10n.tr("Scanning")
        case "syncing":
            return L10n.tr("Syncing")
        case "error":
            return L10n.tr("Error")
        default:
            return L10n.tr("Unknown")
        }
    }
}

#Preview {
    ContentView(
        syncthingManager: SyncthingManager(),
        vaultManager: VaultManager(),
        subscriptionManager: SubscriptionManager()
    )
}

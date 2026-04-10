import SwiftUI
import UIKit

struct OnboardingView: View {
    @Binding var hasCompletedOnboarding: Bool
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager

    @State private var checklistViewModel: SetupChecklistViewModel
    @State private var showWelcome = true
    @State private var newDeviceID = ""
    @State private var newDeviceName = ""
    @State private var showQRScanner = false
    @State private var showObsidianPicker = false
    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var showContinueAnywayDialog = false
    @State private var focusedRequirement: SetupChecklistViewModel.Requirement?
    @State private var deviceIDCopied = false
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.openURL) private var openURL

    init(
        hasCompletedOnboarding: Binding<Bool>,
        syncthingManager: SyncthingManager,
        vaultManager: VaultManager,
        subscriptionManager: SubscriptionManager
    ) {
        _hasCompletedOnboarding = hasCompletedOnboarding
        self.syncthingManager = syncthingManager
        self.vaultManager = vaultManager
        self.subscriptionManager = subscriptionManager
        _checklistViewModel = State(
            initialValue: SetupChecklistViewModel(
                syncthingManager: syncthingManager,
                vaultManager: vaultManager,
                subscriptionManager: subscriptionManager
            )
        )
    }

    var body: some View {
        NavigationStack {
            if showWelcome {
                welcomeScreen
            } else {
                setupFlow
            }
        }
        .alert("Setup Issue", isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
        .confirmationDialog("Continue without finishing setup?", isPresented: $showContinueAnywayDialog, titleVisibility: .visible) {
            Button("Continue Without Finishing") {
                completeOnboarding()
            }
            Button("Cancel", role: .cancel) { }
        } message: {
            Text(checklistViewModel.continueWarningText)
        }
        .sheet(isPresented: $showObsidianPicker) {
            FolderPicker(initialDirectoryURL: vaultManager.obsidianDirectoryURL, onCancel: {
                showObsidianPicker = false
            }) { url in
                showObsidianPicker = false
                if let err = vaultManager.grantAccess(url: url) {
                    alertMessage = mappedError(err, fallbackTitle: "Obsidian Folder Connection Failed").userVisibleDescription
                    showAlert = true
                }
            }
        }
        .sheet(isPresented: $showQRScanner) {
            QRScannerView { scannedCode in
                newDeviceID = scannedCode
            }
        }
        .onAppear {
            vaultManager.restoreAccess()
            syncthingManager.start()
        }
    }

    // MARK: - Welcome Screen

    private var welcomeScreen: some View {
        ScrollView {
            VStack(spacing: 32) {
                Spacer(minLength: 20)

                VStack(spacing: 12) {
                    Image(systemName: "arrow.triangle.2.circlepath.circle.fill")
                        .font(.system(size: 56))
                        .foregroundStyle(Color.accentColor)
                        .accessibilityHidden(true)
                    Text("Welcome to VaultSync")
                        .font(.largeTitle.bold())
                    Text("Sync your Obsidian vaults privately with Syncthing — no cloud required.")
                        .font(.body)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)

                VStack(alignment: .leading, spacing: 8) {
                    featureRow(icon: "lock.shield", text: "End-to-end encrypted")
                    featureRow(icon: "server.rack", text: "No cloud required")
                    featureRow(icon: "bolt", text: "Fast Markdown sync")
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemBackground))
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))

                VStack(alignment: .leading, spacing: 16) {
                    Text("Before you start")
                        .font(.title3.bold())

                    prerequisiteRow(
                        icon: "desktopcomputer",
                        title: "Syncthing on your desktop",
                        description: "Install and run Syncthing on the computer you sync with."
                    )

                    Link(destination: URL(string: "https://syncthing.net/downloads/")!) {
                        Label("Download Syncthing", systemImage: "arrow.down.circle")
                            .font(.subheadline)
                    }
                    .padding(.leading, 36)

                    prerequisiteRow(
                        icon: "books.vertical",
                        title: "Obsidian on both devices",
                        description: "Install Obsidian on this iPhone and on your desktop."
                    )
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                VStack(alignment: .leading, spacing: 12) {
                    Text("How pairing works")
                        .font(.title3.bold())

                    pairingStepRow(number: "1", text: "Both devices exchange Device IDs")
                    pairingStepRow(number: "2", text: "Your desktop shares a vault folder")
                    pairingStepRow(number: "3", text: "VaultSync keeps everything in sync")
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                Button {
                    withAnimation(.easeInOut(duration: 0.3)) {
                        showWelcome = false
                    }
                } label: {
                    Text("Let's Get Started")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .padding(.top, 8)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
        }
        .navigationTitle("VaultSync")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func prerequisiteRow(icon: String, title: String, description: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: icon)
                .font(.title3)
                .foregroundStyle(Color.accentColor)
                .frame(width: 24, alignment: .center)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.subheadline.bold())
                Text(description)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func pairingStepRow(number: String, text: String) -> some View {
        HStack(spacing: 12) {
            Text(number)
                .font(.caption.bold().monospacedDigit())
                .foregroundStyle(.white)
                .frame(width: 22, height: 22)
                .background(Color.accentColor, in: Circle())
                .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
        }
        .accessibilityElement(children: .combine)
    }

    // MARK: - Setup Flow

    private var setupFlow: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    SetupChecklistView(viewModel: checklistViewModel) { requirement in
                        focusedRequirement = requirement
                    }

                    pairingCard
                        .id(SetupChecklistViewModel.Requirement.syncthingRunning)

                    obsidianCard
                        .id(SetupChecklistViewModel.Requirement.obsidianConnected)

                    firstShareCard
                        .id(SetupChecklistViewModel.Requirement.firstShareDetectedOrAccepted)

                    relayCard
                        .id(SetupChecklistViewModel.Requirement.relayConfigured)

                    completionCard
                }
                .padding(.horizontal, 20)
                .padding(.vertical, 16)
            }
            .navigationTitle("VaultSync Setup")
            .navigationBarTitleDisplayMode(.inline)
            .onChange(of: focusedRequirement) { _, requirement in
                guard let requirement else { return }
                // Both pairing requirements scroll to the same card
                let scrollTarget: SetupChecklistViewModel.Requirement
                switch requirement {
                case .desktopDeviceAdded:
                    scrollTarget = .syncthingRunning
                default:
                    scrollTarget = requirement
                }
                withAnimation(.easeInOut) {
                    proxy.scrollTo(scrollTarget, anchor: .top)
                }
            }
        }
    }

    // MARK: - Pairing Card (merged steps 1 + 2)

    private var pairingCard: some View {
        VStack(alignment: .leading, spacing: 16) {
            Label("1. Pair your devices", systemImage: "arrow.left.arrow.right")
                .font(.headline)

            Text("Syncthing requires both devices to know each other. Complete both parts below.")
                .font(.subheadline)
                .foregroundStyle(.secondary)

            // Part A: iPhone Device ID
            VStack(alignment: .leading, spacing: 8) {
                Text("A — Share this iPhone's Device ID")
                    .font(.subheadline.weight(.semibold))

                if syncthingManager.deviceID.isEmpty {
                    if let userError = syncthingManager.userError {
                        Label(userError.message, systemImage: "exclamationmark.triangle.fill")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(.orange)
                        Text(userError.remediation)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        if let url = troubleshootingURL(for: userError) {
                            Link("Learn how to fix", destination: url)
                                .font(.caption2)
                        }
                    } else {
                        ProgressView("Starting Syncthing…")
                            .foregroundStyle(.secondary)
                        Text("Keep VaultSync open for a moment. Your Device ID will appear here.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                } else {
                    Text("Copy this ID and add it as a new remote device in your desktop Syncthing.")
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    Text(syncthingManager.deviceID)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .padding(10)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color(.secondarySystemBackground))
                        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))

                    Button {
                        UIPasteboard.general.string = syncthingManager.deviceID
                        deviceIDCopied = true
                        Task {
                            try? await Task.sleep(for: .seconds(2))
                            deviceIDCopied = false
                        }
                    } label: {
                        Label(
                            deviceIDCopied ? "Copied!" : "Copy Device ID",
                            systemImage: deviceIDCopied ? "checkmark" : "doc.on.doc"
                        )
                    }
                    .buttonStyle(.bordered)
                    .accessibilityLabel("Copy Device ID to clipboard")
                }
            }
            .padding(12)
            .background(Color(.secondarySystemBackground).opacity(0.5))
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))

            Divider()

            // Part B: Desktop Device ID
            VStack(alignment: .leading, spacing: 8) {
                Text("B — Enter your desktop's Device ID")
                    .font(.subheadline.weight(.semibold))

                Text("In desktop Syncthing, go to Actions \u{2192} Show ID to find it.")
                    .font(.caption)
                    .foregroundStyle(.secondary)

                TextField(
                    "XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX",
                    text: $newDeviceID
                )
                .font(.system(.body, design: .monospaced))
                .textInputAutocapitalization(.characters)
                .autocorrectionDisabled()
                .textFieldStyle(.roundedBorder)
                .accessibilityLabel("Desktop Device ID")
                .accessibilityHint("Enter the 8-group Device ID from your desktop Syncthing")

                // Inline validation feedback
                if let message = deviceIDValidationMessage {
                    Label(message, systemImage: "exclamationmark.circle")
                        .font(.caption)
                        .foregroundStyle(.orange)
                        .accessibilityLabel("Validation: \(message)")
                } else if !newDeviceID.isEmpty && hasValidDeviceID {
                    Label("Valid Device ID format", systemImage: "checkmark.circle")
                        .font(.caption)
                        .foregroundStyle(.green)
                }

                TextField("Device name (optional)", text: $newDeviceName)
                    .textFieldStyle(.roundedBorder)
                    .accessibilityLabel("Device name")

                if dynamicTypeSize.isAccessibilitySize {
                    VStack(alignment: .leading, spacing: 8) {
                        Button {
                            showQRScanner = true
                        } label: {
                            Label("Scan QR Code", systemImage: "qrcode.viewfinder")
                        }
                        .buttonStyle(.bordered)

                        Button("Add Device") {
                            addDevice()
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(!hasValidDeviceID)
                    }
                } else {
                    HStack {
                        Button {
                            showQRScanner = true
                        } label: {
                            Label("Scan QR Code", systemImage: "qrcode.viewfinder")
                        }
                        .buttonStyle(.bordered)

                        Spacer()

                        Button("Add Device") {
                            addDevice()
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(!hasValidDeviceID)
                    }
                }

                if syncthingManager.devices.isEmpty {
                    Text("No desktop device added yet.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    Label(deviceSummaryText, systemImage: "checkmark.circle.fill")
                        .font(.caption)
                        .foregroundStyle(.green)
                }
            }
            .padding(12)
            .background(Color(.secondarySystemBackground).opacity(0.5))
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
        .padding(16)
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    // MARK: - Obsidian Card

    private var obsidianCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(
                vaultManager.needsReconnect ? "2. Reconnect Obsidian Folder" : "2. Connect Obsidian Folder",
                systemImage: vaultManager.needsReconnect ? "arrow.clockwise.circle.fill" : "folder.badge.gearshape"
            )
            .font(.headline)

            if vaultManager.isAccessible {
                Label("Obsidian folder connected", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
                if vaultManager.detectedVaults.isEmpty {
                    Text("No vaults detected yet. Open Obsidian and create or import a vault.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    Text("Detected vaults: \(vaultManager.detectedVaults.joined(separator: ", "))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            } else {
                if let issue = vaultManager.accessIssue {
                    Text(issue.message)
                        .font(.caption)
                        .foregroundStyle(.orange)
                    Text(issue.remediation)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                    if let url = troubleshootingURL(anchor: vaultManager.needsReconnect ? "bookmark-access-expired" : "obsidian-folder-not-found") {
                        Link("Learn how to fix", destination: url)
                            .font(.caption2)
                    }
                } else {
                    Text("VaultSync needs one-time access to your Obsidian directory so Syncthing can sync into it.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }

                // Always-visible prerequisite hint (replaces collapsed DisclosureGroup)
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: "info.circle.fill")
                        .foregroundStyle(.blue)
                        .accessibilityHidden(true)
                    Text("Obsidian must be installed and opened at least once so the folder exists. If you haven't yet, open Obsidian first.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .padding(10)
                .background(Color.blue.opacity(0.08), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .accessibilityElement(children: .combine)

                if dynamicTypeSize.isAccessibilitySize {
                    VStack(alignment: .leading, spacing: 8) {
                        Button {
                            openURL(URL(string: "obsidian://")!)
                        } label: {
                            Label("Open Obsidian", systemImage: "arrow.up.forward.app")
                        }
                        .buttonStyle(.bordered)
                        .accessibilityHint("Opens Obsidian so it creates its local folder")

                        Button {
                            showObsidianPicker = true
                        } label: {
                            Label(
                                vaultManager.needsReconnect ? "Reconnect Obsidian Folder" : "Connect Obsidian Folder",
                                systemImage: vaultManager.needsReconnect ? "arrow.clockwise" : "folder.badge.plus"
                            )
                            .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                    }
                } else {
                    HStack {
                        Button {
                            openURL(URL(string: "obsidian://")!)
                        } label: {
                            Label("Open Obsidian", systemImage: "arrow.up.forward.app")
                        }
                        .buttonStyle(.bordered)
                        .accessibilityHint("Opens Obsidian so it creates its local folder")

                        Spacer()

                        Button {
                            showObsidianPicker = true
                        } label: {
                            Label(
                                vaultManager.needsReconnect ? "Reconnect Obsidian Folder" : "Connect Obsidian Folder",
                                systemImage: vaultManager.needsReconnect ? "arrow.clockwise" : "folder.badge.plus"
                            )
                        }
                        .buttonStyle(.borderedProminent)
                    }
                }

                Text("In the picker, choose \"On My iPhone\" \u{2192} \"Obsidian\", then tap Open.")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(16)
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    // MARK: - First Share Card

    private var firstShareCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("3. Confirm your first share", systemImage: "tray.and.arrow.down")
                .font(.headline)

            if !syncthingManager.folders.isEmpty {
                Label(sharedFolderSummaryText, systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
            } else if !syncthingManager.pendingFolders.isEmpty {
                Label(pendingShareSummaryText, systemImage: "clock.badge.exclamationmark")
                    .foregroundStyle(.orange)
                Text("Open Pending Shares on the main screen to accept. VaultSync also auto-accepts when Obsidian is connected.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                Text("From desktop Syncthing, share one Obsidian vault folder to this iPhone.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)

                VStack(alignment: .leading, spacing: 6) {
                    Text("On your desktop:")
                        .font(.caption.bold())
                    desktopInstructionRow(number: "1", text: "Open Syncthing (usually localhost:8384)")
                    desktopInstructionRow(number: "2", text: "Click \"Add Folder\" or edit an existing one")
                    desktopInstructionRow(number: "3", text: "Set the folder path to your Obsidian vault")
                    desktopInstructionRow(number: "4", text: "Under Sharing, enable this iPhone")
                    desktopInstructionRow(number: "5", text: "Save — VaultSync will detect the share")
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(10)
                .background(Color(.secondarySystemBackground))
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))

                if let url = troubleshootingURL(anchor: "no-pending-shares-appear") {
                    Link("Having trouble?", destination: url)
                        .font(.caption2)
                }
            }
        }
        .padding(16)
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    // MARK: - Relay Card

    private var relayCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("4. Enable instant sync (optional)", systemImage: "bolt.horizontal.circle.fill")
                .font(.headline)

            Text("Cloud Relay wakes VaultSync instantly via push notifications instead of waiting for background refresh.")
                .font(.subheadline)
                .foregroundStyle(.secondary)

            if subscriptionManager.isRelaySubscribed {
                Label("Cloud Relay active", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
            } else if let product = subscriptionManager.availableProduct {
                Button {
                    Task {
                        do {
                            let deviceIDs = syncthingManager.devices.map(\.deviceID)
                            try await subscriptionManager.purchase(homeserverDeviceIDs: deviceIDs)
                        } catch {
                            let userError = SyncUserError.from(error: error, fallbackTitle: "Purchase Failed")
                            alertMessage = userError.userVisibleDescription
                            showAlert = true
                        }
                    }
                } label: {
                    Text("Enable Cloud Relay (\(product.displayPrice)/mo)")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .disabled(subscriptionManager.purchaseInProgress)
            } else {
                Text("Cloud Relay pricing is currently unavailable. You can enable it later in Settings.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            // Server-side requirement
            HStack(alignment: .top, spacing: 8) {
                Image(systemName: "server.rack")
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Cloud Relay also requires vaultsync-notify running on your server to detect file changes and trigger sync.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Link(destination: URL(string: "https://github.com/psimaker/vaultsync/blob/main/notify/README.md")!) {
                        Label("View setup guide", systemImage: "doc.text")
                            .font(.caption)
                    }
                }
            }
            .padding(10)
            .background(Color(.secondarySystemBackground))
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .accessibilityElement(children: .combine)
        }
        .padding(16)
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    // MARK: - Completion Card

    private var completionCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            if checklistViewModel.isReadyToFinish {
                Label("Setup complete. You are ready to sync.", systemImage: "checkmark.seal.fill")
                    .foregroundStyle(.green)
            } else {
                Label("Required setup steps are still incomplete.", systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                Text(checklistViewModel.continueWarningText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if checklistViewModel.isReadyToFinish {
                Button("Get Started") {
                    completeOnboarding()
                }
                .buttonStyle(.borderedProminent)
                .frame(maxWidth: .infinity)
            } else {
                Button("Continue Anyway") {
                    showContinueAnywayDialog = true
                }
                .buttonStyle(.borderedProminent)
                .tint(.orange)

                Button("Go to First Incomplete Step") {
                    focusedRequirement = checklistViewModel.incompleteRequiredItems.first?.requirement
                }
                .buttonStyle(.bordered)
            }
        }
        .padding(16)
        .background(Color(.secondarySystemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    // MARK: - Device ID Validation

    private var hasValidDeviceID: Bool {
        let trimmed = newDeviceID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.count == 63 else { return false }
        let base32 = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
        let parts = trimmed.split(separator: "-", omittingEmptySubsequences: false)
        return parts.count == 8 && parts.allSatisfy {
            $0.count == 7 && $0.unicodeScalars.allSatisfy { base32.contains($0) }
        }
    }

    /// Returns a specific validation message for the current Device ID input, or nil if empty/valid.
    private var deviceIDValidationMessage: String? {
        let trimmed = newDeviceID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        if hasValidDeviceID { return nil }

        let base32Chars = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567-")
        let upper = trimmed.uppercased()
        if !upper.unicodeScalars.allSatisfy({ base32Chars.contains($0) }) {
            return "Contains invalid characters. Device IDs use only A\u{2013}Z and 2\u{2013}7, separated by dashes."
        }

        let parts = trimmed.split(separator: "-", omittingEmptySubsequences: false)
        if parts.count != 8 {
            if trimmed.count < 63 {
                return "Too short. A full Device ID has 8 groups of 7 characters (e.g. XXXXXXX-XXXXXXX-\u{2026})."
            }
            return "Expected 8 groups separated by dashes (e.g. XXXXXXX-XXXXXXX-\u{2026})."
        }

        if let badGroup = parts.first(where: { $0.count != 7 }) {
            return "Group \"\(badGroup)\" should be exactly 7 characters."
        }

        return "Invalid Device ID format."
    }

    // MARK: - Helpers

    private var deviceSummaryText: String {
        let count = syncthingManager.devices.count
        return count == 1 ? "1 device configured" : "\(count) devices configured"
    }

    private var sharedFolderSummaryText: String {
        let count = syncthingManager.folders.count
        return count == 1 ? "1 shared folder active" : "\(count) shared folders active"
    }

    private var pendingShareSummaryText: String {
        let count = syncthingManager.pendingFolders.count
        return count == 1 ? "1 pending share found" : "\(count) pending shares found"
    }

    private func featureRow(icon: String, text: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .foregroundStyle(Color.accentColor)
                .frame(width: 20)
                .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
        }
        .accessibilityElement(children: .combine)
    }

    private func desktopInstructionRow(number: String, text: String) -> some View {
        HStack(alignment: .top, spacing: 6) {
            Text("\(number).")
                .monospacedDigit()
                .frame(width: 16, alignment: .trailing)
            Text(text)
        }
    }

    private func addDevice() {
        let id = newDeviceID.trimmingCharacters(in: .whitespacesAndNewlines)
        let name = newDeviceName.trimmingCharacters(in: .whitespacesAndNewlines)

        if let err = syncthingManager.addDevice(id: id, name: name) {
            alertMessage = mappedError(err, fallbackTitle: "Could Not Add Device").userVisibleDescription
            showAlert = true
        } else {
            newDeviceID = ""
            newDeviceName = ""
        }
    }

    private func completeOnboarding() {
        hasCompletedOnboarding = true
    }

    private func mappedError(_ error: String, fallbackTitle: String = "Setup Error") -> SyncUserError {
        SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle)
    }

    private func troubleshootingURL(for error: SyncUserError) -> URL? {
        let details = "\(error.message) \(error.remediation) \(error.technicalDetails ?? "")".lowercased()
        switch error.category {
        case .syncthingNotRunning:
            return troubleshootingURL(anchor: "syncthing-not-running")
        case .relayUnreachable, .relayProvision, .network:
            return troubleshootingURL(anchor: "relay-unreachable")
        case .auth:
            return troubleshootingURL(anchor: "wrong-syncthing-api-key-in-notify")
        case .permission, .fileAccess:
            if details.contains("apns") || details.contains("notification") || details.contains("push") {
                return troubleshootingURL(anchor: "apns-not-registered")
            }
            return troubleshootingURL(anchor: "bookmark-access-expired")
        case .config, .validation:
            if details.contains("pending") || details.contains("share") {
                return troubleshootingURL(anchor: "no-pending-shares-appear")
            }
            return troubleshootingURL(anchor: "obsidian-folder-not-found")
        case .unknown:
            return troubleshootingURL(anchor: "background-sync-not-working")
        }
    }

    private func troubleshootingURL(anchor: String) -> URL? {
        URL(string: "\(Self.troubleshootingBaseURL)#\(anchor)")
    }

    private static let troubleshootingBaseURL = "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md"
}

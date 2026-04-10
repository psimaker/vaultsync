import StoreKit
import SwiftUI
import UIKit

struct SettingsView: View {
    let syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager

    @State private var localDiscovery = true
    @State private var globalDiscovery = true
    @State private var didLoadConfig = false
    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var showSetupGuide = false
    @State private var retryProvisioningInProgress = false
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                cloudRelaySection

                Section("Discovery") {
                    Toggle("Local Discovery", isOn: $localDiscovery)
                        .onChange(of: localDiscovery) { _, _ in
                            guard didLoadConfig else { return }
                            applyDiscovery()
                        }
                    Toggle("Global Discovery", isOn: $globalDiscovery)
                        .onChange(of: globalDiscovery) { _, _ in
                            guard didLoadConfig else { return }
                            applyDiscovery()
                        }

                    Text("Local discovery finds devices on your WiFi network. Global discovery uses Syncthing's servers to find devices anywhere.")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }

                Section("This Device") {
                    if syncthingManager.deviceID.isEmpty {
                        LabeledContent("Device ID", value: "Not available")
                    } else {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Device ID")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Text(syncthingManager.deviceID)
                                .font(.system(.caption, design: .monospaced))
                                .textSelection(.enabled)
                        }
                    }
                }

                Section {
                    Button {
                        showSetupGuide = true
                    } label: {
                        Label("Setup Guide", systemImage: "checklist")
                    }
                } footer: {
                    Text("Review or complete the initial setup checklist.")
                }

            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        dismiss()
                    }
                }
            }
            .alert("Error", isPresented: $showAlert) {
                Button("OK") { }
            } message: {
                Text(alertMessage ?? "")
            }
            .sheet(isPresented: $showSetupGuide) {
                NavigationStack {
                    ScrollView {
                        SetupChecklistView(
                            viewModel: SetupChecklistViewModel(
                                syncthingManager: syncthingManager,
                                vaultManager: vaultManager,
                                subscriptionManager: subscriptionManager
                            )
                        ) { _ in }
                        .padding(16)
                    }
                    .navigationTitle("Setup Guide")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") {
                                showSetupGuide = false
                            }
                        }
                    }
                }
            }
            .onAppear {
                loadDiscoveryState()
                Task {
                    await subscriptionManager.refreshRelayDiagnostics(
                        homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
                    )
                }
            }
        }
    }

    private func loadDiscoveryState() {
        let json = SyncBridgeService.getConfigJSON()
        guard let data = json.data(using: .utf8),
              let cfg = try? JSONDecoder().decode(ConfigOptions.self, from: data) else {
            didLoadConfig = true
            return
        }
        localDiscovery = cfg.options.localAnnounceEnabled
        globalDiscovery = cfg.options.globalAnnounceEnabled
        didLoadConfig = true
    }

    private func applyDiscovery() {
        if let err = SyncBridgeService.setDiscoveryEnabled(local: localDiscovery, global: globalDiscovery) {
            alertMessage = mappedError(err, fallbackTitle: "Discovery Update Failed").userVisibleDescription
            showAlert = true
        }
    }

    // MARK: - Cloud Relay Section

    private var cloudRelaySection: some View {
        Section {
            // Status row
            HStack {
                Label("Status", systemImage: subscriptionManager.isRelaySubscribed ? "antenna.radiowaves.left.and.right" : "antenna.radiowaves.left.and.right.slash")
                Spacer()
                Text(relayStatusText)
                    .foregroundStyle(subscriptionManager.isRelaySubscribed ? .green : .secondary)
            }
            .accessibilityElement(children: .combine)

            // Expiry date
            if let expiry = subscriptionManager.subscriptionExpiryDate, subscriptionManager.isRelaySubscribed {
                LabeledContent("Renews") {
                    Text(expiry, style: .date)
                }
            }

            if !syncthingManager.devices.isEmpty {
                ForEach(syncthingManager.devices) { device in
                    let status = relayProvisionStatus(for: device.deviceID)
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(device.name.isEmpty ? device.deviceID : device.name)
                                    .font(.subheadline)
                                Text(device.deviceID)
                                    .font(.system(.caption2, design: .monospaced))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            Text(status.summary)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(relayProvisionColor(status))
                        }
                        .accessibilityElement(children: .combine)
                        if let reason = status.failureReason {
                            Text(reason)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                            if let url = troubleshootingURL(for: reason) {
                                Link("Learn how to fix", destination: url)
                                    .font(.caption2)
                            }
                        }
                    }
                    .padding(.vertical, 2)
                }
            }

            // Connection test
            HStack {
                Label("Relay Server", systemImage: "server.rack")
                Spacer()
                if subscriptionManager.relayHealthCheckInFlight {
                    ProgressView()
                        .controlSize(.small)
                } else if let result = subscriptionManager.relayHealthResult {
                    Text(result.summary)
                        .foregroundStyle(result.isHealthy ? .green : .red)
                } else {
                    Button("Test") {
                        Task {
                            await subscriptionManager.runRelayHealthCheck()
                        }
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                }
            }
            .accessibilityElement(children: .combine)

            if let healthMessage = subscriptionManager.relayHealthResult?.message,
               !(subscriptionManager.relayHealthResult?.isHealthy ?? true) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(healthMessage)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                    if let url = troubleshootingURL(anchor: "relay-unreachable") {
                        Link("Learn how to fix", destination: url)
                            .font(.caption2)
                    }
                }
            }

            // Subscribe / Manage
            if subscriptionManager.isRelaySubscribed {
                Button("Manage Subscription") {
                    Task {
                        guard let windowScene = UIApplication.shared.connectedScenes.first as? UIWindowScene else { return }
                        try? await AppStore.showManageSubscriptions(in: windowScene)
                    }
                }

                Button {
                    Task {
                        retryProvisioningInProgress = true
                        await subscriptionManager.retryRelayProvisioning(
                            homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
                        )
                        retryProvisioningInProgress = false
                    }
                } label: {
                    HStack {
                        Text("Retry Provisioning")
                        Spacer()
                        if retryProvisioningInProgress {
                            ProgressView()
                                .controlSize(.small)
                        }
                    }
                }
                .disabled(retryProvisioningInProgress)
            } else {
                if let product = subscriptionManager.availableProduct {
                    Button {
                        Task {
                        do {
                            let deviceIDs = syncthingManager.devices.map(\.deviceID)
                            try await subscriptionManager.purchase(homeserverDeviceIDs: deviceIDs)
                        } catch {
                            alertMessage = SyncUserError.from(
                                error: error,
                                fallbackTitle: "Purchase Failed"
                            ).userVisibleDescription
                            showAlert = true
                        }
                    }
                    } label: {
                        HStack {
                            Text("Subscribe")
                            Spacer()
                            Text(product.displayPrice + "/mo")
                                .foregroundStyle(.secondary)
                        }
                    }
                    .disabled(subscriptionManager.purchaseInProgress)
                } else {
                    Text("Subscription unavailable")
                        .foregroundStyle(.secondary)
                }

                Button("Restore Purchases") {
                    Task {
                        await subscriptionManager.restorePurchases()
                    }
                }
            }

            NavigationLink {
                RelayDiagnosticsView(
                    syncthingManager: syncthingManager,
                    subscriptionManager: subscriptionManager
                )
            } label: {
                Label("Open Relay Diagnostics", systemImage: "stethoscope")
            }

            if let relayError = subscriptionManager.errorMessage, !relayError.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    Text(relayError)
                        .font(.caption)
                        .foregroundStyle(.red)
                    if let url = troubleshootingURL(for: relayError) {
                        Link("Learn how to fix", destination: url)
                            .font(.caption2)
                    }
                }
            }

            // Subscription details (required by App Store Review)
            VStack(alignment: .leading, spacing: 2) {
                Text("Cloud Relay — $0.99/month")
                    .font(.caption)
                Text("Auto-renews monthly. Cancel anytime in Settings → Subscriptions.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.vertical, 2)

            // Legal links (required by App Store Review)
            Link(destination: URL(string: "https://github.com/psimaker/vaultsync/blob/main/PRIVACY.md")!) {
                HStack {
                    Label("Privacy Policy", systemImage: "hand.raised")
                    Spacer()
                    Image(systemName: "arrow.up.right.square")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                }
            }

            Link(destination: URL(string: "https://github.com/psimaker/vaultsync/blob/main/TERMS.md")!) {
                HStack {
                    Label("Terms of Use", systemImage: "doc.text")
                    Spacer()
                    Image(systemName: "arrow.up.right.square")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                }
            }
        } header: {
            Text("Cloud Relay")
        } footer: {
            Text("Cloud Relay enables instant sync when files change on your server, instead of waiting for the next background refresh.")
        }
    }

    private var relayStatusText: String {
        if subscriptionManager.isRelaySubscribed { return "Active" }
        if subscriptionManager.isLoadingProduct { return "Loading…" }
        if subscriptionManager.availableProduct != nil { return "Not Subscribed" }
        return "Not Configured"
    }

    private func relayProvisionStatus(for deviceID: String) -> RelayProvisionStatus {
        subscriptionManager.relayProvisionStatuses[deviceID] ?? .notAttempted
    }

    private func relayProvisionColor(_ status: RelayProvisionStatus) -> Color {
        switch status {
        case .provisioned:
            return .green
        case .failed:
            return .red
        case .inProgress:
            return .blue
        case .notAttempted:
            return .secondary
        }
    }

    private func mappedError(_ error: String, fallbackTitle: String = "Settings Error") -> SyncUserError {
        SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle)
    }

    private func troubleshootingURL(for rawError: String) -> URL? {
        let mapped = mappedError(rawError, fallbackTitle: "Settings Error")
        return troubleshootingURL(for: mapped)
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
            return troubleshootingURL(anchor: "obsidian-folder-not-found")
        case .unknown:
            return troubleshootingURL(anchor: "background-sync-not-working")
        }
    }

    private func troubleshootingURL(anchor: String) -> URL? {
        URL(string: "\(Self.troubleshootingBaseURL)#\(anchor)")
    }

    private static let troubleshootingBaseURL = "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md"

    // MARK: - Config

    private struct ConfigOptions: Codable {
        let options: Options
        struct Options: Codable {
            let localAnnounceEnabled: Bool
            let globalAnnounceEnabled: Bool
        }
    }
}

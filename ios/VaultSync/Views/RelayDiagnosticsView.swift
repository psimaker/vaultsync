import SwiftUI
import UIKit

struct RelayDiagnosticsView: View {
    let syncthingManager: SyncthingManager
    var subscriptionManager: SubscriptionManager

    @State private var diagnosticsInFlight = false
    @State private var retryProvisioningInFlight = false

    var body: some View {
        List {
            relayHealthSection
            apnsSection
            provisioningSection
            triggerSection
            actionSection
            troubleshootingSection
        }
        .navigationTitle("Relay Diagnostics")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if subscriptionManager.relayHealthResult == nil {
                await runDiagnostics()
            }
        }
    }

    private var relayHealthSection: some View {
        Section("Relay Backend") {
            HStack {
                Label("Health Endpoint", systemImage: "server.rack")
                Spacer()
                if subscriptionManager.relayHealthCheckInFlight || diagnosticsInFlight {
                    ProgressView()
                        .controlSize(.small)
                } else if let result = subscriptionManager.relayHealthResult {
                    Text(result.summary)
                        .foregroundStyle(result.isHealthy ? .green : .red)
                } else {
                    Text("Not checked")
                        .foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)

            if let checkedAt = subscriptionManager.relayHealthResult?.checkedAt {
                LabeledContent("Last Check") {
                    Text(checkedAt, style: .relative)
                }
            }

            if let latencyMs = subscriptionManager.relayHealthResult?.latencyMs {
                LabeledContent("Latency") {
                    Text("\(latencyMs) ms")
                }
            }

            if let message = subscriptionManager.relayHealthResult?.message,
               !(subscriptionManager.relayHealthResult?.isHealthy ?? true) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(message)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    if let url = SyncUserError.troubleshootingURL(anchor: "relay-unreachable") {
                        Link("Learn how to fix", destination: url)
                            .font(.caption2)
                    }
                }
            }

            if let relayError = subscriptionManager.lastRelayError {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Last Relay Error")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.red)
                    Text(relayError.message)
                        .font(.caption)
                    Text("Context: \(relayError.context) · \(relayError.date.formatted(date: .abbreviated, time: .shortened))")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                    if let url = troubleshootingURL(for: relayError.message) {
                        Link("Learn how to fix", destination: url)
                            .font(.caption2)
                    }
                }
                .padding(.vertical, 2)
            } else {
                Text("No relay errors recorded.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var apnsSection: some View {
        Section("Push Registration") {
            HStack {
                Label("APNs Registration", systemImage: "bell.badge")
                Spacer()
                Text(subscriptionManager.apnsRegistrationStatus.summary)
                    .foregroundStyle(apnsStatusColor)
            }
            .accessibilityElement(children: .combine)

            HStack {
                Label("APNs Token", systemImage: "key.fill")
                Spacer()
                Text(subscriptionManager.hasAPNsToken ? "Present" : "Missing")
                    .foregroundStyle(subscriptionManager.hasAPNsToken ? .green : .orange)
            }
            .accessibilityElement(children: .combine)

            if let updatedAt = subscriptionManager.apnsRegistrationSnapshot.updatedAt {
                LabeledContent("Last Update") {
                    Text(updatedAt, style: .relative)
                }
            }

            if let successAt = subscriptionManager.apnsRegistrationSnapshot.lastSuccessAt {
                LabeledContent("Last Success") {
                    Text(successAt, style: .relative)
                }
            }

            if let failureAt = subscriptionManager.apnsRegistrationSnapshot.lastFailureAt {
                LabeledContent("Last Failure") {
                    Text(failureAt, style: .relative)
                }
            }

            if case .failed(let reason) = subscriptionManager.apnsRegistrationStatus {
                VStack(alignment: .leading, spacing: 2) {
                    Text(reason)
                        .font(.caption)
                        .foregroundStyle(.red)
                    if let url = SyncUserError.troubleshootingURL(anchor: "apns-not-registered") {
                        Link("Learn how to fix", destination: url)
                            .font(.caption2)
                    }
                }
            }

            Button("Retry APNs Registration") {
                APNsRegistrationStore.markNotAttempted()
                UIApplication.shared.registerForRemoteNotifications()
            }

            Button("Open iOS Notification Settings") {
                openSystemSettings()
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
        }
    }

    private var provisioningSection: some View {
        Section("Per-Device Provisioning") {
            if syncthingManager.devices.isEmpty {
                Text("No Syncthing peers available yet.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(syncthingManager.devices) { device in
                    let status = subscriptionManager.relayProvisionStatuses[device.deviceID] ?? .notAttempted
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(device.name.isEmpty ? device.deviceID : device.name)
                                    .font(.subheadline)
                                Text(device.deviceID)
                                    .font(.system(.caption2, design: .monospaced))
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
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
        }
    }

    private var triggerSection: some View {
        Section("Trigger Delivery") {
            HStack {
                Label("Last Trigger Received", systemImage: "bolt.badge.clock")
                Spacer()
                if let lastTrigger = subscriptionManager.lastRelayTriggerReceivedAt {
                    Text(lastTrigger, style: .relative)
                        .foregroundStyle(.primary)
                } else {
                    Text("Never")
                        .foregroundStyle(.secondary)
                }
            }
            .accessibilityElement(children: .combine)

            Text("This timestamp is updated when VaultSync receives a silent push from Cloud Relay.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var actionSection: some View {
        Section("Actions") {
            Button {
                Task { await runDiagnostics() }
            } label: {
                HStack {
                    Text("Run Full Diagnostics")
                    Spacer()
                    if diagnosticsInFlight {
                        ProgressView()
                            .controlSize(.small)
                    }
                }
            }
            .disabled(diagnosticsInFlight)

            if subscriptionManager.isRelaySubscribed {
                Button {
                    Task {
                        retryProvisioningInFlight = true
                        await subscriptionManager.retryRelayProvisioning(
                            homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
                        )
                        retryProvisioningInFlight = false
                        await runDiagnostics()
                    }
                } label: {
                    HStack {
                        Text("Retry Provisioning")
                        Spacer()
                        if retryProvisioningInFlight {
                            ProgressView()
                                .controlSize(.small)
                        }
                    }
                }
                .disabled(retryProvisioningInFlight)
            }
        }
    }

    private var troubleshootingSection: some View {
        let hints = troubleshootingHints
        return Section("Troubleshooting") {
            if hints.isEmpty {
                Text("No immediate relay problems detected.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(hints, id: \.self) { hint in
                    Text(hint)
                        .font(.caption)
                }
                if let url = SyncUserError.troubleshootingURL(anchor: "relay-unreachable") {
                    Link("Open full relay troubleshooting", destination: url)
                        .font(.caption2)
                }
            }
        }
    }

    private var troubleshootingHints: [String] {
        var hints: [String] = []

        if !subscriptionManager.isRelaySubscribed {
            hints.append("Cloud Relay is not currently subscribed. Push-triggered wake-ups are disabled until the subscription is active.")
        }

        if !subscriptionManager.hasAPNsToken {
            hints.append("APNs token is missing. Enable notifications for VaultSync and retry APNs registration.")
        }

        if case .failed = subscriptionManager.apnsRegistrationStatus {
            hints.append("APNs registration failed. Open iOS Settings > Notifications > VaultSync, allow notifications, then retry.")
        }

        if let health = subscriptionManager.relayHealthResult, !health.isHealthy {
            hints.append("Relay health endpoint is not healthy. Check internet access, VPN/firewall rules, or relay availability.")
        }

        let failedDevices = subscriptionManager.relayProvisionStatuses.values.filter {
            if case .failed = $0 { return true }
            return false
        }.count
        if failedDevices > 0 {
            hints.append("Some devices are not provisioned. Retry provisioning after APNs and subscription checks are green.")
        }

        if subscriptionManager.isRelaySubscribed, subscriptionManager.lastRelayTriggerReceivedAt == nil {
            hints.append("No relay trigger has been received yet. Verify your homeserver `vaultsync-notify` container is running and can reach relay.vaultsync.eu.")
        }

        return hints
    }

    private var apnsStatusColor: Color {
        switch subscriptionManager.apnsRegistrationStatus {
        case .registered:
            return .green
        case .failed:
            return .red
        case .notAttempted:
            return .secondary
        }
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

    private func openSystemSettings() {
        guard let url = URL(string: UIApplication.openSettingsURLString) else { return }
        UIApplication.shared.open(url)
    }

    private func troubleshootingURL(for rawError: String) -> URL? {
        SyncUserError.troubleshootingURL(forRawError: rawError)
    }

    private func runDiagnostics() async {
        diagnosticsInFlight = true
        await subscriptionManager.refreshRelayDiagnostics(
            homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
        )
        diagnosticsInFlight = false
    }
}

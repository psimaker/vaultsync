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
            #if DEBUG
            if RelayService.isUsingRelayOverride {
                Label(L10n.fmt("DEBUG: pointed at mock relay %@", RelayService.relayURL), systemImage: "exclamationmark.triangle.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Color.statusAttention)
            } else {
                // Inverse of the mock banner: make it loud when a DEBUG/lab build
                // is silently on PRODUCTION, so the operator notices that
                // provision / health calls are hitting the real relay (golden
                // rule #3 — point lab builds at the mock).
                Label(L10n.tr("DEBUG: pointed at PRODUCTION relay. Relaunch with -RELAY_BASE_URL_OVERRIDE to use the local mock."), systemImage: "exclamationmark.octagon.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Color.statusError)
            }
            #endif
            if subscriptionManager.relayDeliveryConfirmed {
                Label(L10n.tr("Cloud Relay is delivering wake-ups"), systemImage: "checkmark.seal.fill")
                    .foregroundStyle(Color.statusSuccess)
                    .font(.subheadline)
            } else if subscriptionManager.relayDeliveryLikelyWorking {
                // "Reachable" is NOT "delivering" — keep it neutral and qualified so
                // it never reads as success. Distinguish never-delivered (finish
                // setup) from previously-delivered-now-stale ("went quiet") so this
                // surface agrees with the dashboard / RelayHomeView (review
                // finding: cross-surface contradiction).
                if subscriptionManager.lastRelayTriggerReceivedAt != nil {
                    Label(L10n.tr("Relay reachable — but no wake-up has arrived recently. Check the helper is still running on your server."), systemImage: "dot.radiowaves.left.and.right")
                        .foregroundStyle(Color.statusAttention)
                        .font(.subheadline)
                } else {
                    Label(L10n.tr("Relay reachable — but no wake-up delivered yet. Run the helper on your server."), systemImage: "dot.radiowaves.left.and.right")
                        .foregroundStyle(.secondary)
                        .font(.subheadline)
                }
            }
            HStack {
                Label("Health Endpoint", systemImage: "server.rack")
                Spacer()
                if subscriptionManager.relayHealthCheckInFlight || diagnosticsInFlight {
                    ProgressView()
                        .controlSize(.small)
                } else if let result = subscriptionManager.relayHealthResult {
                    Text(result.summary)
                        .foregroundStyle(result.isHealthy ? Color.statusSuccess : Color.statusError)
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
                        ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                            .font(.caption2)
                    }
                }
            }

            if let relayError = subscriptionManager.lastRelayError {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Last Relay Error")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Color.statusError)
                    Text(relayError.message)
                        .font(.caption)
                    Text(
                        L10n.fmt(
                            "Context: %@ · %@",
                            relayError.context,
                            relayError.date.formatted(date: .abbreviated, time: .shortened)
                        )
                    )
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                    if let url = troubleshootingURL(for: relayError.message) {
                        ExternalLinkButton(titleKey: "Learn how to fix", url: url)
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
                Text(subscriptionManager.hasAPNsToken ? L10n.tr("Present") : L10n.tr("Missing"))
                    .foregroundStyle(subscriptionManager.hasAPNsToken ? Color.statusSuccess : Color.statusAttention)
            }
            .accessibilityElement(children: .combine)

            HStack {
                Label(L10n.tr("Alert Banners"), systemImage: "app.badge")
                Spacer()
                Text(alertBannerText)
                    .foregroundStyle(alertBannerColor)
            }
            .accessibilityElement(children: .combine)

            if subscriptionManager.alertBannerStatus == .denied {
                Text(L10n.tr("Alert banners are off at the iOS level. Cloud Relay wake-ups still work — they use silent push, which does not need notification permission."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

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
                        .foregroundStyle(Color.statusError)
                    if let url = SyncUserError.troubleshootingURL(anchor: "apns-not-registered") {
                        ExternalLinkButton(titleKey: "Learn how to fix", url: url)
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
                                ExternalLinkButton(titleKey: "Learn how to fix", url: url)
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

            #if DEBUG
            // LAB: the Simulator can't deliver real silent pushes, so this drives
            // the REAL receive→UI path (markReceived → freshness → "active" /
            // first-delivery celebration) to demo the activation states. The mock
            // relay's `-deliver openurl` does the same via the
            // vaultsync://relay-wake deep link. Compiled out of release builds.
            Button {
                RelayTriggerStore.markReceived()
            } label: {
                Label(L10n.tr("DEBUG: Simulate real wake-up arrival"), systemImage: "ladybug.fill")
            }
            .font(.footnote)
            .foregroundStyle(Color.statusAttention)
            #endif
        }
    }

    private var actionSection: some View {
        Section("Actions") {
            Button {
                Task { await runDiagnostics() }
            } label: {
                HStack {
                    Text("Check Relay Status")
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
                    ExternalLinkButton(titleKey: "Open full relay troubleshooting", url: url)
                        .font(.caption2)
                }
            }
        }
    }

    private var troubleshootingHints: [String] {
        var hints: [String] = []

        if !subscriptionManager.isRelaySubscribed {
            hints.append(L10n.tr("Cloud Relay is not currently subscribed. Push-triggered wake-ups are disabled until the subscription is active."))
        }

        if !subscriptionManager.hasAPNsToken {
            hints.append(L10n.tr("APNs token is missing. Retry APNs registration; if it keeps failing, check your internet connection. (Silent push does not require notification banners.)"))
        }

        if case .failed = subscriptionManager.apnsRegistrationStatus {
            hints.append(L10n.tr("APNs registration failed. Check your internet connection and retry registration. Silent push does not require notification banners to be enabled."))
        }

        if let health = subscriptionManager.relayHealthResult, !health.isHealthy {
            hints.append(L10n.tr("Relay health endpoint is not healthy. Check internet access, VPN/firewall rules, or relay availability."))
        }

        let failedDevices = subscriptionManager.relayProvisionStatuses.values.filter {
            if case .failed = $0 { return true }
            return false
        }.count
        if failedDevices > 0 {
            hints.append(L10n.tr("Some devices are not provisioned. Retry provisioning after APNs and subscription checks are green."))
        }

        if subscriptionManager.isRelaySubscribed, subscriptionManager.lastRelayTriggerReceivedAt == nil {
            hints.append(L10n.tr("No relay trigger has been received yet. Verify your homeserver `vaultsync-notify` container is running and can reach relay.vaultsync.eu."))
        }

        return hints
    }

    private var alertBannerText: String {
        switch subscriptionManager.alertBannerStatus {
        case .allowed: return L10n.tr("Allowed")
        case .denied: return L10n.tr("Denied")
        case .unknown: return L10n.tr("Unknown")
        }
    }

    private var alertBannerColor: Color {
        switch subscriptionManager.alertBannerStatus {
        case .allowed: return .statusSuccess
        case .denied: return .secondary
        // "Not determined" is not an error — keep it neutral rather than a
        // warning yellow that implies something is wrong.
        case .unknown: return .secondary
        }
    }

    private var apnsStatusColor: Color {
        switch subscriptionManager.apnsRegistrationStatus {
        case .registered:
            return .statusSuccess
        case .failed:
            return .statusError
        case .notAttempted:
            return .secondary
        }
    }

    private func relayProvisionColor(_ status: RelayProvisionStatus) -> Color {
        switch status {
        case .provisioned:
            return .statusSuccess
        case .failed:
            return .statusError
        case .inProgress:
            return .statusInfo
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

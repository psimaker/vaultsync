import SwiftUI
import UIKit

struct RelayDiagnosticsView: View {
    let syncthingManager: SyncthingManager
    var subscriptionManager: SubscriptionManager

    @State private var diagnosticsInFlight = false
    @State private var retryProvisioningInFlight = false
    @State private var lowPowerModeEnabled = ProcessInfo.processInfo.isLowPowerModeEnabled
    @State private var syncPathCheck = SyncPathCheckController()
    @Environment(\.scenePhase) private var scenePhase
    var body: some View {
        List {
            relayHealthSection
            apnsSection
            provisioningSection
            observationSection
            syncPathCheckSection
            triggerSection
            actionSection
            troubleshootingSection
        }
        .navigationTitle("Relay Diagnostics")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: subscriptionManager.relayStatusPollViewState) {
            if subscriptionManager.relayHealthResult == nil {
                await runDiagnostics(checkObservationStatus: false)
            }
            if subscriptionManager.isRelaySubscribed && !subscriptionManager.relayDeliveryConfirmed {
                await subscriptionManager.pollRelayObservationStatus(
                    homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID),
                    context: .diagnostics
                )
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: .NSProcessInfoPowerStateDidChange)) { _ in
            lowPowerModeEnabled = ProcessInfo.processInfo.isLowPowerModeEnabled
        }
        .onChange(of: scenePhase) { _, phase in
            if phase != .active {
                syncPathCheck.cancel(reason: .appLifecycle)
            }
        }
        .onDisappear {
            syncPathCheck.cancel(reason: .viewLeft)
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
                VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
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
                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
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
                .padding(.vertical, VaultSpacing.xxs)
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
                VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
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
            LabeledContent(L10n.tr("Purchase locally verified")) {
                Text(subscriptionManager.relayEntitlementLocallyVerified ? L10n.tr("Confirmed") : L10n.tr("Not confirmed"))
                    .foregroundStyle(subscriptionManager.relayEntitlementLocallyVerified ? Color.statusSuccess : .secondary)
            }

            if syncthingManager.devices.isEmpty {
                Text("No Syncthing peers available yet.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(syncthingManager.devices) { device in
                    let status = subscriptionManager.relayProvisionStatuses[device.deviceID] ?? .notAttempted
                    VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                        HStack {
                            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
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
                    .padding(.vertical, VaultSpacing.xxs)
                }
            }
        }
    }

    private var triggerSection: some View {
        Section("Wake-up Delivery") {
            HStack {
                Label("Last Wake-up Received", systemImage: "bolt.badge.clock")
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

            HStack {
                Label(L10n.tr("Wake-ups (Last 7 Days)"), systemImage: "bolt.circle")
                Spacer()
                Text("\(subscriptionManager.relayWakeupsLast7Days)")
                    .foregroundStyle(subscriptionManager.relayWakeupsLast7Days > 0 ? .primary : .secondary)
            }
            .accessibilityElement(children: .combine)

            Text("This timestamp is updated when VaultSync receives a silent push from Cloud Relay.")
                .font(.caption)
                .foregroundStyle(.secondary)

            LabeledContent(L10n.tr("Last background sync start")) {
                if let startedAt = subscriptionManager.relayBackgroundSyncStartedAt {
                    Text(startedAt, style: .relative)
                } else {
                    Text(L10n.tr("Never"))
                        .foregroundStyle(.secondary)
                }
            }

            LabeledContent(L10n.tr("Last observed local data progress")) {
                if let progressAt = subscriptionManager.relayLocalDataProgressObservedAt {
                    Text(progressAt, style: .relative)
                } else {
                    Text(L10n.tr("Never"))
                        .foregroundStyle(.secondary)
                }
            }

            if lowPowerModeEnabled {
                Label(L10n.tr("Low Power Mode is on — iOS defers silent wake-ups until it is off or the iPhone is charging."), systemImage: "battery.25percent")
                    .font(.caption)
                    .foregroundStyle(Color.statusAttention)
            }

            Text(L10n.tr("Keep VaultSync in the background: if you force-quit it from the app switcher, iOS stops delivering wake-ups until you open the app again."))
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

    private var observationSection: some View {
        Section(L10n.tr("Server Signal Observation")) {
            if syncthingManager.devices.isEmpty {
                Text(L10n.tr("No server devices available yet."))
                    .foregroundStyle(.secondary)
            } else {
                ForEach(syncthingManager.devices) { device in
                    VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                        Text(device.name.isEmpty ? device.deviceID : device.name)
                            .font(.subheadline.weight(.semibold))
                        if let failure = subscriptionManager.relayStatusFailures[device.deviceID] {
                            Text(statusFailureText(failure))
                                .font(.caption)
                                .foregroundStyle(Color.statusAttention)
                        } else if let observation = subscriptionManager.relayServerObservations[device.deviceID] {
                            if let observedAt = observation.lastTriggerObservedAt {
                                LabeledContent(L10n.tr("Last signal observed by Relay")) {
                                    Text(observedAt, style: .relative)
                                }
                                .font(.caption)
                            } else {
                                Text(L10n.tr("Relay has not observed a signal for this server."))
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            LabeledContent(L10n.tr("Status checked")) {
                                Text(observation.checkedAt, style: .relative)
                            }
                            .font(.caption)
                        } else {
                            Text(L10n.tr("Status not checked yet."))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, VaultSpacing.xxs)
                }
            }

            Text(L10n.tr("Relay observation means an unauthenticated server signal was accepted for this server identity. It does not confirm who sent it, delivery to this iPhone, or a completed sync."))
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(L10n.tr("A wake-up recorded on this iPhone is stronger delivery evidence. Local data progress is separate; upload, controlled download, and a full roundtrip are not confirmed."))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var syncPathCheckSection: some View {
        Section(L10n.tr("Synchronization Path Check")) {
            Text(L10n.tr("This optional check starts only when you tap the button. It creates no test file and does not change your folders."))
                .font(.caption)
                .foregroundStyle(.secondary)

            if let session = syncPathCheck.session {
                if session.results.isEmpty {
                    Text(L10n.tr("No configured server folders are available for this check."))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(sortedSyncPathResults(session), id: \.targetID) { result in
                        TimelineView(.periodic(from: .now, by: 60)) { context in
                            syncPathTargetRow(result, now: context.date)
                        }
                    }
                }

                if syncPathCheck.isRunning {
                    LabeledContent(L10n.tr("Check progress")) {
                        Text(L10n.fmt("Attempt %d of %d", session.attempt, session.maximumAttempts))
                    }
                    .font(.caption)
                }
            }

            if syncPathCheck.isRunning {
                Button(role: .cancel) {
                    syncPathCheck.cancel(reason: .user)
                } label: {
                    Label(L10n.tr("Cancel synchronization check"), systemImage: "xmark.circle")
                }
                .disabled(syncPathCheck.isCancellationPending)
            } else {
                Button {
                    syncPathCheck.start(
                        devices: syncthingManager.devices,
                        folders: syncthingManager.folders
                    )
                } label: {
                    Label(L10n.tr("Check synchronization path"), systemImage: "arrow.triangle.2.circlepath")
                }
            }

            Text(L10n.tr("An incoming file change applied on this iPhone can confirm local data progress during this check. Upload, controlled download, and a full roundtrip cannot be confirmed with the current server helper yet."))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private func syncPathTargetRow(_ result: SyncPathTargetResult, now: Date) -> some View {
        let presented = SyncPathCheckPresentation.state(for: result, now: now)
        VStack(alignment: .leading, spacing: VaultSpacing.xs) {
            Text(syncPathTargetTitle(result.targetID))
                .font(.subheadline.weight(.semibold))
            Label(presented.userFacingTitle, systemImage: syncPathStatusSymbol(presented))
                .font(.caption.weight(.semibold))
                .foregroundStyle(syncPathStatusColor(presented))
            Text(presented.userFacingDetail)
                .font(.caption)
                .foregroundStyle(.secondary)

            ForEach(SyncPathDiagnosticStage.allCases, id: \.self) { stage in
                LabeledContent(stage.title) {
                    proofValue(stage.timestamp(in: result.proof))
                }
            }
        }
        .font(.caption)
        .padding(.vertical, VaultSpacing.xxs)
        .accessibilityElement(children: .contain)
    }

    @ViewBuilder
    private func proofValue(_ date: Date?) -> some View {
        if let date {
            Text(date, style: .relative)
                .foregroundStyle(Color.statusSuccess)
        } else {
            Text(L10n.tr("Not confirmed"))
                .foregroundStyle(.secondary)
        }
    }

    private func sortedSyncPathResults(_ session: SyncPathCheckSession) -> [SyncPathTargetResult] {
        session.results.values.sorted {
            if $0.targetID.folderID == $1.targetID.folderID {
                return ($0.targetID.deviceID ?? "") < ($1.targetID.deviceID ?? "")
            }
            return $0.targetID.folderID < $1.targetID.folderID
        }
    }

    private func syncPathTargetTitle(_ targetID: SyncPathTargetID) -> String {
        let folderName = syncthingManager.folders.first(where: { $0.id == targetID.folderID }).map {
            $0.label.isEmpty ? $0.id : $0.label
        } ?? L10n.tr("Unknown Folder")
        guard let deviceID = targetID.deviceID else { return folderName }
        let deviceName = syncthingManager.devices.first(where: { $0.deviceID == deviceID }).map {
            $0.name.isEmpty ? L10n.tr("Unknown Device") : $0.name
        } ?? L10n.tr("Unknown Device")
        return L10n.fmt("%@ — %@", folderName, deviceName)
    }

    private func syncPathStatusColor(_ state: SyncPathPresentedState) -> Color {
        switch state {
        case .localDataProgressObserved: return .statusInfo
        case .checking: return .statusInfo
        case .stale, .incomplete, .interrupted, .unavailable, .conflicting: return .statusAttention
        case .cancelled, .unsupported: return .secondary
        }
    }

    private func syncPathStatusSymbol(_ state: SyncPathPresentedState) -> String {
        switch state {
        case .localDataProgressObserved: return "waveform.path.ecg"
        case .checking: return "clock.arrow.circlepath"
        case .stale: return "clock.badge.exclamationmark"
        case .cancelled: return "xmark.circle"
        case .unsupported: return "nosign"
        case .incomplete, .interrupted, .unavailable, .conflicting: return "exclamationmark.triangle"
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
            if case .temporarilyFailed = $0 { return true }
            return false
        }.count
        if failedDevices > 0 {
            hints.append(L10n.tr("Some devices are not provisioned. Retry provisioning after APNs and subscription checks are green."))
        }

        if subscriptionManager.isRelaySubscribed, subscriptionManager.lastRelayTriggerReceivedAt == nil {
            hints.append(L10n.tr("No wake-up has been received yet. Check that the vaultsync-notify helper is running on your server and can reach relay.vaultsync.eu — running vaultsync-notify --doctor on the server shows exactly which step fails."))
        }

        if lowPowerModeEnabled {
            hints.append(L10n.tr("Low Power Mode is on — iOS defers silent wake-ups until it is off or the iPhone is charging."))
        }

        // Delivered before, but nothing within the freshness window: the most
        // common cause by far is a force-quit (iOS then drops silent pushes on
        // the floor until the next manual launch), not a broken helper.
        if subscriptionManager.isRelaySubscribed,
           subscriptionManager.lastRelayTriggerReceivedAt != nil,
           !subscriptionManager.relayDeliveryConfirmed {
            hints.append(L10n.tr("Wake-ups went quiet. If there were no vault changes recently, this is normal — the helper only sends when something changes. Otherwise the most common cause is force-quitting VaultSync from the app switcher — iOS then blocks wake-ups until the next manual launch. Also check that the helper on your server is still running."))
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
        case .provisionedVerified:
            return .statusSuccess
        case .temporarilyFailed:
            return .statusError
        case .inProgress:
            return .statusInfo
        case .migrationRequired, .storeKitVerificationRequired:
            return .statusAttention
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

    private func statusFailureText(_ failure: RelayStatusCheckFailure) -> String {
        switch failure {
        case .verificationRequired:
            return L10n.tr("Purchase confirmation is required before status can be checked.")
        case .rateLimited:
            return L10n.tr("Status checks are temporarily limited. Try again later.")
        case .temporarilyUnavailable:
            return L10n.tr("Status could not be checked right now. Try again later.")
        }
    }

    private func runDiagnostics(checkObservationStatus: Bool = true) async {
        diagnosticsInFlight = true
        await subscriptionManager.refreshRelayDiagnostics(
            homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
        )
        if checkObservationStatus {
            await subscriptionManager.checkRelayObservationStatus(
                homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
            )
        }
        diagnosticsInFlight = false
    }
}

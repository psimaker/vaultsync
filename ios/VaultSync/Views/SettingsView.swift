import StoreKit
import SwiftUI
import UIKit

struct SettingsView: View {
    let syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager

    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var showSetupStatus = false
    @State private var tipJar = TipJarManager()
    @State private var showThankYou = false
    @State private var deviceIDCopied = false
    @State private var isRestoring = false
    @State private var showServerSetup = false
    @AppStorage(BackgroundSyncService.conflictNotificationsEnabledKey) private var conflictNotificationsEnabled = true
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                cloudRelaySection
                supportSection
                notificationsSection
                aboutSection

                Section("This Device") {
                    if syncthingManager.deviceID.isEmpty {
                        LabeledContent("Device ID", value: L10n.tr("Not available"))
                    } else {
                        Button {
                            UIPasteboard.general.string = syncthingManager.deviceID
                            UINotificationFeedbackGenerator().notificationOccurred(.success)
                            deviceIDCopied = true
                            Task {
                                try? await Task.sleep(for: .seconds(1.5))
                                deviceIDCopied = false
                            }
                        } label: {
                            Label(
                                deviceIDCopied ? L10n.tr("Copied") : L10n.tr("Copy Device ID"),
                                systemImage: deviceIDCopied ? "checkmark.circle" : "doc.on.doc"
                            )
                        }
                    }

                    NavigationLink {
                        SyncActivityView(events: Array(syncthingManager.syncActivity.prefix(50)))
                    } label: {
                        Label("Log", systemImage: "text.append")
                    }
                }

                Section {
                    Button {
                        showSetupStatus = true
                    } label: {
                        Label(L10n.tr("Setup Status"), systemImage: "checklist")
                    }
                } footer: {
                    Text(L10n.tr("Check setup progress and troubleshooting tips."))
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
            .sheet(isPresented: $showSetupStatus) {
                NavigationStack {
                    ScrollView {
                        SetupChecklistView(
                            viewModel: SetupChecklistViewModel(
                                syncthingManager: syncthingManager,
                                vaultManager: vaultManager,
                                subscriptionManager: subscriptionManager
                            )
                        )
                        .padding(16)
                    }
                    .navigationTitle(L10n.tr("Setup Status"))
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") {
                                showSetupStatus = false
                            }
                        }
                    }
                }
            }
            .sheet(isPresented: $showServerSetup) {
                NavigationStack {
                    RelayServerSetupView(isDelivering: subscriptionManager.relayDeliveryConfirmed)
                        .toolbar {
                            ToolbarItem(placement: .confirmationAction) {
                                Button("Done") {
                                    showServerSetup = false
                                }
                            }
                        }
                }
            }
            .onAppear {
                Task {
                    await subscriptionManager.refreshRelayDiagnostics(
                        homeserverDeviceIDs: syncthingManager.devices.map(\.deviceID)
                    )
                }
            }
            .onChange(of: tipJar.didContribute) { _, contributed in
                if contributed {
                    showThankYou = true
                    tipJar.acknowledgeThankYou()
                }
            }
            .alert(L10n.tr("Thank you!"), isPresented: $showThankYou) {
                Button("OK") { }
            } message: {
                Text(L10n.tr("Your contribution means a lot and directly supports VaultSync development. Thank you!"))
            }
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
                    .foregroundStyle(subscriptionManager.isRelaySubscribed ? Color.statusSuccess : Color.statusInactive)
            }
            .accessibilityElement(children: .combine)

            // Expiry date
            if let expiry = subscriptionManager.subscriptionExpiryDate, subscriptionManager.isRelaySubscribed {
                LabeledContent("Renews") {
                    Text(expiry, style: .date)
                }
            }

            if subscriptionManager.isRelaySubscribed {
                relayDeliveryRow
            }

            if !syncthingManager.devices.isEmpty {
                ForEach(syncthingManager.devices) { device in
                    let status = relayProvisionStatus(for: device.deviceID)
                    if status != .provisioned {
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
                                if let url = SyncUserError.troubleshootingURL(forRawError: reason) {
                                    ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                                        .font(.caption2)
                                }
                            }
                        }
                        .padding(.vertical, 2)
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
            } else {
                if subscriptionManager.monthlyProduct != nil || subscriptionManager.yearlyProduct != nil {
                    if let monthly = subscriptionManager.monthlyProduct {
                        subscribeButton(for: monthly, label: L10n.tr("Subscribe Monthly"))
                    }
                    if let yearly = subscriptionManager.yearlyProduct {
                        subscribeButton(for: yearly, label: L10n.tr("Subscribe Yearly"), accent: true)
                    }
                } else {
                    Text("Subscription unavailable")
                        .foregroundStyle(.secondary)
                }

                Button {
                    Task {
                        isRestoring = true
                        await subscriptionManager.restorePurchases()
                        isRestoring = false
                    }
                } label: {
                    HStack {
                        Text("Restore Purchases")
                        if isRestoring {
                            Spacer()
                            ProgressView()
                                .controlSize(.small)
                        }
                    }
                }
                .disabled(isRestoring)
            }

            if subscriptionManager.isRelaySubscribed {
                NavigationLink {
                    RelayServerSetupView(isDelivering: subscriptionManager.relayDeliveryConfirmed)
                } label: {
                    Label(L10n.tr("Set Up Your Server"), systemImage: "server.rack")
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
                        .foregroundStyle(Color.statusError)
                    if let url = SyncUserError.troubleshootingURL(forRawError: relayError) {
                        ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                            .font(.caption2)
                    }
                }
            }

            // Subscription details (required by App Store Review). Price comes
            // from StoreKit so it is correct per storefront — never hard-coded.
            VStack(alignment: .leading, spacing: 2) {
                if let yearly = subscriptionManager.yearlyProduct {
                    Text(L10n.fmt("Cloud Relay — %@", subscriptionManager.priceText(for: yearly)))
                        .font(.caption)
                }
                if let monthly = subscriptionManager.monthlyProduct {
                    Text(L10n.fmt("Cloud Relay — %@", subscriptionManager.priceText(for: monthly)))
                        .font(.caption)
                }
                if subscriptionManager.monthlyProduct == nil, subscriptionManager.yearlyProduct == nil {
                    Text(L10n.tr("Cloud Relay subscription"))
                        .font(.caption)
                }
                Text("Auto-renews until canceled. Cancel anytime in Settings → Subscriptions.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.vertical, 2)
        } header: {
            Text("Cloud Relay")
        } footer: {
            Text("When files change on your server, a silent push wakes VaultSync the moment it happens, so sync feels instant without opening the app. The relay only sends a wake-up signal — it never sees your notes. It needs a one-time helper on your server — tap Set Up Your Server after subscribing.")
        }
    }

    @ViewBuilder
    private var relayDeliveryRow: some View {
        if subscriptionManager.relayDeliveryConfirmed {
            Label(L10n.tr("Delivering wake-ups"), systemImage: "checkmark.seal.fill")
                .foregroundStyle(Color.statusSuccess)
                .font(.subheadline)
                .accessibilityElement(children: .combine)
        } else if let last = subscriptionManager.lastRelayTriggerReceivedAt {
            LabeledContent(L10n.tr("Last wake-up")) {
                Text(last, style: .relative)
            }
        } else {
            VStack(alignment: .leading, spacing: 6) {
                Label(L10n.tr("Waiting for your server"), systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(Color.statusAttention)
                    .font(.subheadline)
                Text(L10n.tr("Cloud Relay is subscribed, but no wake-up has arrived yet. Finish the one-time setup on your server to start receiving instant updates."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .accessibilityElement(children: .combine)
        }
    }

    @ViewBuilder
    private func subscribeButton(for product: Product, label: String, accent: Bool = false) -> some View {
        Button {
            // Cloud Relay can only be provisioned for a paired homeserver. Block
            // the purchase until at least one Syncthing peer exists, otherwise the
            // user would pay and the relay would have no Device ID to wake.
            guard !syncthingManager.devices.isEmpty else {
                alertMessage = L10n.tr("Add your server as a Syncthing device before subscribing to Cloud Relay.")
                showAlert = true
                return
            }
            Task {
                do {
                    let deviceIDs = syncthingManager.devices.map(\.deviceID)
                    try await subscriptionManager.purchase(product, homeserverDeviceIDs: deviceIDs)
                    // A subscription alone delivers nothing until the server-side
                    // helper runs — guide the buyer there immediately.
                    if subscriptionManager.isRelaySubscribed {
                        showServerSetup = true
                    }
                } catch {
                    alertMessage = SyncUserError.from(
                        error: error,
                        fallbackTitle: L10n.tr("Purchase Failed")
                    ).userVisibleDescription
                    showAlert = true
                }
            }
        } label: {
            HStack {
                Text(label)
                    .fontWeight(accent ? .semibold : .regular)
                Spacer()
                if subscriptionManager.purchaseInProgress {
                    ProgressView()
                        .controlSize(.small)
                } else {
                    Text(subscriptionManager.priceText(for: product))
                        .foregroundStyle(accent ? Color.vaultTeal : Color.secondary)
                }
            }
        }
        .disabled(subscriptionManager.purchaseInProgress)
    }

    // MARK: - Notifications Section

    private var notificationsSection: some View {
        Section {
            Toggle(isOn: $conflictNotificationsEnabled) {
                Label(L10n.tr("Conflict Notifications"), systemImage: "exclamationmark.triangle")
            }
        } header: {
            Text(L10n.tr("Notifications"))
        } footer: {
            Text(L10n.tr("Show a banner when sync conflicts are detected. Turning this off does not affect Cloud Relay or background sync — your vault keeps syncing."))
        }
    }

    // MARK: - Support Section

    private var supportSection: some View {
        Section {
            if tipJar.products.isEmpty {
                if tipJar.isLoading {
                    HStack {
                        Text(L10n.tr("Loading…"))
                            .foregroundStyle(.secondary)
                        Spacer()
                        ProgressView()
                            .controlSize(.small)
                    }
                } else {
                    Text(L10n.tr("Contributions are currently unavailable."))
                        .foregroundStyle(.secondary)
                }
            } else {
                ForEach(tipJar.products, id: \.id) { product in
                    Button {
                        Task { await tipJar.purchase(product) }
                    } label: {
                        HStack {
                            Label(contributionTitle(for: product), systemImage: contributionSymbol(for: product))
                            Spacer()
                            if tipJar.purchasingProductID == product.id {
                                ProgressView()
                                    .controlSize(.small)
                            } else {
                                Text(product.displayPrice)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                    .disabled(tipJar.purchasingProductID != nil)
                }
            }

            if let error = tipJar.errorMessage, !error.isEmpty {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Color.statusError)
            }

            if let pending = tipJar.pendingMessage, !pending.isEmpty {
                Text(pending)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        } header: {
            Text(L10n.tr("Support VaultSync"))
        } footer: {
            Text(L10n.tr("VaultSync is an independent, open-source app (MPL-2.0). A one-time contribution keeps it independent, ad-free, and moving forward. It unlocks nothing — VaultSync stays fully functional without it — and you can give as often as you like."))
        }
    }

    private func contributionTitle(for product: Product) -> String {
        if !product.displayName.isEmpty {
            return product.displayName
        }
        switch product.id {
        case TipJarManager.smallProductID:
            return L10n.tr("Small Contribution")
        case TipJarManager.bigProductID:
            return L10n.tr("Big Contribution")
        default:
            return L10n.tr("Contribution")
        }
    }

    private func contributionSymbol(for product: Product) -> String {
        product.id == TipJarManager.bigProductID ? "heart.fill" : "heart"
    }

    private var aboutSection: some View {
        Section("About") {
            Link(destination: DocURL.privacyPolicy) {
                HStack {
                    Label("Privacy Policy", systemImage: "hand.raised")
                    Spacer()
                    Image(systemName: "arrow.up.right.square")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                }
            }

            Link(destination: DocURL.termsOfUse) {
                HStack {
                    Label("Terms of Use", systemImage: "doc.text")
                    Spacer()
                    Image(systemName: "arrow.up.right.square")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                }
            }
        }
    }

    private var relayStatusText: String {
        if subscriptionManager.isRelaySubscribed { return L10n.tr("Active") }
        if subscriptionManager.isLoadingProduct { return L10n.tr("Loading…") }
        if subscriptionManager.availableProduct != nil { return L10n.tr("Not Subscribed") }
        return L10n.tr("Not Configured")
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

}

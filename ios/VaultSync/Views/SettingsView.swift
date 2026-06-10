import StoreKit
import SwiftUI
import UIKit

struct SettingsView: View {
    let syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    /// Routes a tapped checklist remediation to its in-app action. The host
    /// (ContentView) owns the folder picker, the add-device sheet, and the tab
    /// selection, so the action must travel up through this sheet.
    var onChecklistAction: ((SetupChecklistViewModel.ChecklistAction) -> Void)? = nil

    @State private var showSetupStatus = false
    @State private var tipJar = TipJarManager()
    @State private var showThankYou = false
    @State private var deviceIDCopied = false
    @AppStorage(BackgroundSyncService.conflictNotificationsEnabledKey) private var conflictNotificationsEnabled = true
    @Environment(\.dismiss) private var dismiss

    // Cloud Relay now lives entirely in its own tab (RelayHomeView) — subscribe,
    // server setup, diagnostics, and manage-subscription. Settings no longer
    // duplicates it.

    var body: some View {
        NavigationStack {
            List {
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
            .sheet(isPresented: $showSetupStatus) {
                NavigationStack {
                    ScrollView {
                        SetupChecklistView(
                            viewModel: SetupChecklistViewModel(
                                syncthingManager: syncthingManager,
                                vaultManager: vaultManager,
                                subscriptionManager: subscriptionManager
                            ),
                            onAction: onChecklistAction.map { handler in
                                { action in
                                    // Collapse both sheets first; the host delays
                                    // its own presentation until the dismissal
                                    // transition has finished.
                                    showSetupStatus = false
                                    dismiss()
                                    handler(action)
                                }
                            }
                        )
                        .padding(VaultSpacing.l)
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
            Text(L10n.tr("VaultSync is an independent, open-source app (MPL-2.0). A one-time contribution keeps it independent, ad-free, and moving forward — it unlocks nothing, and the app stays fully functional without it."))
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
}

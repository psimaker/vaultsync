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
            // Operational settings first, the contribution ask after them,
            // About last (#187 review).
            VaultPage {
                conflictsSection
                notificationsSection
                thisDeviceSection
                setupStatusSection
                diagnosticsSection
                supportSection
                aboutSection
            }
            .navigationTitle(L10n.tr("Settings"))
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        dismiss()
                    }
                }
            }
            .sheet(isPresented: $showSetupStatus) {
                SetupChecklistSheet(
                    syncthingManager: syncthingManager,
                    vaultManager: vaultManager,
                    subscriptionManager: subscriptionManager,
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

    // MARK: - Support Section

    @ViewBuilder
    private var supportSection: some View {
        VaultSectionHeader(L10n.tr("Support VaultSync"))
        VaultCardGroup {
            if tipJar.products.isEmpty {
                if tipJar.isLoading {
                    VaultRow(L10n.tr("Loading…"), systemImage: "heart", iconTint: .vaultSecondaryLabel) {
                        ProgressView()
                            .controlSize(.small)
                    }
                } else {
                    VaultRow(
                        L10n.tr("Contributions are currently unavailable."),
                        systemImage: "heart",
                        iconTint: .vaultSecondaryLabel
                    )
                }
            } else {
                ForEach(tipJar.products, id: \.id) { product in
                    Button {
                        Task { await tipJar.purchase(product) }
                    } label: {
                        VaultRow(contributionTitle(for: product), systemImage: contributionSymbol(for: product)) {
                            if tipJar.purchasingProductID == product.id {
                                ProgressView()
                                    .controlSize(.small)
                            } else {
                                Text(product.displayPrice)
                                    .font(.subheadline)
                                    .foregroundStyle(Color.vaultSecondaryLabel)
                            }
                        }
                    }
                    .buttonStyle(.vaultRow)
                    .disabled(tipJar.purchasingProductID != nil)
                }
            }
        }

        if let error = tipJar.errorMessage, !error.isEmpty {
            footnote(error, color: .statusErrorText)
        }

        if let pending = tipJar.pendingMessage, !pending.isEmpty {
            footnote(pending)
        }

        footnote(L10n.tr("VaultSync is an independent, open-source app (MPL-2.0). A one-time contribution keeps it independent, ad-free, and moving forward — it unlocks nothing, and the app stays fully functional without it."))
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

    // MARK: - Conflicts Section

    @ViewBuilder
    private var conflictsSection: some View {
        VaultSectionHeader(L10n.tr("Conflicts"))
        VaultCardGroup {
            VaultRow(
                L10n.tr("Review Conflicts Manually"),
                systemImage: "exclamationmark.triangle",
                iconTint: .vaultSecondaryLabel
            )
        }
        footnote(L10n.tr("VaultSync does not automatically choose between conflicting copies in your notes, Obsidian settings, or plugin state. Review each conflict and decide what to keep."))
    }

    // MARK: - Notifications Section

    @ViewBuilder
    private var notificationsSection: some View {
        VaultSectionHeader(L10n.tr("Notifications"))
        VaultCardGroup {
            Toggle(isOn: $conflictNotificationsEnabled) {
                Label {
                    Text(L10n.tr("Conflict Notifications"))
                        .foregroundStyle(Color.vaultLabel)
                } icon: {
                    Image(systemName: "exclamationmark.triangle")
                        .foregroundStyle(Color.vaultSecondaryLabel)
                }
            }
            .tint(Color.vaultAccent)
            .padding(.horizontal, VaultSpacing.l)
            .frame(minHeight: VaultMetrics.rowMinHeight)
        }
        footnote(L10n.tr("Show a banner when sync conflicts are detected. Turning this off does not affect Cloud Relay or background sync — your vault keeps syncing."))
    }

    // MARK: - About Section

    @ViewBuilder
    private var aboutSection: some View {
        VaultSectionHeader(L10n.tr("About"))
        VaultCardGroup {
            externalLinkRow(L10n.tr("Privacy Policy"), systemImage: "hand.raised", url: DocURL.privacyPolicy)
            externalLinkRow(L10n.tr("Terms of Use"), systemImage: "doc.text", url: DocURL.termsOfUse)
            VaultRow("VaultSync", subtitle: appVersion, systemImage: "info.circle", iconTint: .vaultSecondaryLabel)
                .accessibilityElement(children: .combine)
        }
    }

    private func externalLinkRow(_ title: String, systemImage: String, url: URL) -> some View {
        Link(destination: url) {
            VaultRow(title, systemImage: systemImage, iconTint: .vaultSecondaryLabel) {
                Image(systemName: "arrow.up.right")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .accessibilityHidden(true)
            }
        }
        .buttonStyle(.vaultRow)
    }

    /// "Version 2.1.0 (39)" — what support asks for first.
    private var appVersion: String {
        let info = Bundle.main.infoDictionary
        let version = info?["CFBundleShortVersionString"] as? String ?? "?"
        let build = info?["CFBundleVersion"] as? String ?? "?"
        return L10n.fmt("Version %@ (%@)", version, build)
    }

    // MARK: - This Device Section

    @ViewBuilder
    private var thisDeviceSection: some View {
        VaultSectionHeader(L10n.tr("This Device"))
        VaultCardGroup {
            if syncthingManager.deviceID.isEmpty {
                VaultRow(
                    L10n.tr("Device ID"),
                    subtitle: L10n.tr("Not available"),
                    systemImage: "doc.on.doc",
                    iconTint: .vaultSecondaryLabel
                )
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
                    VaultRow(
                        deviceIDCopied ? L10n.tr("Copied") : L10n.tr("Copy Device ID"),
                        systemImage: deviceIDCopied ? "checkmark.circle" : "doc.on.doc",
                        iconTint: deviceIDCopied ? .statusSuccess : .vaultAccent
                    )
                }
                .buttonStyle(.vaultRow)
            }

            NavigationLink {
                SyncActivityView(events: Array(syncthingManager.syncActivity.prefix(50)))
            } label: {
                VaultRow(L10n.tr("Log"), systemImage: "text.append", iconTint: .vaultSecondaryLabel) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
        }
    }

    // MARK: - Setup & Diagnostics

    @ViewBuilder
    private var setupStatusSection: some View {
        VaultCardGroup {
            Button {
                showSetupStatus = true
            } label: {
                VaultRow(L10n.tr("Setup Status"), systemImage: "checklist") {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
        }
        .padding(.top, VaultSpacing.s)
        footnote(L10n.tr("Check setup progress and troubleshooting tips."))
    }

    @ViewBuilder
    private var diagnosticsSection: some View {
        VaultCardGroup {
            NavigationLink {
                ControlledDiagnosticsView(syncthingManager: syncthingManager)
            } label: {
                VaultRow(L10n.tr("Controlled Diagnostics"), systemImage: "stethoscope", iconTint: .vaultSecondaryLabel) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
        }
        .padding(.top, VaultSpacing.s)
        footnote(L10n.tr("Optional, explicit helper pairing and namespace authorization. Nothing is created on upgrade or without your action."))
    }

    /// Section footer text below a card.
    private func footnote(_ text: String, color: Color = .vaultSecondaryLabel) -> some View {
        Text(text)
            .font(.footnote)
            .foregroundStyle(color)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, VaultSpacing.xs)
    }
}

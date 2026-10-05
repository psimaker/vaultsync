import StoreKit
import SwiftUI
import UIKit

/// The Relay tab — the single home for the paid Cloud Relay feature. It has two
/// deliberately different shapes:
///
///  - **Not subscribed:** a focused, decluttered pitch that frames Relay honestly
///    (a tiny private wake-up on top of the already-free P2P sync — NOT cloud
///    storage), then the canonical `SubscribePlanPicker`.
///  - **Subscribed:** an operational control center. Activation is the real lever
///    (most subscriptions never finish the server setup), so when wake-ups aren't
///    arriving yet the setup step is front and center; otherwise it's a calm
///    "active" status plus manage / diagnostics. Relay diagnostics live here now —
///    the old Settings → Cloud Relay section has been removed.
///
/// #187: both shapes open with a hero card and keep everything else in quiet
/// cards. The plan picker (the paywall) is placed on a card but otherwise
/// untouched.
struct RelayHomeView: View {
    let syncthingManager: SyncthingManager
    var subscriptionManager: SubscriptionManager

    @State private var showPrivacyInfo = false
    @State private var purchaseConfirmationInFlight = false
    /// One-time "Connected" celebration on the FIRST real wake-up (K1). Set when
    /// the user acknowledges it; survives backgrounding, so a wake-up that arrived
    /// while the app was away still celebrates on next open.
    @AppStorage("relay-connected-celebrated") private var connectedCelebrated = false

    private var deviceIDs: [String] { syncthingManager.devices.map(\.deviceID) }
    private var isDelivering: Bool { subscriptionManager.relayDeliveryConfirmed }
    private var relayUserStatus: RelayUserStatus {
        subscriptionManager.relayUserStatus(homeserverDeviceIDs: deviceIDs)
    }

    var body: some View {
        VaultPage {
            if subscriptionManager.isRelaySubscribed {
                subscribedContent
            } else {
                pitchContent
            }
        }
        .navigationTitle(L10n.tr("Cloud Relay"))
        .navigationBarTitleDisplayMode(.large)
        .task(id: subscriptionManager.relayStatusPollViewState) {
            await subscriptionManager.refreshRelayDiagnostics(homeserverDeviceIDs: deviceIDs)
            if subscriptionManager.isRelaySubscribed && !subscriptionManager.relayDeliveryConfirmed {
                await subscriptionManager.pollRelayObservationStatus(
                    homeserverDeviceIDs: deviceIDs,
                    context: .waitingView
                )
            }
        }
    }

    // MARK: - Not subscribed

    @ViewBuilder
    private var pitchContent: some View {
        VStack(alignment: .leading, spacing: 14) {
            VaultHeroHeader(
                tone: .accent,
                systemImage: "antenna.radiowaves.left.and.right",
                title: L10n.tr("Instant sync, still private")
            )
            .accessibilityElement(children: .combine)

            Text(L10n.tr("Your notes never touch our servers. Cloud Relay sends a tiny wake-up so changes from your other devices land the moment they happen — even with the app closed."))
                .font(.subheadline)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .fixedSize(horizontal: false, vertical: true)

            Label(L10n.tr("One-step setup: a single line on your server."), systemImage: "terminal")
                .font(.footnote)
                .foregroundStyle(Color.vaultSecondaryLabel)

            Button {
                showPrivacyInfo = true
            } label: {
                Label(L10n.tr("How is this private?"), systemImage: "info.circle")
            }
            .buttonStyle(.vaultLink)
            .popover(isPresented: $showPrivacyInfo) {
                Text(L10n.tr("Your vault already syncs free and peer-to-peer. Relay only removes the “open the app to sync” wait — it isn’t cloud storage."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.leading)
                    // Force multi-line wrapping at a fixed width and let the
                    // popover grow to the full text height — without this the
                    // popover sizes the text to a single line and truncates it.
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(width: 280, alignment: .leading)
                    .padding()
                    .presentationCompactAdaptation(.popover)
            }
        }
        .padding(VaultSpacing.gutter)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(radius: VaultRadius.hero)

        SubscribePlanPicker(
            subscriptionManager: subscriptionManager,
            homeserverDeviceIDs: deviceIDs
        )
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard()
    }

    // MARK: - Subscribed

    @ViewBuilder
    private var subscribedContent: some View {
        if subscriptionManager.relayProvisioningNeedsAttention {
            relayProvisioningUpdateCard
        }

        // Honest status (K1): "delivering" means a REAL wake-up has reached this
        // device (relayDeliveryConfirmed) — never merely "provisioned/reachable".
        // Three states: first-delivery celebration → steady delivering → actively
        // waiting for the helper's first check-in.
        relayStatusHero

        if relayUserStatus == .waitingForFirstSignal {
            nextStepCard(
                title: L10n.tr("Set up the server helper"),
                detail: L10n.tr("One step left: run a single line on your server and instant updates start. The helper only sends a wake-up — it never sees your notes."),
                systemImage: "server.rack"
            ) {
                RelayServerSetupView(isDelivering: isDelivering)
            }
        } else if relayUserStatus == .relayObservedWaitingForWakeUp {
            nextStepCard(
                title: L10n.tr("Open Relay Diagnostics"),
                detail: L10n.tr("Your server reached the Relay, but this iPhone has not received a wake-up yet. You can check again in Diagnostics."),
                systemImage: "stethoscope"
            ) {
                RelayDiagnosticsView(
                    syncthingManager: syncthingManager,
                    subscriptionManager: subscriptionManager
                )
            }
        }

        VaultSectionHeader(L10n.tr("Manage"))
        VaultCardGroup {
            if isDelivering {
                NavigationLink {
                    RelayServerSetupView(isDelivering: isDelivering)
                } label: {
                    VaultRow(L10n.tr("Server helper setup"), systemImage: "server.rack", iconTint: .vaultSecondaryLabel) {
                        VaultChevron()
                    }
                }
                .buttonStyle(.vaultRow)
            }

            NavigationLink {
                RelayDiagnosticsView(
                    syncthingManager: syncthingManager,
                    subscriptionManager: subscriptionManager
                )
            } label: {
                VaultRow(L10n.tr("Relay health & diagnostics"), systemImage: "stethoscope", iconTint: .vaultSecondaryLabel) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)

            Button {
                Task {
                    guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene else { return }
                    try? await AppStore.showManageSubscriptions(in: scene)
                }
            } label: {
                VaultRow(L10n.tr("Manage Subscription"), systemImage: "creditcard", iconTint: .vaultSecondaryLabel) {
                    Image(systemName: "arrow.up.right")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(Color.vaultSecondaryLabel)
                        .accessibilityHidden(true)
                }
            }
            .buttonStyle(.vaultRow)

            if let expiry = subscriptionManager.subscriptionExpiryDate {
                VaultRow(L10n.tr("Renews"), systemImage: "calendar", iconTint: .vaultSecondaryLabel) {
                    Text(expiry, style: .date)
                        .font(.subheadline)
                        .foregroundStyle(Color.vaultSecondaryLabel)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    /// The status hero of a subscribed Relay. `.contain`, NOT `.combine`: the
    /// celebration holds an interactive "Great" button, and `.combine`
    /// flattens the card into one static element that can strand the button
    /// for VoiceOver users (review finding V4). `.contain` groups the status
    /// while keeping the button — the only way to dismiss the celebration —
    /// independently focusable and activatable.
    private var relayStatusHero: some View {
        VStack(alignment: .leading, spacing: 14) {
            if relayUserStatus == .wakeUpReceived && !connectedCelebrated {
                // [5] First real wake-up. Celebrate — but no fantasy latency
                // ("in 2s"): the app can't honestly time a server-driven push.
                heroHeader(status: .synced, title: L10n.tr("Connected"))
                heroDetail(L10n.tr("A wake-up signal reached this iPhone. This confirms delivery to this device, not that every change has finished syncing."))
                Button(L10n.tr("Great")) {
                    connectedCelebrated = true
                }
                .buttonStyle(.vault(.primary, compact: true))
            } else if relayUserStatus == .wakeUpReceived {
                heroHeader(status: .synced, title: L10n.tr("Cloud Relay active"))
                heroDetail(L10n.tr("A wake-up signal was received on this iPhone. VaultSync then started its normal sync check."))
                if let last = subscriptionManager.lastRelayTriggerReceivedAt {
                    HStack(spacing: VaultSpacing.s) {
                        Image(systemName: "clock")
                            .accessibilityHidden(true)
                        Text(L10n.tr("Last wake-up"))
                        Text(last, style: .relative)
                    }
                    .font(.footnote)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .accessibilityElement(children: .combine)
                }
            } else {
                heroHeader(status: waitingStatus, title: relayUserStatus.userFacingTitle)
                heroDetail(relayUserStatus.userFacingDetail)
            }
        }
        .padding(VaultSpacing.gutter)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(radius: VaultRadius.hero)
        .accessibilityElement(children: .contain)
    }

    /// The hero well for each waiting state — the same mapping the former
    /// status badges used.
    private var waitingStatus: SyncStatus {
        switch relayUserStatus {
        case .checking, .relayObservedWithinGrace:
            return .starting
        case .waitingForFirstSignal, .relayObservedWaitingForWakeUp, .statusUnavailable:
            return .attention
        case .quietCanBeNormal:
            return .paused
        case .wakeUpReceived:
            return .synced
        }
    }

    /// The Relay's own words carry the meaning: each title ("Cloud Relay
    /// active", "Connected", the waiting titles) already says what the state
    /// is. The sync status only picks the well's tone — its spoken label
    /// ("All Synced", "Paused") would claim something a wake-up does not
    /// prove (#187 review).
    private func heroHeader(status: SyncStatus, title: String) -> some View {
        VaultHeroHeader(
            tone: status.tone,
            systemImage: status == .synced ? "antenna.radiowaves.left.and.right" : status.wellSymbolName,
            title: title
        )
        .accessibilityElement(children: .combine)
    }

    private func heroDetail(_ text: String) -> some View {
        Text(text)
            .font(.subheadline)
            .foregroundStyle(Color.vaultSecondaryLabel)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// The one missing step, on the attention wash, one tap away.
    private func nextStepCard<Destination: View>(
        title: String,
        detail: String,
        systemImage: String,
        @ViewBuilder destination: @escaping () -> Destination
    ) -> some View {
        VaultCardGroup(tone: .attention) {
            NavigationLink {
                destination()
            } label: {
                VaultRow(title, subtitle: detail, systemImage: systemImage, iconTint: .statusAttention) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
        }
    }

    private var relayProvisioningUpdateCard: some View {
        let storageError = subscriptionManager.relayDeviceIDStorageErrorMessage
        return VStack(alignment: .leading, spacing: VaultSpacing.m) {
            if storageError != nil {
                Label(
                    L10n.tr("Relay Provisioning Failed"),
                    systemImage: "exclamationmark.shield"
                )
                .font(.headline)
                .foregroundStyle(Color.statusErrorText)
            } else {
                Label(
                    L10n.tr("Automatic updates are being updated"),
                    systemImage: "arrow.triangle.2.circlepath"
                )
                .font(.headline)
                .foregroundStyle(Color.statusInfoText)
            }

            if let storageError {
                Text(storageError)
                    .font(.subheadline)
                    .foregroundStyle(Color.statusErrorText)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if subscriptionManager.relayProvisioningNeedsStoreKitVerification {
                Text(L10n.tr("Your purchase must be confirmed again."))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                Button {
                    Task {
                        purchaseConfirmationInFlight = true
                        _ = await subscriptionManager.restorePurchases(
                            homeserverDeviceIDs: deviceIDs
                        )
                        purchaseConfirmationInFlight = false
                    }
                } label: {
                    HStack(spacing: VaultSpacing.s) {
                        Text(L10n.tr("Restore Purchases"))
                        if purchaseConfirmationInFlight {
                            ProgressView().controlSize(.small)
                        }
                    }
                }
                .buttonStyle(.vault(.primary, compact: true))
                .disabled(purchaseConfirmationInFlight)
            } else if subscriptionManager.relayProvisioningTemporarilyFailed || storageError != nil {
                Text(L10n.tr("Try Again Later"))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultSecondaryLabel)
            } else {
                ProgressView()
                    .controlSize(.small)
                    .accessibilityLabel(L10n.tr("Automatic updates are being updated"))
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(tone: storageError != nil ? .error : .info)
    }
}

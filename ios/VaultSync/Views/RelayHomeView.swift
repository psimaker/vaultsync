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
struct RelayHomeView: View {
    let syncthingManager: SyncthingManager
    var subscriptionManager: SubscriptionManager

    @State private var showPrivacyInfo = false
    /// One-time "Connected" celebration on the FIRST real wake-up (K1). Set when
    /// the user acknowledges it; survives backgrounding, so a wake-up that arrived
    /// while the app was away still celebrates on next open.
    @AppStorage("relay-connected-celebrated") private var connectedCelebrated = false

    private var deviceIDs: [String] { syncthingManager.devices.map(\.deviceID) }
    private var isDelivering: Bool { subscriptionManager.relayDeliveryConfirmed }

    var body: some View {
        List {
            if subscriptionManager.isRelaySubscribed {
                subscribedContent
            } else {
                pitchContent
            }
        }
        .navigationTitle(L10n.tr("Cloud Relay"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await subscriptionManager.refreshRelayDiagnostics(homeserverDeviceIDs: deviceIDs)
        }
    }

    // MARK: - Not subscribed

    @ViewBuilder
    private var pitchContent: some View {
        Section {
            VStack(alignment: .leading, spacing: VaultSpacing.m) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .font(.largeTitle)
                    .foregroundStyle(Color.vaultAccent)
                    .accessibilityHidden(true)

                Text(L10n.tr("Instant sync, still private"))
                    .font(.title2.weight(.bold))

                Text(L10n.tr("Your notes never touch our servers. Cloud Relay sends a tiny wake-up so changes from your other devices land the moment they happen — even with the app closed."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                Button {
                    showPrivacyInfo = true
                } label: {
                    Label(L10n.tr("How is this private?"), systemImage: "info.circle")
                        .font(.footnote)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Color.vaultAccent)
                .padding(.top, VaultSpacing.xs)
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
            .padding(.vertical, VaultSpacing.s)
        }

        Section {
            SubscribePlanPicker(
                subscriptionManager: subscriptionManager,
                homeserverDeviceIDs: deviceIDs
            )
        }
    }

    // MARK: - Subscribed

    @ViewBuilder
    private var subscribedContent: some View {
        // Honest status (K1): "delivering" means a REAL wake-up has reached this
        // device (relayDeliveryConfirmed) — never merely "provisioned/reachable".
        // Three states: first-delivery celebration → steady delivering → actively
        // waiting for the helper's first check-in.
        Section {
            VStack(alignment: .leading, spacing: VaultSpacing.s) {
                if isDelivering && !connectedCelebrated {
                    // [5] First real wake-up. Celebrate — but no fantasy latency
                    // ("in 2s"): the app can't honestly time a server-driven push.
                    StatusBadge(.synced, text: L10n.tr("Connected"))
                        .font(.headline)
                    Text(L10n.tr("Your server just reached this iPhone. Incoming changes now sync the moment they happen."))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    Button(L10n.tr("Great")) {
                        connectedCelebrated = true
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Color.vaultAccent)
                    .padding(.top, VaultSpacing.xs)
                } else if isDelivering {
                    StatusBadge(.synced, text: L10n.tr("Cloud Relay active"))
                        .font(.headline)
                    Text(L10n.tr("Wake-ups are being delivered — changes from your other devices arrive instantly."))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    if let last = subscriptionManager.lastRelayTriggerReceivedAt {
                        LabeledContent(L10n.tr("Last wake-up")) {
                            Text(last, style: .relative)
                        }
                        .font(.subheadline)
                    }
                } else if subscriptionManager.lastRelayTriggerReceivedAt != nil {
                    // [6] Delivered before, now quiet — recovery, NOT "set up again".
                    StatusBadge(.attention, text: L10n.tr("Cloud Relay went quiet"))
                        .font(.headline)
                    Text(L10n.tr("No wake-up has arrived in a while. Make sure the helper is still running on the computer or server you keep on."))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                } else {
                    // Never delivered: blocked on finishing setup (below), not on a
                    // live handshake — so no spinner (the app waits for a push, it
                    // doesn't poll). Updates by itself once the first wake-up lands.
                    StatusBadge(.attention, text: L10n.tr("Not active yet"))
                        .font(.headline)
                    Text(L10n.tr("You’re subscribed. Wake-ups start once the helper is running on the computer or server you keep on — finish setup below."))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.vertical, VaultSpacing.xs)
            // `.contain`, NOT `.combine`: the celebration branch holds an
            // interactive "Great" button, and `.combine` flattens the VStack into
            // one static element that can strand the button for VoiceOver users
            // (review finding V4). `.contain` groups the status while keeping the
            // button — the only way to dismiss the celebration — independently
            // focusable and activatable.
            .accessibilityElement(children: .contain)
        }

        // Activation lever — prominent while wake-ups aren't arriving yet.
        if !isDelivering {
            Section {
                NavigationLink {
                    RelayServerSetupView(isDelivering: isDelivering)
                } label: {
                    Label(L10n.tr("Set up the server helper"), systemImage: "server.rack")
                        .font(.headline)
                }
            } footer: {
                Text(L10n.tr("Run the one-time vaultsync-notify helper on your server to start receiving instant updates. It only sends a wake-up — it never sees your notes."))
            }
        }

        Section {
            if isDelivering {
                NavigationLink {
                    RelayServerSetupView(isDelivering: isDelivering)
                } label: {
                    Label(L10n.tr("Server helper setup"), systemImage: "server.rack")
                }
            }

            NavigationLink {
                RelayDiagnosticsView(
                    syncthingManager: syncthingManager,
                    subscriptionManager: subscriptionManager
                )
            } label: {
                Label(L10n.tr("Relay health & diagnostics"), systemImage: "stethoscope")
            }

            Button {
                Task {
                    guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene else { return }
                    try? await AppStore.showManageSubscriptions(in: scene)
                }
            } label: {
                Label(L10n.tr("Manage Subscription"), systemImage: "creditcard")
            }

            if let expiry = subscriptionManager.subscriptionExpiryDate {
                LabeledContent(L10n.tr("Renews")) {
                    Text(expiry, style: .date)
                }
            }
        } header: {
            Text(L10n.tr("Manage"))
        }

        if let relayError = subscriptionManager.errorMessage, !relayError.isEmpty {
            Section {
                Text(relayError)
                    .font(.caption)
                    .foregroundStyle(Color.statusError)
                if let url = SyncUserError.troubleshootingURL(forRawError: relayError) {
                    ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                        .font(.footnote)
                }
            }
        }
    }
}

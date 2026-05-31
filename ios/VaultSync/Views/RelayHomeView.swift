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
                    .font(.system(size: 34))
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
        Section {
            VStack(alignment: .leading, spacing: VaultSpacing.s) {
                StatusBadge(
                    isDelivering ? .synced : .attention,
                    text: isDelivering
                        ? L10n.tr("Cloud Relay active")
                        : L10n.tr("One step left to activate")
                )
                .font(.headline)

                Text(isDelivering
                     ? L10n.tr("Wake-ups are being delivered — changes from your other devices arrive instantly.")
                     : L10n.tr("You’re subscribed, but no wake-up has arrived yet. Cloud Relay only delivers once the helper is running on your server."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                if isDelivering, let last = subscriptionManager.lastRelayTriggerReceivedAt {
                    LabeledContent(L10n.tr("Last wake-up")) {
                        Text(last, style: .relative)
                    }
                    .font(.subheadline)
                }
            }
            .padding(.vertical, VaultSpacing.xs)
            .accessibilityElement(children: .combine)
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

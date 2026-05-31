import SwiftUI

/// The Relay tab — the single home for the paid Cloud Relay feature. Replaces the
/// three disconnected altitudes (marketing paywall, Docker setup wall, engineer
/// diagnostics dump) with one progressively-disclosed flow that cross-links them:
///
///  - Not subscribed: plain-language pitch + the canonical `SubscribePlanPicker`.
///  - Subscribed: a status header + a three-step spine (Subscribe → run the
///    server helper → verify delivery) whose steps link into Server Setup and
///    Diagnostics, finally closing the funnel that previously had no links.
struct RelayHomeView: View {
    let syncthingManager: SyncthingManager
    var subscriptionManager: SubscriptionManager

    private var deviceIDs: [String] { syncthingManager.devices.map(\.deviceID) }
    private var isDelivering: Bool { subscriptionManager.relayDeliveryConfirmed }

    var body: some View {
        List {
            if subscriptionManager.isRelaySubscribed {
                subscribedSections
            } else {
                pitchSections
            }
        }
        .navigationTitle(L10n.tr("Cloud Relay"))
        .navigationBarTitleDisplayMode(.inline)
    }

    // MARK: - Not subscribed

    @ViewBuilder
    private var pitchSections: some View {
        Section {
            VStack(alignment: .leading, spacing: VaultSpacing.s) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .font(.largeTitle)
                    .foregroundStyle(Color.vaultAccent)
                    .accessibilityHidden(true)
                Text(L10n.tr("Make incoming sync instant"))
                    .font(.title3.weight(.bold))
                Text(L10n.tr("Your vault already syncs when you open VaultSync. Cloud Relay adds a silent push so changes on your server reach this iPhone the moment they happen — no need to open the app first."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            .padding(.vertical, VaultSpacing.xs)
        }

        Section {
            benefitRow(icon: "bolt.fill", L10n.tr("Near-instant server → iPhone updates"))
            benefitRow(icon: "lock.shield.fill", L10n.tr("The relay only sends a wake-up — it never sees your notes"))
            benefitRow(icon: "xmark.circle.fill", L10n.tr("Cancel anytime in Settings → Subscriptions"))
        }

        Section {
            SubscribePlanPicker(
                subscriptionManager: subscriptionManager,
                homeserverDeviceIDs: deviceIDs
            )
        } footer: {
            Text(L10n.tr("Cloud Relay needs a one-time helper on your server, shown right after you subscribe."))
        }
    }

    // MARK: - Subscribed

    @ViewBuilder
    private var subscribedSections: some View {
        Section {
            StatusBadge(
                isDelivering ? .synced : .attention,
                text: isDelivering
                    ? L10n.tr("Cloud Relay active")
                    : L10n.tr("Finish server setup")
            )
            .font(.headline)
            Text(isDelivering
                 ? L10n.tr("Wake-ups are being delivered — incoming changes sync the moment they happen.")
                 : L10n.tr("You’re subscribed. Cloud Relay only delivers wake-ups once the helper is running on your server."))
                .font(.subheadline)
                .foregroundStyle(.secondary)
            if let expiry = subscriptionManager.subscriptionExpiryDate {
                DetailRow(title: L10n.tr("Renews"), value: expiry.formatted(date: .abbreviated, time: .omitted))
            }
        }

        Section {
            spineRow(done: true, title: L10n.tr("Subscribe"), detail: L10n.tr("Your Cloud Relay subscription is active."))

            NavigationLink {
                RelayServerSetupView(isDelivering: isDelivering)
            } label: {
                spineLabel(
                    done: isDelivering,
                    title: L10n.tr("Run the server helper"),
                    detail: L10n.tr("Start vaultsync-notify on your server — copyable command inside.")
                )
            }

            NavigationLink {
                RelayDiagnosticsView(
                    syncthingManager: syncthingManager,
                    subscriptionManager: subscriptionManager
                )
            } label: {
                spineLabel(
                    done: isDelivering,
                    title: L10n.tr("Verify delivery"),
                    detail: L10n.tr("Check relay health, push token, and per-device provisioning.")
                )
            }
        } header: {
            Text(L10n.tr("Finish setup"))
        } footer: {
            Text(L10n.tr("Manage or cancel your subscription anytime in Settings → Subscriptions."))
        }
    }

    // MARK: - Helpers

    private func benefitRow(icon: String, _ text: String) -> some View {
        Label {
            Text(text).font(.subheadline)
        } icon: {
            Image(systemName: icon).foregroundStyle(Color.vaultAccent)
        }
        .accessibilityElement(children: .combine)
    }

    private func spineRow(done: Bool, title: String, detail: String) -> some View {
        spineLabel(done: done, title: title, detail: detail)
    }

    private func spineLabel(done: Bool, title: String, detail: String) -> some View {
        HStack(spacing: VaultSpacing.m) {
            Image(systemName: done ? "checkmark.circle.fill" : "circle")
                .font(.title3)
                .foregroundStyle(done ? Color.statusSuccess : Color.statusInactive)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.body)
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityValue(done ? L10n.tr("Done") : "")
    }
}

import StoreKit
import SwiftUI

/// In-context Cloud Relay offer, presented at the "aha moment" (right after the
/// first successful sync) and from a dashboard upgrade affordance. Lets the user
/// subscribe in place, then flows straight into the mandatory server setup so the
/// subscription actually delivers value.
struct CloudRelayUpsellView: View {
    let syncthingManager: SyncthingManager
    var subscriptionManager: SubscriptionManager

    @Environment(\.dismiss) private var dismiss
    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var isRestoring = false
    private let teal = Color.vaultTeal

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
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button(subscriptionManager.isRelaySubscribed ? L10n.tr("Done") : L10n.tr("Not Now")) { dismiss() }
            }
        }
        .alert("Error", isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
    }

    // MARK: - Pitch (not yet subscribed)

    @ViewBuilder
    private var pitchContent: some View {
        Section {
            VStack(alignment: .leading, spacing: 10) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .font(.largeTitle)
                    .foregroundStyle(teal)
                    .accessibilityHidden(true)
                Text(L10n.tr("Make incoming sync instant"))
                    .font(.title3.weight(.bold))
                Text(L10n.tr("Your vault already syncs when you open VaultSync. Cloud Relay adds a silent push so changes on your server reach this iPhone the moment they happen — no need to open the app first."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            .padding(.vertical, 4)
        }

        Section {
            benefitRow(icon: "bolt.fill", L10n.tr("Near-instant server → iPhone updates"))
            benefitRow(icon: "lock.shield.fill", L10n.tr("The relay only sends a wake-up — it never sees your notes"))
            benefitRow(icon: "xmark.circle.fill", L10n.tr("Cancel anytime in Settings → Subscriptions"))
        }

        Section {
            if subscriptionManager.monthlyProduct != nil || subscriptionManager.yearlyProduct != nil {
                if let yearly = subscriptionManager.yearlyProduct {
                    subscribeButton(for: yearly, label: L10n.tr("Subscribe Yearly"), accent: true)
                }
                if let monthly = subscriptionManager.monthlyProduct {
                    subscribeButton(for: monthly, label: L10n.tr("Subscribe Monthly"))
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
                        ProgressView().controlSize(.small)
                    }
                }
            }
            .disabled(isRestoring)
        } footer: {
            subscriptionDetailsFooter
        }
    }

    // MARK: - Subscribed (flow into server setup)

    @ViewBuilder
    private var subscribedContent: some View {
        Section {
            Label(L10n.tr("You’re subscribed to Cloud Relay"), systemImage: "checkmark.seal.fill")
                .foregroundStyle(Color.statusSuccess)
                .font(.headline)
        }
        Section {
            Text(L10n.tr("One step left: Cloud Relay only delivers wake-ups once a small helper is running on your server."))
                .font(.subheadline)
            NavigationLink {
                RelayServerSetupView(isDelivering: subscriptionManager.relayDeliveryConfirmed)
            } label: {
                Label(L10n.tr("Set Up Your Server"), systemImage: "server.rack")
            }
        } header: {
            Text(L10n.tr("Finish setup"))
        }
    }

    // MARK: - Helpers

    private func benefitRow(icon: String, _ text: String) -> some View {
        Label {
            Text(text).font(.subheadline)
        } icon: {
            Image(systemName: icon).foregroundStyle(teal)
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder
    private func subscribeButton(for product: Product, label: String, accent: Bool = false) -> some View {
        Button {
            Task {
                do {
                    let deviceIDs = syncthingManager.devices.map(\.deviceID)
                    try await subscriptionManager.purchase(product, homeserverDeviceIDs: deviceIDs)
                    // On success the view switches to subscribedContent, which
                    // surfaces the server-setup step — no extra navigation needed.
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
                    ProgressView().controlSize(.small)
                } else {
                    Text(subscriptionManager.priceText(for: product))
                        .foregroundStyle(accent ? Color.vaultTeal : Color.secondary)
                }
            }
        }
        .disabled(subscriptionManager.purchaseInProgress)
    }

    @ViewBuilder
    private var subscriptionDetailsFooter: some View {
        VStack(alignment: .leading, spacing: 2) {
            if let monthly = subscriptionManager.monthlyProduct {
                Text(L10n.fmt("Cloud Relay — %@", subscriptionManager.priceText(for: monthly)))
            }
            if let yearly = subscriptionManager.yearlyProduct {
                Text(L10n.fmt("Cloud Relay — %@", subscriptionManager.priceText(for: yearly)))
            }
            Text("Auto-renews until canceled. Cancel anytime in Settings → Subscriptions.")
            Text(L10n.tr("Cloud Relay needs a one-time helper on your server, shown right after you subscribe."))
        }
        .font(.caption)
    }
}

import StoreKit
import SwiftUI

/// The single canonical Cloud Relay subscription picker. Yearly is listed first
/// and highlighted as the recommended plan; monthly sits below; restore and a
/// prominent, compliant price / term / auto-renew disclosure with Terms & Privacy
/// links follow (App Store guideline 3.1.2(a)).
///
/// Used by both the Relay tab and the in-context upsell so the two can never
/// drift in ordering or copy again — replacing the previously duplicated blocks
/// that listed the plans in opposite orders.
struct SubscribePlanPicker: View {
    var subscriptionManager: SubscriptionManager
    let homeserverDeviceIDs: [String]

    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var isRestoring = false

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            if subscriptionManager.yearlyProduct == nil && subscriptionManager.monthlyProduct == nil {
                if subscriptionManager.isLoadingProduct {
                    HStack(spacing: VaultSpacing.s) {
                        ProgressView()
                        Text(L10n.tr("Loading plans…")).foregroundStyle(.secondary)
                    }
                } else {
                    Text(L10n.tr("Subscription unavailable")).foregroundStyle(.secondary)
                }
            } else {
                if let yearly = subscriptionManager.yearlyProduct {
                    planCard(product: yearly, title: L10n.tr("Yearly"), recommended: true)
                }
                if let monthly = subscriptionManager.monthlyProduct {
                    planCard(product: monthly, title: L10n.tr("Monthly"), recommended: false)
                }
            }

            Button {
                Task {
                    isRestoring = true
                    await subscriptionManager.restorePurchases()
                    isRestoring = false
                }
            } label: {
                HStack {
                    Text(L10n.tr("Restore Purchases"))
                    if isRestoring {
                        Spacer()
                        ProgressView().controlSize(.small)
                    }
                }
                .font(.subheadline)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .foregroundStyle(Color.vaultAccent)
            .disabled(isRestoring)

            complianceFooter
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .alert("Error", isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
    }

    private func planCard(product: Product, title: String, recommended: Bool) -> some View {
        Button {
            purchase(product)
        } label: {
            HStack(spacing: VaultSpacing.m) {
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: VaultSpacing.s) {
                        Text(title)
                            .font(.headline)
                        if recommended {
                            Text(L10n.tr("Best value"))
                                .font(.caption2.weight(.bold))
                                .padding(.horizontal, 8)
                                .padding(.vertical, 2)
                                .background(Color.vaultAccent, in: Capsule())
                                .foregroundStyle(.white)
                        }
                    }
                    Text(subscriptionManager.priceText(for: product))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: VaultSpacing.s)
                if subscriptionManager.purchaseInProgress {
                    ProgressView()
                } else {
                    Image(systemName: "chevron.right")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                }
            }
            .padding(VaultSpacing.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                Color(.secondarySystemBackground),
                in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
            )
            .overlay {
                RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
                    .stroke(recommended ? Color.vaultAccent : Color.clear, lineWidth: 2)
            }
        }
        .buttonStyle(.plain)
        .disabled(subscriptionManager.purchaseInProgress)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(L10n.fmt("%1$@ — %2$@", title, subscriptionManager.priceText(for: product)))
        .accessibilityHint(L10n.tr("Starts a subscription purchase."))
    }

    private var complianceFooter: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.xs) {
            Text(L10n.tr("Auto-renews until canceled. Cancel anytime in Settings → Subscriptions."))
            HStack(spacing: VaultSpacing.l) {
                ExternalLinkButton(titleKey: "Terms of Use", url: DocURL.termsOfUse)
                ExternalLinkButton(titleKey: "Privacy Policy", url: DocURL.privacyPolicy)
            }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
    }

    private func purchase(_ product: Product) {
        Task {
            do {
                try await subscriptionManager.purchase(product, homeserverDeviceIDs: homeserverDeviceIDs)
            } catch {
                alertMessage = SyncUserError.from(
                    error: error,
                    fallbackTitle: L10n.tr("Purchase Failed")
                ).userVisibleDescription
                showAlert = true
            }
        }
    }
}

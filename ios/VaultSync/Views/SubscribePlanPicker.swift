import StoreKit
import SwiftUI

/// The single canonical Cloud Relay subscription picker. Monthly is listed first
/// (the low-commitment entry point — leading with the small price avoids the
/// "annual sticker shock" that makes people reach for a full cloud-sync product
/// instead); yearly follows, framed as savings and marked as the best value.
/// Prominent, compliant price / term / auto-renew disclosure with Terms & Privacy
/// links follows (App Store guideline 3.1.2(a)).
///
/// Relay can only be provisioned for a paired homeserver, so with no devices the
/// picker shows a guide instead of letting the user pay for nothing to wake.
struct SubscribePlanPicker: View {
    var subscriptionManager: SubscriptionManager
    let homeserverDeviceIDs: [String]

    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var isRestoring = false

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            if homeserverDeviceIDs.isEmpty {
                noDeviceNotice
            } else if subscriptionManager.yearlyProduct == nil && subscriptionManager.monthlyProduct == nil {
                if subscriptionManager.isLoadingProduct {
                    HStack(spacing: VaultSpacing.s) {
                        ProgressView()
                        Text(L10n.tr("Loading plans…")).foregroundStyle(.secondary)
                    }
                } else {
                    Text(L10n.tr("Subscription unavailable")).foregroundStyle(.secondary)
                }
            } else {
                if let monthly = subscriptionManager.monthlyProduct {
                    planCard(product: monthly, title: L10n.tr("Monthly"), recommended: false)
                }
                if let yearly = subscriptionManager.yearlyProduct {
                    planCard(product: yearly, title: L10n.tr("Yearly"), recommended: true)
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

    private var noDeviceNotice: some View {
        HStack(alignment: .top, spacing: VaultSpacing.m) {
            Image(systemName: "laptopcomputer.slash")
                .font(.title3)
                .foregroundStyle(Color.statusAttention)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(L10n.tr("Add your server first"))
                    .font(.headline)
                Text(L10n.tr("Cloud Relay wakes a specific device. Pair the computer or server that hosts your vault on the Devices tab, then come back to subscribe."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
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
                        if recommended, let savings = yearlySavingsText {
                            Text(savings)
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
                        .accessibilityHidden(true)
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
        .accessibilityLabel(planAccessibilityLabel(title: title, product: product, recommended: recommended))
        .accessibilityHint(L10n.tr("Starts a subscription purchase."))
    }

    /// VoiceOver label for a plan card — folds the "best value / save N%" badge into
    /// the spoken label, which the combined element would otherwise drop.
    private func planAccessibilityLabel(title: String, product: Product, recommended: Bool) -> String {
        let base = L10n.fmt("%1$@ — %2$@", title, subscriptionManager.priceText(for: product))
        if recommended, let savings = yearlySavingsText {
            return base + ". " + savings
        }
        return base
    }

    /// "Save N%" for the yearly plan vs. paying monthly for a year. Derived from
    /// StoreKit prices so it is correct per storefront; nil if not computable.
    private var yearlySavingsText: String? {
        guard let monthly = subscriptionManager.monthlyProduct,
              let yearly = subscriptionManager.yearlyProduct else { return nil }
        let monthlyAnnual = monthly.price * 12
        guard monthlyAnnual > 0 else { return nil }
        let fraction = (monthlyAnnual - yearly.price) / monthlyAnnual
        let percent = NSDecimalNumber(decimal: fraction * 100).intValue
        guard percent > 0 else { return L10n.tr("Best value") }
        return L10n.fmt("Save %d%%", percent)
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

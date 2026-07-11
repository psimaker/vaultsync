import StoreKit
import SwiftUI

/// The single canonical Cloud Relay subscription picker. Monthly is listed first
/// (the low-commitment entry point — leading with the small price avoids the
/// "annual sticker shock" that makes people reach for a full cloud-sync product
/// instead); yearly follows, framed as savings and marked as the best value.
/// Prominent, compliant price / term / auto-renew disclosure with Terms & Privacy
/// links follows (App Store guideline 3.1.2(a)).
///
/// A plan row only SELECTS; the purchase starts exclusively from the explicit
/// Subscribe button below the rows (#70) — the rows used to start the StoreKit
/// purchase directly while also showing a navigation chevron and a
/// "recommended" outline, three mismatched signals on the payment surface.
///
/// Relay can only be provisioned for a paired homeserver, so with no devices the
/// picker shows a guide instead of letting the user pay for nothing to wake.
struct SubscribePlanPicker: View {
    var subscriptionManager: SubscriptionManager
    let homeserverDeviceIDs: [String]

    @State private var alertTitle: String?
    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var isRestoring = false
    /// The user's explicit row choice; nil until first tap. `selectedProduct`
    /// falls back to the recommended yearly plan so the Subscribe button is
    /// never armed against an undefined plan.
    @State private var selectedPlanID: Product.ID?

    private var selectedProduct: Product? {
        let available = [subscriptionManager.monthlyProduct, subscriptionManager.yearlyProduct]
            .compactMap { $0 }
        if let selectedPlanID,
           let chosen = available.first(where: { $0.id == selectedPlanID }) {
            return chosen
        }
        return subscriptionManager.yearlyProduct ?? subscriptionManager.monthlyProduct
    }

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            if homeserverDeviceIDs.isEmpty {
                noDeviceNotice
            } else if subscriptionManager.yearlyProduct == nil && subscriptionManager.monthlyProduct == nil {
                if subscriptionManager.isLoadingProduct || !subscriptionManager.productLoadFailed {
                    HStack(spacing: VaultSpacing.s) {
                        ProgressView()
                        Text(L10n.tr("Loading plans…")).foregroundStyle(.secondary)
                    }
                } else {
                    VStack(alignment: .leading, spacing: VaultSpacing.s) {
                        Text(L10n.tr("The subscription plans could not be loaded. This is usually a brief network or App Store hiccup."))
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                        Button {
                            Task { await subscriptionManager.reloadProductsIfNeeded() }
                        } label: {
                            Label(L10n.tr("Try Again"), systemImage: "arrow.clockwise")
                                .font(.subheadline.weight(.semibold))
                        }
                        .buttonStyle(.bordered)
                    }
                }
            } else {
                if let monthly = subscriptionManager.monthlyProduct {
                    planCard(product: monthly, title: L10n.tr("Monthly"), recommended: false)
                }
                if let yearly = subscriptionManager.yearlyProduct {
                    planCard(product: yearly, title: L10n.tr("Yearly"), recommended: true)
                }
                subscribeButton
            }

            if subscriptionManager.purchasePendingApproval {
                pendingApprovalNotice
            }

            if let verificationHint = subscriptionManager.unverifiedRelayTransactionMessage {
                Label {
                    Text(verificationHint)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                } icon: {
                    Image(systemName: "exclamationmark.shield")
                        .foregroundStyle(Color.statusAttention)
                }
                .accessibilityElement(children: .combine)
            }

            Button {
                guard !subscriptionManager.purchaseInProgress, !isRestoring else { return }
                Task {
                    isRestoring = true
                    let outcome = await subscriptionManager.restorePurchases(
                        homeserverDeviceIDs: homeserverDeviceIDs
                    )
                    isRestoring = false
                    switch outcome {
                    case .restored, .cancelled:
                        // Restored flips RelayHomeView to subscribedContent by
                        // itself; a cancelled App Store sign-in needs no alert.
                        break
                    case .nothingToRestore:
                        alertTitle = L10n.tr("No Purchases Found")
                        alertMessage = L10n.tr("No Cloud Relay subscription was found for this Apple Account. If you subscribed with a different account, sign in with it in the App Store and try again.")
                        showAlert = true
                    case .foundButUnverified:
                        alertTitle = L10n.tr("Purchase Could Not Be Verified")
                        alertMessage = subscriptionManager.unverifiedRelayTransactionMessage
                        showAlert = true
                    case .failed:
                        alertTitle = L10n.tr("Restore Failed")
                        alertMessage = L10n.tr("The App Store could not be reached to restore purchases. Check your internet connection and try again.")
                        showAlert = true
                    }
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
            .disabled(isRestoring || subscriptionManager.purchaseInProgress)

            complianceFooter
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .alert(alertTitle ?? L10n.tr("Purchase Failed"), isPresented: $showAlert) {
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
            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
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

    /// Ask to Buy: the purchase sits with the family organizer. Informational
    /// only — Subscribe stays enabled (a declined request emits no StoreKit
    /// event, so this must never dead-end the purchase surface) and Dismiss is
    /// explicit, never automatic (#96).
    private var pendingApprovalNotice: some View {
        HStack(alignment: .top, spacing: VaultSpacing.m) {
            Image(systemName: "hourglass")
                .font(.title3)
                .foregroundStyle(Color.statusInfo)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                Text(L10n.tr("Waiting for approval"))
                    .font(.headline)
                Text(L10n.tr("This purchase needs approval (Ask to Buy). Cloud Relay activates automatically once it is approved. If it was declined, you can simply subscribe again."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button(L10n.tr("Dismiss")) {
                    subscriptionManager.clearPendingApproval()
                }
                .font(.subheadline)
                .buttonStyle(.plain)
                .foregroundStyle(Color.vaultAccent)
                .padding(.top, VaultSpacing.xxs)
            }
        }
        .padding(VaultSpacing.l)
        .background(
            Color(.secondarySystemBackground),
            in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
        )
        .accessibilityElement(children: .contain)
    }

    /// A selection row: tapping picks the plan, the outline + check mark show
    /// the CURRENT choice (never "recommended" — that is the savings badge's
    /// job), and nothing here starts a purchase (#70).
    private func planCard(product: Product, title: String, recommended: Bool) -> some View {
        let isSelected = selectedProduct?.id == product.id
        return Button {
            selectedPlanID = product.id
        } label: {
            HStack(spacing: VaultSpacing.m) {
                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    HStack(spacing: VaultSpacing.s) {
                        Text(title)
                            .font(.headline)
                        if recommended, let savings = yearlySavingsText {
                            StatusTag(text: savings, tint: .vaultAccent, filled: true)
                        }
                    }
                    Text(subscriptionManager.priceText(for: product))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: VaultSpacing.s)
                Image(systemName: isSelected ? "checkmark.circle.fill" : "circle")
                    .font(.title3)
                    .foregroundStyle(isSelected ? Color.vaultAccent : Color.statusInactive)
                    .accessibilityHidden(true)
            }
            .padding(VaultSpacing.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                Color(.secondarySystemBackground),
                in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
            )
            .overlay {
                RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)
                    .stroke(isSelected ? Color.vaultAccent : Color.clear, lineWidth: 2)
            }
        }
        .buttonStyle(.plain)
        .disabled(subscriptionManager.purchaseInProgress)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(planAccessibilityLabel(title: title, product: product, recommended: recommended))
        .accessibilityAddTraits(isSelected ? [.isSelected] : [])
        .accessibilityHint(L10n.tr("Selects this plan."))
    }

    /// The single, explicit purchase entry point (#70). StoreKit's own
    /// confirmation sheet still follows, so this is the first of two
    /// deliberate steps — never a surprise charge.
    private var subscribeButton: some View {
        Button {
            guard let product = selectedProduct else { return }
            purchase(product)
        } label: {
            HStack(spacing: VaultSpacing.s) {
                Spacer()
                if subscriptionManager.purchaseInProgress {
                    ProgressView()
                        .tint(.white)
                        .accessibilityHidden(true)
                } else {
                    Text(L10n.tr("Subscribe"))
                        .font(.headline)
                }
                Spacer()
            }
        }
        .buttonStyle(.borderedProminent)
        .controlSize(.large)
        .disabled(subscriptionManager.purchaseInProgress || selectedProduct == nil)
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
        guard percent > 0 else { return nil }
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
                alertTitle = L10n.tr("Purchase Failed")
                alertMessage = SyncUserError.from(
                    error: error,
                    fallbackTitle: L10n.tr("Purchase Failed")
                ).userVisibleDescription
                showAlert = true
            }
        }
    }
}

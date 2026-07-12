import Foundation
import Observation
import StoreKit
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "tipjar")

/// One-time, repeatable "contribution" purchases (StoreKit consumables) that
/// unlock nothing — they only let users support development. Fully independent
/// of the Cloud Relay subscription: VaultSync stays completely functional
/// whether or not a contribution is ever made, and a user may contribute as
/// often as they like.
@MainActor
@Observable
final class TipJarManager {

    static let smallProductID = "eu.vaultsync.app.contribution.small"
    static let bigProductID = "eu.vaultsync.app.contribution.big"

    /// Loaded contribution products, ordered cheapest → most expensive so the
    /// UI lists "Small" before "Big" regardless of fetch order.
    private(set) var products: [Product] = []
    private(set) var isLoading = true
    /// The productID currently being purchased, or nil. Drives per-row spinners
    /// and disables the buttons while a purchase is in flight.
    private(set) var purchasingProductID: String?
    /// Set after a successful contribution so the UI can say thank you. The view
    /// calls `acknowledgeThankYou()` once it has shown its message.
    private(set) var didContribute = false
    private(set) var errorMessage: String?
    /// Set when a purchase is deferred (e.g. an Ask to Buy awaiting approval).
    /// Neutral status, not an error — shown so the tap is not silently dropped.
    private(set) var pendingMessage: String?

    @ObservationIgnored nonisolated(unsafe) private var loadTask: Task<Void, Never>?

    init() {
        loadTask = Task { [weak self] in
            await self?.loadProducts()
        }
    }

    deinit {
        loadTask?.cancel()
    }

    func loadProducts() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let fetched = try await Product.products(for: [Self.smallProductID, Self.bigProductID])
            products = fetched.sorted { $0.price < $1.price }
            if fetched.isEmpty {
                logger.warning("No contribution products returned by StoreKit")
            }
        } catch {
            logger.error("Failed to load contribution products")
        }
    }

    func purchase(_ product: Product) async {
        purchasingProductID = product.id
        errorMessage = nil
        pendingMessage = nil
        defer { purchasingProductID = nil }

        do {
            let result = try await product.purchase()
            switch result {
            case .success(let verification):
                guard case .verified(let transaction) = verification else {
                    logger.warning("Unverified contribution transaction for \(product.id)")
                    return
                }
                // Consumable: there is nothing to unlock, so finishing the
                // transaction IS the fulfillment. (If a contribution arrives
                // later via Transaction.updates — e.g. an approved Ask to Buy —
                // SubscriptionManager's updates loop finishes it as a safety net.)
                await transaction.finish()
                didContribute = true
                logger.info("Contribution completed: \(product.id)")
            case .userCancelled:
                logger.info("Contribution cancelled by user")
            case .pending:
                logger.info("Contribution pending (e.g. Ask to Buy)")
                pendingMessage = L10n.tr("Your contribution is pending approval.")
            @unknown default:
                break
            }
        } catch {
            logger.error("Contribution purchase failed")
            errorMessage = SyncUserError.from(
                error: error,
                fallbackTitle: L10n.tr("Contribution Failed")
            ).userVisibleDescription
        }
    }

    func acknowledgeThankYou() {
        didContribute = false
    }
}

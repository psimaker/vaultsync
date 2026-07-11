import Foundation
import StoreKit
import Testing
@testable import VaultSync

@MainActor
@Suite("Purchase-path UX (#96)")
struct SubscriptionPurchaseUXTests {

    private struct StubError: Error, LocalizedError {
        var errorDescription: String? { "boom" }
    }

    // MARK: - Restore outcome state machine (issue point 3)

    @Test("Restore: sync failure surfaces as .failed with the error message")
    func restoreSyncFailure() async {
        let outcome = await SubscriptionManager.performRestore(
            sync: { throw StubError() },
            refreshIsSubscribed: { false },
            hasUnverifiedRelayEntitlement: { false },
            isUserCancellation: { _ in false }
        )
        #expect(outcome == .failed(message: "boom"))
    }

    @Test("Restore: user-cancelled App Store sign-in stays silent")
    func restoreCancelled() async {
        let outcome = await SubscriptionManager.performRestore(
            sync: { throw StubError() },
            refreshIsSubscribed: { false },
            hasUnverifiedRelayEntitlement: { false },
            isUserCancellation: { _ in true }
        )
        #expect(outcome == .cancelled)
    }

    @Test("Restore: no entitlement after a clean sync is .nothingToRestore, not silence")
    func restoreNothingFound() async {
        let outcome = await SubscriptionManager.performRestore(
            sync: {},
            refreshIsSubscribed: { false },
            hasUnverifiedRelayEntitlement: { false },
            isUserCancellation: { _ in false }
        )
        #expect(outcome == .nothingToRestore)
    }

    @Test("Restore: active entitlement after sync is .restored")
    func restoreFound() async {
        let outcome = await SubscriptionManager.performRestore(
            sync: {},
            refreshIsSubscribed: { true },
            hasUnverifiedRelayEntitlement: { false },
            isUserCancellation: { _ in false }
        )
        #expect(outcome == .restored)
    }

    @Test("Restore: unverified relay entitlement is called out (issue point 4)")
    func restoreUnverified() async {
        let outcome = await SubscriptionManager.performRestore(
            sync: {},
            refreshIsSubscribed: { false },
            hasUnverifiedRelayEntitlement: { true },
            isUserCancellation: { _ in false }
        )
        #expect(outcome == .foundButUnverified)
    }

    @Test("StoreKitError.userCancelled is recognized as user cancellation")
    func userCancellationMapping() {
        #expect(SubscriptionManager.isUserCancellation(StoreKitError.userCancelled))
        #expect(!SubscriptionManager.isUserCancellation(StubError()))
    }

    // MARK: - Product reload gate (issue point 1)

    @Test("Product reload gate retries only when idle and no product is loaded")
    func reloadGate() {
        #expect(SubscriptionManager.shouldReloadProducts(isLoading: false, hasAnyProduct: false))
        #expect(!SubscriptionManager.shouldReloadProducts(isLoading: true, hasAnyProduct: false))
        #expect(!SubscriptionManager.shouldReloadProducts(isLoading: false, hasAnyProduct: true))
        #expect(!SubscriptionManager.shouldReloadProducts(isLoading: true, hasAnyProduct: true))
    }

    // MARK: - Ask-to-Buy pending persistence (issue point 2)

    @Test("Pending-approval flag persists, clears, and defaults to false")
    func pendingApprovalStore() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "pending-approval-96")
        #expect(!PurchasePendingApprovalStore.isPending(defaults: defaults))
        PurchasePendingApprovalStore.setPending(true, defaults: defaults)
        #expect(PurchasePendingApprovalStore.isPending(defaults: defaults))
        PurchasePendingApprovalStore.setPending(false, defaults: defaults)
        #expect(!PurchasePendingApprovalStore.isPending(defaults: defaults))
    }

    // MARK: - APNs remediation copy (issue point 6)

    @Test("APNs remediation no longer points at the notification permission")
    func apnsRemediationCopy() {
        let error = SyncUserError.apnsRegistrationFailed(reason: "test")
        #expect(!error.remediation.contains("Notifications → VaultSync"))
        #expect(error.remediation.contains("Notification permission is not required"))
        // Routing to the apns-not-registered troubleshooting anchor must survive
        // the reword (message still says "push token").
        let url = SyncUserError.troubleshootingURL(for: error)
        #expect(url?.absoluteString.contains("apns-not-registered") == true)
    }
}

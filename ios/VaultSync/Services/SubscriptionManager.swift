import Foundation
import Observation
import StoreKit
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "subscription")

@MainActor
@Observable
final class SubscriptionManager {

    static let monthlyProductID = "eu.vaultsync.app.relay.monthly"
    static let yearlyProductID = "eu.vaultsync.app.relay.yearly"
    static let relayProductIDs: Set<String> = [monthlyProductID, yearlyProductID]

    private(set) var isRelaySubscribed = false
    private(set) var subscriptionExpiryDate: Date?
    /// When the relay subscription originally started (StoreKit
    /// originalPurchaseDate). Drives the A1 reactivation grace period.
    private(set) var subscriptionStartDate: Date?
    private(set) var monthlyProduct: Product?
    private(set) var yearlyProduct: Product?
    private(set) var purchaseInProgress = false
    private(set) var isLoadingProduct = true
    /// True when the last product load settled without yielding any relay
    /// product (App Store/network error or empty response). Drives the retry
    /// UI in SubscribePlanPicker (#96); reset by every new load attempt.
    private(set) var productLoadFailed = false
    /// Ask to Buy: a purchase returned `.pending` and awaits family approval.
    /// Persisted (approval can arrive days later via `Transaction.updates`).
    /// Cleared by a verified relay transaction / active entitlement or by an
    /// explicit user dismiss — a DECLINED request emits no StoreKit event, so
    /// this flag must never disable the purchase surface (#96).
    private(set) var purchasePendingApproval = false
    /// Set when StoreKit reports a relay transaction that fails local
    /// verification — explains an otherwise silent "not subscribed" (#96).
    private(set) var unverifiedRelayTransactionMessage: String?
    private(set) var errorMessage: String?
    private(set) var relayProvisionStatuses: [String: RelayProvisionStatus] = [:]
    private(set) var apnsRegistrationStatus: APNsRegistrationStatus = APNsRegistrationStore.current()
    private(set) var apnsRegistrationSnapshot: APNsRegistrationStore.Snapshot = APNsRegistrationStore.snapshot()
    private(set) var hasAPNsToken = KeychainService.hasAPNsDeviceToken()
    private(set) var relayHealthResult: RelayService.HealthCheckResult?
    private(set) var relayHealthCheckInFlight = false
    private(set) var relayServerObservations: [String: RelayServerObservation] = [:]
    private(set) var relayStatusFailures: [String: RelayStatusCheckFailure] = [:]
    private(set) var relayStatusCheckInFlight = false
    private(set) var lastRelayTriggerReceivedAt: Date?
    /// Wake-ups that actually arrived in the trailing 7 days (local count, see
    /// `RelayTriggerStore.receivedCount`). Surfaces how much iOS lets through.
    private(set) var relayWakeupsLast7Days: Int = 0
    private(set) var lastRelayError: RelayService.RecordedRelayError?
    /// Whether iOS will actually present an alert banner (authorized + banners
    /// enabled), denied, or unknown. Informational only — silent pushes (Cloud
    /// Relay wake-ups) do not depend on it, so this must NOT feed any relay/APNs
    /// "failure" state.
    private(set) var alertBannerStatus: BackgroundSyncService.AlertBannerStatus = .unknown

    var relayProvisioningNeedsStoreKitVerification: Bool {
        relayProvisionStatuses.values.contains(.storeKitVerificationRequired) ||
            relayStatusFailures.values.contains(.verificationRequired) ||
            unverifiedRelayTransactionMessage != nil
    }

    var relayProvisioningUpdateInProgress: Bool {
        relayProvisionStatuses.values.contains(.migrationRequired) ||
            relayProvisionStatuses.values.contains(.inProgress)
    }

    var relayProvisioningTemporarilyFailed: Bool {
        relayProvisionStatuses.values.contains { status in
            if case .temporarilyFailed = status { return true }
            return false
        }
    }

    var relayStatusPollViewState: RelayStatusPollViewState {
        RelayStatusPollViewState(
            isSubscriptionActive: isRelaySubscribed,
            lastWakeUpReceivedAt: lastRelayTriggerReceivedAt
        )
    }

    var relayBackgroundSyncStartedAt: Date? {
        RelaySyncProofStore.backgroundSyncStartedAt()
    }

    var relaySyncProgressObservedAt: Date? {
        RelaySyncProofStore.syncProgressObservedAt()
    }

    /// Strong signal: a recent silent-push trigger proves Cloud Relay is
    /// actually delivering wake-ups to THIS device (the only leg that proves
    /// end-to-end delivery to this device's token). Deliberately independent of
    /// alert-banner authorization, so muting conflict banners never reads as
    /// "relay broken".
    var relayDeliveryConfirmed: Bool {
        guard isRelaySubscribed, hasAPNsToken,
              let last = lastRelayTriggerReceivedAt else {
            return false
        }
        return Date().timeIntervalSince(last) < Self.relayTriggerFreshnessWindow
    }

    /// Weaker signal: subscribed, provisioned, and the relay endpoint is
    /// reachable — but no recent trigger has proven delivery to this device yet.
    /// Use for a "looks reachable" indicator, NOT a definitive "delivering" one.
    var relayDeliveryLikelyWorking: Bool {
        if relayDeliveryConfirmed { return true }
        guard isRelaySubscribed, hasAPNsToken,
              relayProvisionStatuses.values.contains(where: \.isProvisionedWithVerifiedEntitlement) else {
            return false
        }
        return relayHealthResult?.isHealthy ?? false
    }

    /// A1 — the reactivation signal: subscribed, but NO real wake-up has ever
    /// reached this device (not even a stale one), and the subscription is old
    /// enough that the buyer isn't simply mid-setup. Targets the already-paying-
    /// but-never-activated cohort (the "dead" subs). Keyed on the REAL trigger
    /// timestamp (`lastRelayTriggerReceivedAt`), NOT the self-test key — so
    /// running a self-test never dismisses it (a self-test ≠ the helper actually
    /// delivering real changes; see K3/K5).
    var needsRelayReactivation: Bool {
        guard isRelaySubscribed, lastRelayTriggerReceivedAt == nil else { return false }
        guard let start = subscriptionStartDate else { return false }
        return Date().timeIntervalSince(start) > Self.reactivationGracePeriod
    }

    /// Grace before nagging a fresh buyer who may still be setting up. Tunable;
    /// in DEBUG it can be overridden (including 0 for demos) via the
    /// `RELAY_REACTIVATION_GRACE_SECONDS` UserDefault / launch argument.
    static var reactivationGracePeriod: TimeInterval {
        #if DEBUG
        if UserDefaults.standard.object(forKey: "RELAY_REACTIVATION_GRACE_SECONDS") != nil {
            return UserDefaults.standard.double(forKey: "RELAY_REACTIVATION_GRACE_SECONDS")
        }
        #endif
        return 6 * 60 * 60
    }

    private static let relayTriggerFreshnessWindow: TimeInterval = 48 * 60 * 60

    /// Localized "price / period" for any relay product, derived entirely from
    /// StoreKit so it is correct in every storefront — e.g. "1,99 € / month" or
    /// "14,99 € / year". Falls back to the bare localized price if the period is
    /// unavailable. Never hard-code a currency or amount in the UI.
    func priceText(for product: Product) -> String {
        guard let period = product.subscription?.subscriptionPeriod else {
            return product.displayPrice
        }
        let unit: String
        switch period.unit {
        case .day:
            unit = period.value == 1 ? L10n.tr("day") : L10n.tr("days")
        case .week:
            unit = period.value == 1 ? L10n.tr("week") : L10n.tr("weeks")
        case .month:
            unit = period.value == 1 ? L10n.tr("month") : L10n.tr("months")
        case .year:
            unit = period.value == 1 ? L10n.tr("year") : L10n.tr("years")
        @unknown default:
            return product.displayPrice
        }
        if period.value == 1 {
            return L10n.fmt("%@ / %@", product.displayPrice, unit)
        }
        return L10n.fmt("%1$@ / %2$d %3$@", product.displayPrice, period.value, unit)
    }

    @ObservationIgnored nonisolated(unsafe) private var loadTask: Task<Void, Never>?
    @ObservationIgnored nonisolated(unsafe) private var unfinishedTask: Task<Void, Never>?
    @ObservationIgnored nonisolated(unsafe) private var updatesTask: Task<Void, Never>?
    @ObservationIgnored nonisolated(unsafe) private var apnsObserver: NSObjectProtocol?
    @ObservationIgnored nonisolated(unsafe) private var triggerObserver: NSObjectProtocol?
    @ObservationIgnored nonisolated(unsafe) private var relayDiagnosticsObserver: NSObjectProtocol?
    @ObservationIgnored nonisolated(unsafe) private var tokenChangeObserver: NSObjectProtocol?
    @ObservationIgnored private var relayEntitlementAvailability: RelayEntitlementAvailability = .inactive
    @ObservationIgnored private var reprovisioningInFlight = false
    @ObservationIgnored private var pendingReprovisionRequests: [(RelayReprovisionTrigger, [String])] = []
    @ObservationIgnored private let relayStatusCheckGate = RelayStatusCheckGate()
    @ObservationIgnored private var relayStatusCacheGeneration = 0

    init() {
        apnsRegistrationStatus = APNsRegistrationStore.current()
        apnsRegistrationSnapshot = APNsRegistrationStore.snapshot()
        refreshStoredRelayDiagnostics()
        // Pre-v2 local success flags are historical evidence only. They become
        // migration-required until this app receives a successful relay response
        // backed by a currently verified StoreKit signed transaction.
        relayProvisionStatuses = RelayProvisionStatusStore.load()
        purchasePendingApproval = PurchasePendingApprovalStore.isPending()

        apnsObserver = NotificationCenter.default.addObserver(
            forName: APNsRegistrationStore.statusDidChangeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.refreshAPNsRegistrationStatus()
            }
        }
        
        triggerObserver = NotificationCenter.default.addObserver(
            forName: RelayTriggerStore.triggerDidChangeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.refreshStoredRelayDiagnostics()
            }
        }

        relayDiagnosticsObserver = NotificationCenter.default.addObserver(
            forName: RelayService.diagnosticsDidChangeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.refreshStoredRelayDiagnostics()
            }
        }

        tokenChangeObserver = NotificationCenter.default.addObserver(
            forName: APNsRegistrationStore.tokenDidChangeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                await self?.reprovisionOnTokenChange()
            }
        }

        loadTask = Task(priority: .background) {
            await loadProduct()
        }
        unfinishedTask = Task(priority: .background) {
            for await verificationResult in Transaction.unfinished {
                await handle(verificationResult, trigger: .purchase)
            }
            await checkSubscriptionStatus()
            await ensureProvisioningIfNeeded()
        }
        updatesTask = Task(priority: .background) {
            for await verificationResult in Transaction.updates {
                await handle(verificationResult, trigger: .renewal)
            }
        }
    }

    deinit {
        loadTask?.cancel()
        unfinishedTask?.cancel()
        updatesTask?.cancel()
        if let apnsObserver {
            NotificationCenter.default.removeObserver(apnsObserver)
        }
        if let triggerObserver {
            NotificationCenter.default.removeObserver(triggerObserver)
        }
        if let relayDiagnosticsObserver {
            NotificationCenter.default.removeObserver(relayDiagnosticsObserver)
        }
        if let tokenChangeObserver {
            NotificationCenter.default.removeObserver(tokenChangeObserver)
        }
    }

    // MARK: - Public

    func checkSubscriptionStatus() async {
        var activeEntitlement: (
            transaction: Transaction,
            proof: RelayVerifiedEntitlement,
            expiry: Date
        )?
        var foundUnverifiedRelay = false
        for await verificationResult in Transaction.currentEntitlements {
            guard case .verified(let transaction) = verificationResult else {
                if case .unverified(let unverified, _) = verificationResult,
                   Self.relayProductIDs.contains(unverified.productID) {
                    foundUnverifiedRelay = true
                }
                continue
            }
            guard Self.relayProductIDs.contains(transaction.productID),
                  transaction.revocationDate == nil,
                  let expiry = transaction.expirationDate,
                  expiry > Date(),
                  let proof = RelayVerifiedEntitlement(
                    signedTransaction: verificationResult.jwsRepresentation
                  ) else { continue }

            if let current = activeEntitlement, expiry <= current.expiry { continue }
            activeEntitlement = (transaction, proof, expiry)
        }
        isRelaySubscribed = activeEntitlement != nil
        if let activeEntitlement {
            subscriptionExpiryDate = activeEntitlement.expiry
            subscriptionStartDate = activeEntitlement.transaction.originalPurchaseDate
            relayEntitlementAvailability = .verified(activeEntitlement.proof)
            // A verified active entitlement settles any pending Ask-to-Buy and
            // disproves a verification problem.
            setPendingApproval(false)
            unverifiedRelayTransactionMessage = nil
        } else {
            subscriptionExpiryDate = nil
            subscriptionStartDate = nil
            unverifiedRelayTransactionMessage = foundUnverifiedRelay ? Self.unverifiedRelayMessage() : nil
            relayEntitlementAvailability = foundUnverifiedRelay ? .verificationRequired : .inactive
            resetRelayStatusEvidence()
            if foundUnverifiedRelay {
                markStoreKitVerificationRequired(for: allKnownDeviceIDs())
            }
        }
        refreshAPNsRegistrationStatus()
        refreshStoredRelayDiagnostics()
        logger.info("Subscription status: \(self.isRelaySubscribed ? "active" : "inactive")")
    }

    /// Purchase a relay subscription product (monthly or yearly). Pass all peer
    /// device IDs from SyncthingManager so they can be provisioned with the
    /// relay after purchase.
    func purchase(_ product: Product, homeserverDeviceIDs: [String]) async throws {
        purchaseInProgress = true
        errorMessage = nil
        ensureProvisionStateEntries(for: homeserverDeviceIDs)
        defer { purchaseInProgress = false }

        let result = try await product.purchase()

        switch result {
        case .success(let verificationResult):
            await handle(
                verificationResult,
                provisionDeviceIDs: homeserverDeviceIDs,
                trigger: .purchase
            )
        case .userCancelled:
            logger.info("User cancelled purchase")
        case .pending:
            logger.info("Purchase pending (e.g. Ask to Buy)")
            setPendingApproval(true)
            // Store device IDs for later provisioning when transaction completes
            storeDeviceIDs(homeserverDeviceIDs)
            for deviceID in homeserverDeviceIDs {
                relayProvisionStatuses[deviceID] = .notAttempted
            }
        @unknown default:
            logger.warning("Unknown purchase result")
        }
    }

    enum RestoreOutcome: Equatable {
        case restored
        case nothingToRestore
        case foundButUnverified
        case cancelled
        case failed(message: String)
    }

    @discardableResult
    func restorePurchases(homeserverDeviceIDs: [String] = []) async -> RestoreOutcome {
        if !homeserverDeviceIDs.isEmpty {
            storeDeviceIDs(homeserverDeviceIDs)
            ensureProvisionStateEntries(for: homeserverDeviceIDs)
        }
        let outcome = await Self.performRestore(
            sync: { try await AppStore.sync() },
            refreshIsSubscribed: { [weak self] in
                await self?.checkSubscriptionStatus()
                return self?.isRelaySubscribed ?? false
            },
            hasUnverifiedRelayEntitlement: { [weak self] in
                self?.unverifiedRelayTransactionMessage != nil
            },
            isUserCancellation: Self.isUserCancellation
        )
        if outcome == .restored {
            await requestReprovisioning(
                trigger: .restore,
                deviceIDs: allKnownDeviceIDs(including: homeserverDeviceIDs)
            )
        }
        return outcome
    }

    /// Pure restore state machine (injected effects — PathCollisionGuard
    /// pattern): sync errors surface instead of being swallowed by `try?`,
    /// and "nothing found" / "found but unverified" are distinguished from
    /// "restored" so the picker can say so (#96).
    static func performRestore(
        sync: () async throws -> Void,
        refreshIsSubscribed: () async -> Bool,
        hasUnverifiedRelayEntitlement: () -> Bool,
        isUserCancellation: (Error) -> Bool
    ) async -> RestoreOutcome {
        do {
            try await sync()
        } catch {
            // User dismissed the App Store sign-in sheet — not an error state.
            if isUserCancellation(error) { return .cancelled }
            return .failed(message: error.localizedDescription)
        }
        if await refreshIsSubscribed() { return .restored }
        return hasUnverifiedRelayEntitlement() ? .foundButUnverified : .nothingToRestore
    }

    nonisolated static func isUserCancellation(_ error: Error) -> Bool {
        if case StoreKitError.userCancelled = error { return true }
        return false
    }

    /// Re-runs the App Store product query. No-op while a load is in flight or
    /// once any product is present — safe to call from every diagnostics
    /// refresh and from the picker's retry button (#96).
    func reloadProductsIfNeeded() async {
        guard Self.shouldReloadProducts(
            isLoading: isLoadingProduct,
            hasAnyProduct: monthlyProduct != nil || yearlyProduct != nil
        ) else { return }
        await loadProduct()
    }

    /// Pure retry gate, extracted for unit tests (StoreKit `Product` cannot be
    /// constructed in tests).
    nonisolated static func shouldReloadProducts(isLoading: Bool, hasAnyProduct: Bool) -> Bool {
        !isLoading && !hasAnyProduct
    }

    /// User-invoked dismiss of the Ask-to-Buy banner (e.g. after a decline,
    /// which StoreKit never reports). Never called automatically.
    func clearPendingApproval() {
        setPendingApproval(false)
    }

    nonisolated static func unverifiedRelayMessage() -> String {
        L10n.tr("Your purchase must be confirmed again.")
    }

    private func setPendingApproval(_ pending: Bool) {
        purchasePendingApproval = pending
        PurchasePendingApprovalStore.setPending(pending)
    }

    func refreshRelayDiagnostics(homeserverDeviceIDs: [String]) async {
        let allDeviceIDs = homeserverDeviceIDs.isEmpty ? loadStoredDeviceIDs() : homeserverDeviceIDs
        ensureProvisionStateEntries(for: allDeviceIDs)
        if !allDeviceIDs.isEmpty {
            storeDeviceIDs(allDeviceIDs)
        }
        refreshAPNsRegistrationStatus()
        refreshStoredRelayDiagnostics()
        // One failed launch-time product load must not kill the buy button for
        // the whole session (#96): every diagnostics refresh (Relay-tab .task)
        // retries while no product is loaded. Idempotent via the reload gate.
        await reloadProductsIfNeeded()
        alertBannerStatus = await BackgroundSyncService.alertBannerStatus()
        await checkSubscriptionStatus()
        await runRelayHealthCheck()
        // Opportunistically re-provision if the last successful provision is
        // older than the refresh interval. Covers the case where the relay DB
        // was reset (e.g. from token self-healing) but the app still thinks
        // it's provisioned and won't trigger re-provision until 24h passed.
        await ensureProvisioningIfNeeded()
    }

    func runRelayHealthCheck(timeout: TimeInterval = 6) async {
        relayHealthCheckInFlight = true
        defer { relayHealthCheckInFlight = false }
        relayHealthResult = await RelayService.checkHealthResult(timeout: timeout)
        refreshStoredRelayDiagnostics()
    }

    @discardableResult
    func checkRelayObservationStatus(
        homeserverDeviceIDs: [String]
    ) async -> RelayStatusCheckOutcome {
        guard relayStatusCheckGate.begin() else {
            return currentRelayStatusOutcome()
        }
        relayStatusCheckInFlight = true
        let startedAtGeneration = relayStatusCacheGeneration
        defer {
            relayStatusCheckInFlight = false
            relayStatusCheckGate.end()
        }
        let outcome = await RelayStatusChecking.run(
            deviceIDs: homeserverDeviceIDs,
            provisionStatuses: relayProvisionStatuses,
            initialObservations: relayServerObservations,
            initialFailures: relayStatusFailures,
            isSubscriptionActive: isRelaySubscribed,
            entitlement: relayEntitlementAvailability,
            fetch: { deviceID, signedTransaction in
                try await RelayService.fetchStatus(
                    deviceID: deviceID,
                    signedTransaction: signedTransaction
                )
            }
        )
        let hasVerifiedEntitlement: Bool
        if case .verified = relayEntitlementAvailability {
            hasVerifiedEntitlement = true
        } else {
            hasVerifiedEntitlement = false
        }
        guard RelayStatusCacheLifecycle.shouldApplyOutcome(
            startedAtGeneration: startedAtGeneration,
            currentGeneration: relayStatusCacheGeneration,
            isSubscriptionActive: isRelaySubscribed,
            hasVerifiedEntitlement: hasVerifiedEntitlement,
            isCancelled: Task.isCancelled
        ) else {
            return currentRelayStatusOutcome()
        }
        relayServerObservations = outcome.observations
        relayStatusFailures = outcome.failures
        return outcome
    }

    /// Called only by RelayHomeView and RelayDiagnosticsView. The first check is
    /// immediate, retries are finite, and view-task cancellation stops the loop.
    func pollRelayObservationStatus(
        homeserverDeviceIDs: [String],
        context: RelayStatusPollingContext,
        policy: RelayStatusPollingPolicy = .waitingView
    ) async {
        guard context.allowsStatusPolling else { return }
        let deviceIDs = Array(Set(homeserverDeviceIDs)).sorted().filter {
            relayProvisionStatuses[$0]?.isProvisionedWithVerifiedEntitlement == true
        }
        guard !deviceIDs.isEmpty else { return }
        let wakeUpAtStart = RelayTriggerStore.lastReceivedAt()
        await RelayStatusPolling.run(
            policy: policy,
            check: { [weak self] in
                guard let self else {
                    return RelayStatusCheckOutcome(
                        observations: [:], failures: [:], requestedDeviceIDs: []
                    )
                }
                return await self.checkRelayObservationStatus(homeserverDeviceIDs: deviceIDs)
            },
            shouldContinue: { [weak self] in
                guard let self, self.isRelaySubscribed,
                      case .verified = self.relayEntitlementAvailability,
                      !self.relayDeliveryConfirmed else {
                    return false
                }
                return RelayTriggerStore.lastReceivedAt() == wakeUpAtStart
            }
        )
    }

    func relayUserStatus(homeserverDeviceIDs: [String], now: Date = Date()) -> RelayUserStatus {
        let statuses = Array(Set(homeserverDeviceIDs)).map { deviceID in
            RelayStatusPresentation.status(
                observation: relayServerObservations[deviceID],
                failure: relayStatusFailures[deviceID],
                localWakeUpReceivedAt: lastRelayTriggerReceivedAt,
                now: now
            )
        }
        let priority: [RelayUserStatus] = [
            .wakeUpReceived,
            .relayObservedWaitingForWakeUp,
            .relayObservedWithinGrace,
            .quietCanBeNormal,
            .waitingForFirstSignal,
            .statusUnavailable,
            .checking,
        ]
        return priority.first(where: { statuses.contains($0) }) ?? .checking
    }

    func relayProofSnapshot(for deviceID: String) -> RelayProofSnapshot {
        let entitlementVerified: Bool
        if case .verified = relayEntitlementAvailability {
            entitlementVerified = true
        } else {
            entitlementVerified = false
        }
        return RelayProofSnapshot(
            storeKitEntitlementVerified: entitlementVerified,
            relayProvisioningConfirmed:
                relayProvisionStatuses[deviceID]?.isProvisionedWithVerifiedEntitlement == true,
            relayBackendReachable: relayHealthResult?.isHealthy == true,
            relayTriggerObservedAt: relayServerObservations[deviceID]?.lastTriggerObservedAt,
            silentPushReceivedAt: lastRelayTriggerReceivedAt,
            backgroundSyncStartedAt: RelaySyncProofStore.backgroundSyncStartedAt(),
            syncProgressObservedAt: RelaySyncProofStore.syncProgressObservedAt()
        )
    }

    func retryRelayProvisioning(homeserverDeviceIDs: [String]) async {
        let allDeviceIDs = homeserverDeviceIDs.isEmpty ? loadStoredDeviceIDs() : homeserverDeviceIDs
        ensureProvisionStateEntries(for: allDeviceIDs)
        if !allDeviceIDs.isEmpty {
            storeDeviceIDs(allDeviceIDs)
        }

        await checkSubscriptionStatus()
        guard isRelaySubscribed else {
            if relayEntitlementAvailability == .verificationRequired {
                markStoreKitVerificationRequired(for: allDeviceIDs)
                errorMessage = L10n.tr("Your purchase must be confirmed again.")
            } else {
                errorMessage = L10n.tr("Cloud Relay is not subscribed. Start a subscription first.")
            }
            return
        }

        await requestReprovisioning(trigger: .manualRetry, deviceIDs: allDeviceIDs)
    }

    // MARK: - Private

    private func loadProduct() async {
        isLoadingProduct = true
        productLoadFailed = false
        defer { isLoadingProduct = false }
        do {
            let products = try await Product.products(for: Array(Self.relayProductIDs))
            monthlyProduct = products.first { $0.id == Self.monthlyProductID }
            yearlyProduct = products.first { $0.id == Self.yearlyProductID }
            productLoadFailed = monthlyProduct == nil && yearlyProduct == nil
            if productLoadFailed {
                logger.warning("Relay subscription products not found in App Store")
            }
        } catch {
            productLoadFailed = true
            logger.error("Failed to load products: \(error)")
        }
    }

    private func handle(
        _ verificationResult: VerificationResult<Transaction>,
        provisionDeviceIDs: [String]? = nil,
        trigger: RelayReprovisionTrigger
    ) async {
        guard case .verified(let transaction) = verificationResult else {
            // Still never finished, still never grants entitlement — but a
            // relay purchase that fails verification must not read as a silent
            // "not subscribed" (#96). VerificationResult.Error carries no user
            // data, so it is safe to log.
            if case .unverified(let unverified, _) = verificationResult,
               Self.relayProductIDs.contains(unverified.productID) {
                logger.warning("Unverified relay transaction, skipping")
                unverifiedRelayTransactionMessage = Self.unverifiedRelayMessage()
                relayEntitlementAvailability = .verificationRequired
                markStoreKitVerificationRequired(
                    for: allKnownDeviceIDs(including: provisionDeviceIDs ?? [])
                )
            } else {
                logger.warning("Unverified transaction, skipping")
            }
            return
        }

        if Self.relayProductIDs.contains(transaction.productID) {
            // Any verified relay transaction settles a pending Ask-to-Buy and
            // disproves a verification problem.
            setPendingApproval(false)
            unverifiedRelayTransactionMessage = nil
            if let revocationDate = transaction.revocationDate {
                logger.info("Subscription revoked on \(revocationDate)")
                isRelaySubscribed = false
                subscriptionExpiryDate = nil
                relayEntitlementAvailability = .inactive
                await deprovisionRelay()
            } else if let expirationDate = transaction.expirationDate, expirationDate < Date() {
                logger.info("Subscription expired")
                isRelaySubscribed = false
                subscriptionExpiryDate = nil
                relayEntitlementAvailability = .inactive
                await deprovisionRelay()
            } else if let expirationDate = transaction.expirationDate,
                      expirationDate > Date(),
                      let proof = RelayVerifiedEntitlement(
                        signedTransaction: verificationResult.jwsRepresentation
                      ) {
                isRelaySubscribed = true
                subscriptionExpiryDate = transaction.expirationDate
                subscriptionStartDate = transaction.originalPurchaseDate
                relayEntitlementAvailability = .verified(proof)

                // Use explicitly passed device IDs (from purchase flow) or stored ones (from renewal)
                let deviceIDs = provisionDeviceIDs ?? loadStoredDeviceIDs()
                if let ids = provisionDeviceIDs {
                    storeDeviceIDs(ids)
                }
                ensureProvisionStateEntries(for: deviceIDs)
                await requestReprovisioning(trigger: trigger, deviceIDs: deviceIDs)
            } else {
                relayEntitlementAvailability = .verificationRequired
                markStoreKitVerificationRequired(
                    for: allKnownDeviceIDs(including: provisionDeviceIDs ?? [])
                )
            }
        }

        await transaction.finish()
    }

    private func requestReprovisioning(
        trigger: RelayReprovisionTrigger,
        deviceIDs: [String]
    ) async {
        let allDeviceIDs = allKnownDeviceIDs(including: deviceIDs)
        guard !allDeviceIDs.isEmpty else {
            logger.info("No homeserver device IDs available, skipping relay provision")
            return
        }
        ensureProvisionStateEntries(for: allDeviceIDs)
        pendingReprovisionRequests.append((trigger, allDeviceIDs))
        guard !reprovisioningInFlight else { return }

        reprovisioningInFlight = true
        defer { reprovisioningInFlight = false }
        while !pendingReprovisionRequests.isEmpty {
            let request = pendingReprovisionRequests.removeFirst()
            await performReprovisioning(trigger: request.0, deviceIDs: request.1)
        }
    }

    private func performReprovisioning(
        trigger: RelayReprovisionTrigger,
        deviceIDs: [String]
    ) async {
        refreshAPNsRegistrationStatus()
        let outcome = await RelayReprovisioning.run(
            trigger: trigger,
            deviceIDs: deviceIDs,
            statuses: relayProvisionStatuses,
            entitlement: relayEntitlementAvailability,
            apnsToken: KeychainService.getAPNsDeviceToken(),
            isSubscriptionActive: isRelaySubscribed,
            provision: { deviceID, token, signedTransaction in
                try await RelayService.provision(
                    deviceID: deviceID,
                    apnsToken: token,
                    signedTransaction: signedTransaction
                )
            },
            stateDidChange: { [weak self] statuses in
                guard let self else { return }
                self.relayProvisionStatuses = statuses
                RelayProvisionStatusStore.save(statuses)
            }
        )
        relayProvisionStatuses = outcome.statuses
        RelayProvisionStatusStore.save(relayProvisionStatuses)

        let failed = outcome.statuses.values.compactMap(\.failureReason)
        if let reason = failed.first {
            logger.error("Relay provisioning update failed")
            errorMessage = RelayService.userError(
                from: RelayService.RelayError.provisionFailed(reason: reason)
            ).userVisibleDescription
        } else if !outcome.provisionedDeviceIDs.isEmpty {
            errorMessage = nil
        }

        if allKnownDeviceIDs().allSatisfy({
            relayProvisionStatuses[$0]?.isProvisionedWithVerifiedEntitlement == true
        }) {
            UserDefaults.standard.set(Date(), forKey: Self.lastProvisionDateKey)
        }
        refreshStoredRelayDiagnostics()
    }

    private func deprovisionRelay() async {
        // A verified revoke/expiry is authoritative. Clear local provision state
        // before the token guard so a later cold launch cannot claim a verified
        // registration when the matching entitlement is no longer active.
        let deviceIDs = loadStoredDeviceIDs()
        ensureProvisionStateEntries(for: deviceIDs)
        // Sweep every known status, not just the Keychain-stored IDs. Pairing,
        // folders, paths, and the APNs token itself are deliberately untouched.
        for deviceID in relayProvisionStatuses.keys {
            relayProvisionStatuses[deviceID] = .notAttempted
        }
        RelayProvisionStatusStore.save(
            relayProvisionStatuses,
            preserveLegacyProvisionedIDs: false
        )
        resetRelayStatusEvidence()

        // Clear the per-activation "Connected" celebration on every transition to
        // inactive, so a later resubscribe celebrates again. Done before the token
        // guard so it still clears when no token exists.
        UserDefaults.standard.removeObject(forKey: "relay-connected-celebrated")
        guard let token = KeychainService.getAPNsDeviceToken() else {
            refreshStoredRelayDiagnostics()
            return
        }

        for deviceID in deviceIDs {
            do {
                try await RelayService.deprovision(deviceID: deviceID, apnsToken: token)
                relayProvisionStatuses[deviceID] = .notAttempted
            } catch {
                logger.error("Failed to deprovision relay registration: \(error)")
                let userError = RelayService.userError(from: error)
                relayProvisionStatuses[deviceID] = .temporarilyFailed(reason: userError.message)
                errorMessage = userError.userVisibleDescription
            }
        }
        RelayProvisionStatusStore.save(
            relayProvisionStatuses,
            preserveLegacyProvisionedIDs: false
        )
        refreshStoredRelayDiagnostics()
    }

    // MARK: - Token re-provisioning

    private func reprovisionOnTokenChange() async {
        guard isRelaySubscribed else { return }
        guard KeychainService.hasAPNsDeviceToken() else { return }

        let deviceIDs = loadStoredDeviceIDs()
        guard !deviceIDs.isEmpty else { return }

        logger.info("APNs token changed, re-provisioning relay for \(deviceIDs.count) device(s)")
        await requestReprovisioning(trigger: .tokenRotation, deviceIDs: deviceIDs)
    }

    /// Run pending per-device migration on launch, then refresh fully verified
    /// registrations on the existing six-hour recovery cadence.
    private func ensureProvisioningIfNeeded() async {
        guard isRelaySubscribed else { return }
        guard KeychainService.hasAPNsDeviceToken() else { return }

        let deviceIDs = loadStoredDeviceIDs()
        guard !deviceIDs.isEmpty else { return }

        // The one-time app-update migration is per homeserver and independent of
        // the periodic refresh timestamp. Failed devices remain eligible on the
        // next launch/diagnostics pass; verified successes are skipped.
        markMigrationRequiredForActiveSubscription(deviceIDs: deviceIDs)
        await requestReprovisioning(trigger: .appUpdate, deviceIDs: deviceIDs)
        guard deviceIDs.allSatisfy({
            relayProvisionStatuses[$0]?.isProvisionedWithVerifiedEntitlement == true
        }) else { return }

        let lastProvision = UserDefaults.standard.object(forKey: Self.lastProvisionDateKey) as? Date
        if let lastProvision, Date().timeIntervalSince(lastProvision) < Self.provisionRefreshInterval {
            return
        }

        logger.info("Periodic relay re-provision (last: \(lastProvision?.description ?? "never"))")
        await requestReprovisioning(trigger: .periodicRefresh, deviceIDs: deviceIDs)
    }

    private static let lastProvisionDateKey = "relay-last-provision-date"
    // Re-provision every 6h instead of 24h so a relay DB reset (e.g. from
    // BadDeviceToken self-healing or a stale cache) recovers within a
    // reasonable window without waiting a full day for push delivery to
    // resume. The relay's /provision endpoint is idempotent, so this is cheap.
    private static let provisionRefreshInterval: TimeInterval = 6 * 60 * 60

    // MARK: - Device ID Storage

    private func storeDeviceIDs(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        let merged = Array(Set(loadStoredDeviceIDs() + ids)).sorted()
        if let data = try? JSONEncoder().encode(merged) {
            KeychainService.set(key: "relay-device-ids", value: String(data: data, encoding: .utf8) ?? "[]")
        }
    }

    private func loadStoredDeviceIDs() -> [String] {
        guard let stored = KeychainService.get(key: "relay-device-ids") else { return [] }
        // Support both legacy comma-separated and new JSON array format
        if stored.hasPrefix("[") {
            return (try? JSONDecoder().decode([String].self, from: Data(stored.utf8))) ?? []
        }
        return stored.components(separatedBy: ",").filter { !$0.isEmpty }
    }

    private func ensureProvisionStateEntries(for deviceIDs: [String]) {
        for deviceID in deviceIDs where relayProvisionStatuses[deviceID] == nil {
            relayProvisionStatuses[deviceID] = .notAttempted
        }
        RelayProvisionStatusStore.save(relayProvisionStatuses)
    }

    private func allKnownDeviceIDs(including deviceIDs: [String] = []) -> [String] {
        Array(Set(deviceIDs + loadStoredDeviceIDs() + Array(relayProvisionStatuses.keys))).sorted()
    }

    private func markMigrationRequiredForActiveSubscription(deviceIDs: [String]) {
        guard isRelaySubscribed else { return }
        for deviceID in deviceIDs {
            let status = relayProvisionStatuses[deviceID] ?? .notAttempted
            if status.needsVerifiedProvisioning {
                relayProvisionStatuses[deviceID] = .migrationRequired
            }
        }
        RelayProvisionStatusStore.save(relayProvisionStatuses)
    }

    private func markStoreKitVerificationRequired(for deviceIDs: [String]) {
        guard !deviceIDs.isEmpty else { return }
        for deviceID in deviceIDs {
            relayProvisionStatuses[deviceID] = .storeKitVerificationRequired
        }
        RelayProvisionStatusStore.save(relayProvisionStatuses)
    }

    private func currentRelayStatusOutcome() -> RelayStatusCheckOutcome {
        RelayStatusCheckOutcome(
            observations: relayServerObservations,
            failures: relayStatusFailures,
            requestedDeviceIDs: []
        )
    }

    private func resetRelayStatusEvidence() {
        let reset = RelayStatusCacheLifecycle.reset(currentGeneration: relayStatusCacheGeneration)
        relayServerObservations = reset.observations
        relayStatusFailures = reset.failures
        relayStatusCacheGeneration = reset.generation
    }

    private func refreshAPNsRegistrationStatus() {
        apnsRegistrationStatus = APNsRegistrationStore.current()
        apnsRegistrationSnapshot = APNsRegistrationStore.snapshot()
        hasAPNsToken = KeychainService.hasAPNsDeviceToken()
    }

    private func refreshStoredRelayDiagnostics() {
        hasAPNsToken = KeychainService.hasAPNsDeviceToken()
        lastRelayTriggerReceivedAt = RelayTriggerStore.lastReceivedAt()
        relayWakeupsLast7Days = RelayTriggerStore.receivedCount(within: 7 * 24 * 60 * 60)
        lastRelayError = RelayService.lastRecordedError()
    }

}

/// Persists the Ask-to-Buy "waiting for approval" flag across launches (#96).
/// Injectable defaults for tests (TestSupport.makeIsolatedDefaults).
enum PurchasePendingApprovalStore {
    static let key = "relay-purchase-pending-approval"

    static func isPending(defaults: UserDefaults = .standard) -> Bool {
        defaults.bool(forKey: key)
    }

    static func setPending(_ pending: Bool, defaults: UserDefaults = .standard) {
        defaults.set(pending, forKey: key)
    }
}

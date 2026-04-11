import Foundation
import Observation
import StoreKit
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "subscription")

@MainActor
@Observable
final class SubscriptionManager {

    static let relayProductID = "eu.vaultsync.app.relay.monthly"

    private(set) var isRelaySubscribed = false
    private(set) var subscriptionExpiryDate: Date?
    private(set) var availableProduct: Product?
    private(set) var purchaseInProgress = false
    private(set) var isLoadingProduct = true
    private(set) var errorMessage: String?
    private(set) var relayProvisionStatuses: [String: RelayProvisionStatus] = [:]
    private(set) var apnsRegistrationStatus: APNsRegistrationStatus = APNsRegistrationStore.current()
    private(set) var apnsRegistrationSnapshot: APNsRegistrationStore.Snapshot = APNsRegistrationStore.snapshot()
    private(set) var hasAPNsToken = KeychainService.hasAPNsDeviceToken()
    private(set) var relayHealthResult: RelayService.HealthCheckResult?
    private(set) var relayHealthCheckInFlight = false
    private(set) var lastRelayTriggerReceivedAt: Date?
    private(set) var lastRelayError: RelayService.RecordedRelayError?

    @ObservationIgnored nonisolated(unsafe) private var loadTask: Task<Void, Never>?
    @ObservationIgnored nonisolated(unsafe) private var unfinishedTask: Task<Void, Never>?
    @ObservationIgnored nonisolated(unsafe) private var updatesTask: Task<Void, Never>?
    @ObservationIgnored nonisolated(unsafe) private var apnsObserver: NSObjectProtocol?
    @ObservationIgnored nonisolated(unsafe) private var triggerObserver: NSObjectProtocol?
    @ObservationIgnored nonisolated(unsafe) private var relayDiagnosticsObserver: NSObjectProtocol?
    @ObservationIgnored nonisolated(unsafe) private var tokenChangeObserver: NSObjectProtocol?

    init() {
        apnsRegistrationStatus = APNsRegistrationStore.current()
        apnsRegistrationSnapshot = APNsRegistrationStore.snapshot()
        refreshStoredRelayDiagnostics()

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
                await handle(verificationResult)
            }
            await checkSubscriptionStatus()
            await ensureProvisioningIfNeeded()
        }
        updatesTask = Task(priority: .background) {
            for await verificationResult in Transaction.updates {
                await handle(verificationResult)
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
        var foundActive = false
        for await verificationResult in Transaction.currentEntitlements {
            guard case .verified(let transaction) = verificationResult else { continue }
            if transaction.productID == Self.relayProductID {
                if let expiry = transaction.expirationDate, expiry > Date() {
                    foundActive = true
                    subscriptionExpiryDate = expiry
                } else if transaction.expirationDate == nil {
                    foundActive = true
                }
            }
        }
        let wasSubscribed = isRelaySubscribed
        isRelaySubscribed = foundActive
        if !foundActive {
            subscriptionExpiryDate = nil
            if wasSubscribed {
                await deprovisionRelay()
            }
            for deviceID in relayProvisionStatuses.keys {
                relayProvisionStatuses[deviceID] = .notAttempted
            }
        }
        refreshAPNsRegistrationStatus()
        refreshStoredRelayDiagnostics()
        logger.info("Subscription status: \(foundActive ? "active" : "inactive")")
    }

    /// Purchase the relay subscription. Pass all peer device IDs from SyncthingManager
    /// so they can be provisioned with the relay after purchase.
    func purchase(homeserverDeviceIDs: [String]) async throws {
        guard let product = availableProduct else {
            logger.error("No product available for purchase")
            errorMessage = "No Cloud Relay product is currently available."
            return
        }

        purchaseInProgress = true
        errorMessage = nil
        ensureProvisionStateEntries(for: homeserverDeviceIDs)
        defer { purchaseInProgress = false }

        let result = try await product.purchase()

        switch result {
        case .success(let verificationResult):
            await handle(verificationResult, provisionDeviceIDs: homeserverDeviceIDs)
        case .userCancelled:
            logger.info("User cancelled purchase")
        case .pending:
            logger.info("Purchase pending (e.g. Ask to Buy)")
            // Store device IDs for later provisioning when transaction completes
            storeDeviceIDs(homeserverDeviceIDs)
            for deviceID in homeserverDeviceIDs {
                relayProvisionStatuses[deviceID] = .notAttempted
            }
        @unknown default:
            logger.warning("Unknown purchase result")
        }
    }

    func restorePurchases() async {
        try? await AppStore.sync()
        await checkSubscriptionStatus()
    }

    func refreshRelayDiagnostics(homeserverDeviceIDs: [String]) async {
        let allDeviceIDs = homeserverDeviceIDs.isEmpty ? loadStoredDeviceIDs() : homeserverDeviceIDs
        ensureProvisionStateEntries(for: allDeviceIDs)
        if !allDeviceIDs.isEmpty {
            storeDeviceIDs(allDeviceIDs)
        }
        refreshAPNsRegistrationStatus()
        refreshStoredRelayDiagnostics()
        await checkSubscriptionStatus()
        await runRelayHealthCheck()
    }

    func runRelayHealthCheck(timeout: TimeInterval = 6) async {
        relayHealthCheckInFlight = true
        defer { relayHealthCheckInFlight = false }
        relayHealthResult = await RelayService.checkHealthResult(timeout: timeout)
        refreshStoredRelayDiagnostics()
    }

    func retryRelayProvisioning(homeserverDeviceIDs: [String]) async {
        let allDeviceIDs = homeserverDeviceIDs.isEmpty ? loadStoredDeviceIDs() : homeserverDeviceIDs
        ensureProvisionStateEntries(for: allDeviceIDs)
        if !allDeviceIDs.isEmpty {
            storeDeviceIDs(allDeviceIDs)
        }

        await checkSubscriptionStatus()
        guard isRelaySubscribed else {
            errorMessage = "Cloud Relay is not subscribed. Start a subscription first."
            return
        }

        let transactionID = await currentRelayTransactionID() ?? "manual-retry"
        await provisionRelay(deviceIDs: allDeviceIDs, transactionID: transactionID)
    }

    // MARK: - Private

    private func loadProduct() async {
        isLoadingProduct = true
        defer { isLoadingProduct = false }
        do {
            let products = try await Product.products(for: [Self.relayProductID])
            availableProduct = products.first
            if availableProduct == nil {
                logger.warning("Relay subscription product not found in App Store")
            }
        } catch {
            logger.error("Failed to load products: \(error)")
        }
    }

    private func handle(
        _ verificationResult: VerificationResult<Transaction>,
        provisionDeviceIDs: [String]? = nil
    ) async {
        guard case .verified(let transaction) = verificationResult else {
            logger.warning("Unverified transaction, skipping")
            return
        }

        if transaction.productID == Self.relayProductID {
            if let revocationDate = transaction.revocationDate {
                logger.info("Subscription revoked on \(revocationDate)")
                isRelaySubscribed = false
                subscriptionExpiryDate = nil
                await deprovisionRelay()
            } else if let expirationDate = transaction.expirationDate, expirationDate < Date() {
                logger.info("Subscription expired")
                isRelaySubscribed = false
                subscriptionExpiryDate = nil
                await deprovisionRelay()
            } else {
                isRelaySubscribed = true
                subscriptionExpiryDate = transaction.expirationDate

                // Use explicitly passed device IDs (from purchase flow) or stored ones (from renewal)
                let deviceIDs = provisionDeviceIDs ?? loadStoredDeviceIDs()
                if let ids = provisionDeviceIDs {
                    storeDeviceIDs(ids)
                }
                ensureProvisionStateEntries(for: deviceIDs)
                await provisionRelay(deviceIDs: deviceIDs, transactionID: String(transaction.originalID))
            }
        }

        await transaction.finish()
    }

    private func provisionRelay(deviceIDs: [String], transactionID: String) async {
        ensureProvisionStateEntries(for: deviceIDs)
        refreshAPNsRegistrationStatus()

        guard let token = KeychainService.getAPNsDeviceToken() else {
            logger.info("APNs token not available yet, skipping relay provision")
            if case .failed(let reason) = apnsRegistrationStatus {
                let apnsError = SyncUserError.apnsRegistrationFailed(reason: reason)
                for deviceID in deviceIDs {
                    relayProvisionStatuses[deviceID] = .failed(reason: apnsError.message)
                }
                errorMessage = apnsError.userVisibleDescription
            }
            return
        }

        guard !deviceIDs.isEmpty else {
            logger.info("No homeserver device IDs available, skipping relay provision")
            errorMessage = "No home server devices available for relay provisioning."
            return
        }

        var hadFailure = false
        for deviceID in deviceIDs {
            relayProvisionStatuses[deviceID] = .inProgress
            do {
                try await RelayService.provision(
                    deviceID: deviceID,
                    apnsToken: token,
                    transactionID: transactionID
                )
                relayProvisionStatuses[deviceID] = .provisioned
            } catch {
                logger.error("Failed to provision relay for device \(deviceID.prefix(8))...: \(error)")
                let userError = RelayService.userError(from: error)
                relayProvisionStatuses[deviceID] = .failed(reason: userError.message)
                errorMessage = userError.userVisibleDescription
                hadFailure = true
            }
        }

        if !hadFailure {
            errorMessage = nil
        }
        refreshStoredRelayDiagnostics()
    }

    private func deprovisionRelay() async {
        guard let token = KeychainService.getAPNsDeviceToken() else { return }

        let deviceIDs = loadStoredDeviceIDs()
        ensureProvisionStateEntries(for: deviceIDs)
        for deviceID in deviceIDs {
            do {
                try await RelayService.deprovision(deviceID: deviceID, apnsToken: token)
                relayProvisionStatuses[deviceID] = .notAttempted
            } catch {
                logger.error("Failed to deprovision relay for device \(deviceID.prefix(8))...: \(error)")
                let userError = RelayService.userError(from: error)
                relayProvisionStatuses[deviceID] = .failed(reason: userError.message)
                errorMessage = userError.userVisibleDescription
            }
        }
        refreshStoredRelayDiagnostics()
    }

    // MARK: - Token re-provisioning

    private func reprovisionOnTokenChange() async {
        guard isRelaySubscribed else { return }
        guard KeychainService.hasAPNsDeviceToken() else { return }

        let deviceIDs = loadStoredDeviceIDs()
        guard !deviceIDs.isEmpty else { return }

        logger.info("APNs token changed, re-provisioning relay for \(deviceIDs.count) device(s)")
        let transactionID = await currentRelayTransactionID() ?? "token-refresh"
        await provisionRelay(deviceIDs: deviceIDs, transactionID: transactionID)
    }

    /// Re-provision relay on app launch if the last successful provision was >24h ago.
    /// Runs silently in the background — no UI, no user interaction needed.
    private func ensureProvisioningIfNeeded() async {
        guard isRelaySubscribed else { return }
        guard KeychainService.hasAPNsDeviceToken() else { return }

        let deviceIDs = loadStoredDeviceIDs()
        guard !deviceIDs.isEmpty else { return }

        let lastProvision = UserDefaults.standard.object(forKey: Self.lastProvisionDateKey) as? Date
        if let lastProvision, Date().timeIntervalSince(lastProvision) < Self.provisionRefreshInterval {
            return
        }

        logger.info("Periodic relay re-provision (last: \(lastProvision?.description ?? "never"))")
        let transactionID = await currentRelayTransactionID() ?? "startup-refresh"
        await provisionRelay(deviceIDs: deviceIDs, transactionID: transactionID)

        // Mark successful provision time (only if at least one device succeeded).
        if relayProvisionStatuses.values.contains(.provisioned) {
            UserDefaults.standard.set(Date(), forKey: Self.lastProvisionDateKey)
        }
    }

    private static let lastProvisionDateKey = "relay-last-provision-date"
    private static let provisionRefreshInterval: TimeInterval = 24 * 60 * 60

    // MARK: - Device ID Storage

    private func storeDeviceIDs(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        if let data = try? JSONEncoder().encode(ids) {
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
    }

    private func refreshAPNsRegistrationStatus() {
        apnsRegistrationStatus = APNsRegistrationStore.current()
        apnsRegistrationSnapshot = APNsRegistrationStore.snapshot()
        hasAPNsToken = KeychainService.hasAPNsDeviceToken()
    }

    private func refreshStoredRelayDiagnostics() {
        hasAPNsToken = KeychainService.hasAPNsDeviceToken()
        lastRelayTriggerReceivedAt = RelayTriggerStore.lastReceivedAt()
        lastRelayError = RelayService.lastRecordedError()
    }

    private func currentRelayTransactionID() async -> String? {
        for await verificationResult in Transaction.currentEntitlements {
            guard case .verified(let transaction) = verificationResult else { continue }
            guard transaction.productID == Self.relayProductID else { continue }
            if let expiry = transaction.expirationDate, expiry < Date() {
                continue
            }
            return String(transaction.originalID)
        }
        return nil
    }
}

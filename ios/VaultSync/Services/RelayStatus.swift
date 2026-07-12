import Foundation

struct RelayServerObservation: Equatable, Sendable {
    let triggerObserved: Bool
    let lastTriggerObservedAt: Date?
    let checkedAt: Date
}

enum RelayStatusCheckFailure: Equatable, Sendable {
    case verificationRequired
    case rateLimited
    case temporarilyUnavailable
}

struct RelayStatusCheckOutcome: Equatable, Sendable {
    let observations: [String: RelayServerObservation]
    let failures: [String: RelayStatusCheckFailure]
    let requestedDeviceIDs: [String]

    /// Only reflects failures for devices actually queried in this pass.
    /// Global subscription/entitlement lapses remain the polling caller's gate.
    var wasRateLimited: Bool {
        requestedDeviceIDs.contains { failures[$0] == .rateLimited }
    }

    /// Only queried-device failures stop this pass. Callers must independently
    /// stop when the global subscription or verified entitlement becomes invalid.
    var requiresPollingStop: Bool {
        requestedDeviceIDs.contains {
            failures[$0] == .rateLimited || failures[$0] == .verificationRequired
        }
    }
}

/// Main-actor single-flight lease. Home and Diagnostics may coexist in the
/// navigation stack, but only one status snapshot may cross an await at a time.
@MainActor
final class RelayStatusCheckGate {
    private var isActive = false

    func begin() -> Bool {
        guard !isActive else { return false }
        isActive = true
        return true
    }

    func end() {
        isActive = false
    }
}

/// Pure cache lifecycle policy. A deprovision generation invalidates any
/// request that was already in flight without touching local wake-up history.
enum RelayStatusCacheLifecycle {
    struct Reset: Equatable, Sendable {
        let observations: [String: RelayServerObservation]
        let failures: [String: RelayStatusCheckFailure]
        let generation: Int
    }

    static func reset(currentGeneration: Int) -> Reset {
        Reset(observations: [:], failures: [:], generation: currentGeneration + 1)
    }

    static func shouldApplyOutcome(
        startedAtGeneration: Int,
        currentGeneration: Int,
        isSubscriptionActive: Bool,
        hasVerifiedEntitlement: Bool,
        isCancelled: Bool
    ) -> Bool {
        startedAtGeneration == currentGeneration &&
            isSubscriptionActive &&
            hasVerifiedEntitlement &&
            !isCancelled
    }
}

/// One status pass with injected network effects. Every homeserver commits its
/// result independently; a later failure cannot erase another server's proof.
@MainActor
enum RelayStatusChecking {
    typealias Fetch = (_ deviceID: String, _ signedTransaction: String) async throws -> RelayServerObservation

    static func run(
        deviceIDs: [String],
        provisionStatuses: [String: RelayProvisionStatus],
        initialObservations: [String: RelayServerObservation],
        initialFailures: [String: RelayStatusCheckFailure],
        isSubscriptionActive: Bool,
        entitlement: RelayEntitlementAvailability,
        fetch: Fetch
    ) async -> RelayStatusCheckOutcome {
        guard isSubscriptionActive else {
            return RelayStatusCheckOutcome(
                observations: initialObservations,
                failures: initialFailures,
                requestedDeviceIDs: []
            )
        }
        guard case .verified(let proof) = entitlement else {
            let eligible = eligibleDeviceIDs(deviceIDs, provisionStatuses: provisionStatuses)
            var failures = initialFailures
            for deviceID in eligible {
                failures[deviceID] = .verificationRequired
            }
            return RelayStatusCheckOutcome(
                observations: initialObservations,
                failures: failures,
                requestedDeviceIDs: []
            )
        }

        let eligible = eligibleDeviceIDs(deviceIDs, provisionStatuses: provisionStatuses)
        var observations = initialObservations
        var failures = initialFailures
        for deviceID in eligible {
            do {
                observations[deviceID] = try await fetch(deviceID, proof.signedTransaction)
                failures.removeValue(forKey: deviceID)
            } catch {
                failures[deviceID] = failure(from: error)
            }
        }
        return RelayStatusCheckOutcome(
            observations: observations,
            failures: failures,
            requestedDeviceIDs: eligible
        )
    }

    private static func eligibleDeviceIDs(
        _ deviceIDs: [String],
        provisionStatuses: [String: RelayProvisionStatus]
    ) -> [String] {
        Array(Set(deviceIDs)).sorted().filter {
            provisionStatuses[$0]?.isProvisionedWithVerifiedEntitlement == true
        }
    }

    private static func failure(from error: any Error) -> RelayStatusCheckFailure {
        guard let relayError = error as? RelayService.RelayError else {
            return .temporarilyUnavailable
        }
        switch relayError {
        case .rateLimited:
            return .rateLimited
        case .verifiedEntitlementRequired, .provisionFailed:
            return .verificationRequired
        case .networkError, .serverError:
            return .temporarilyUnavailable
        }
    }
}

struct RelayStatusPollingPolicy: Equatable, Sendable {
    /// Delay after attempts 1...4. Attempt 1 is immediate; the finite list is
    /// also the hard maximum, so the policy cannot become an endless loop.
    let retryDelays: [Duration]

    static let waitingView = RelayStatusPollingPolicy(
        retryDelays: [.seconds(15), .seconds(30), .seconds(60), .seconds(120)]
    )

    var maximumAttempts: Int { retryDelays.count + 1 }
}

enum RelayStatusPollingContext: Sendable {
    case waitingView
    case diagnostics
    case onboarding
    case backgroundSync

    var allowsStatusPolling: Bool {
        self == .waitingView || self == .diagnostics
    }
}

struct RelayStatusPollViewState: Equatable {
    let isSubscriptionActive: Bool
    let lastWakeUpReceivedAt: Date?
}

@MainActor
enum RelayStatusPolling {
    typealias Check = () async -> RelayStatusCheckOutcome
    typealias ShouldContinue = () -> Bool
    typealias Sleep = (Duration) async throws -> Void

    @discardableResult
    static func run(
        policy: RelayStatusPollingPolicy,
        check: Check,
        shouldContinue: ShouldContinue,
        sleep: Sleep = { try await Task.sleep(for: $0) }
    ) async -> Int {
        var attempts = 0
        while attempts < policy.maximumAttempts, !Task.isCancelled {
            let outcome = await check()
            attempts += 1
            if outcome.requiresPollingStop || !shouldContinue() || attempts >= policy.maximumAttempts {
                break
            }
            do {
                try await sleep(policy.retryDelays[attempts - 1])
            } catch {
                break
            }
        }
        return attempts
    }
}

/// The proof ladder deliberately has no derived "success" flag. Callers must
/// inspect the exact evidence they need; a weaker field never fills a stronger
/// one automatically.
struct RelayProofSnapshot: Equatable, Sendable {
    let storeKitEntitlementVerified: Bool
    let relayProvisioningConfirmed: Bool
    let relayBackendReachable: Bool
    let relayTriggerObservedAt: Date?
    let silentPushReceivedAt: Date?
    let backgroundSyncStartedAt: Date?
    let syncProgressObservedAt: Date?
}

enum RelayUserStatus: Equatable, Sendable {
    case checking
    case waitingForFirstSignal
    case relayObservedWithinGrace
    case relayObservedWaitingForWakeUp
    case wakeUpReceived
    case quietCanBeNormal
    case statusUnavailable

    var userFacingTitle: String {
        switch self {
        case .checking: return L10n.tr("Checking Relay status")
        case .waitingForFirstSignal: return L10n.tr("Waiting for your server")
        case .relayObservedWithinGrace: return L10n.tr("Server reached the Relay")
        case .relayObservedWaitingForWakeUp: return L10n.tr("Waiting for this iPhone")
        case .wakeUpReceived: return L10n.tr("Wake-up received")
        case .quietCanBeNormal: return L10n.tr("No recent activity")
        case .statusUnavailable: return L10n.tr("Status unavailable")
        }
    }

    var userFacingDetail: String {
        switch self {
        case .checking:
            return L10n.tr("Checking whether your server has reached the Relay.")
        case .waitingForFirstSignal:
            return L10n.tr("Waiting for the first signal from your server.")
        case .relayObservedWithinGrace:
            return L10n.tr("Your server reached the Relay. Delivery to this iPhone can take a moment.")
        case .relayObservedWaitingForWakeUp:
            return L10n.tr("Your server reached the Relay. This iPhone is still waiting for the wake-up signal.")
        case .wakeUpReceived:
            return L10n.tr("A wake-up signal was received on this iPhone. VaultSync then started its normal sync check.")
        case .quietCanBeNormal:
            return L10n.tr("There have been no recent changes. This can be normal.")
        case .statusUnavailable:
            return L10n.tr("Status could not be checked right now. Try again later.")
        }
    }
}

enum RelayStatusPresentation {
    static let deliveryGracePeriod: TimeInterval = 2 * 60
    static let activityFreshnessWindow: TimeInterval = 48 * 60 * 60

    static func status(
        observation: RelayServerObservation?,
        failure: RelayStatusCheckFailure?,
        localWakeUpReceivedAt: Date?,
        now: Date,
        gracePeriod: TimeInterval = deliveryGracePeriod,
        freshnessWindow: TimeInterval = activityFreshnessWindow
    ) -> RelayUserStatus {
        if let localWakeUpReceivedAt,
           now.timeIntervalSince(localWakeUpReceivedAt) < freshnessWindow {
            return .wakeUpReceived
        }
        if failure != nil {
            return .statusUnavailable
        }
        guard let observation else {
            return .checking
        }
        guard observation.triggerObserved,
              let observedAt = observation.lastTriggerObservedAt else {
            return .waitingForFirstSignal
        }
        let age = max(0, now.timeIntervalSince(observedAt))
        if age >= freshnessWindow {
            return .quietCanBeNormal
        }
        if age < gracePeriod {
            return .relayObservedWithinGrace
        }
        return .relayObservedWaitingForWakeUp
    }
}

enum RelaySyncProofStore {
    private static let backgroundStartedAtKey = "relay-background-sync-started-at"
    private static let syncProgressObservedAtKey = "relay-sync-progress-observed-at"

    static func markBackgroundSyncStarted(at date: Date = Date()) {
        UserDefaults.standard.set(date, forKey: backgroundStartedAtKey)
    }

    static func markSyncProgressObserved(at date: Date = Date()) {
        UserDefaults.standard.set(date, forKey: syncProgressObservedAtKey)
    }

    static func backgroundSyncStartedAt() -> Date? {
        UserDefaults.standard.object(forKey: backgroundStartedAtKey) as? Date
    }

    static func syncProgressObservedAt() -> Date? {
        UserDefaults.standard.object(forKey: syncProgressObservedAtKey) as? Date
    }
}

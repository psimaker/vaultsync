import Foundation

/// A StoreKit signed transaction that reached this type only after local
/// `VerificationResult.verified` handling and active-entitlement checks.
/// The compact-JWS shape guard is a final local wire safeguard: placeholder or
/// legacy strings cannot reach the relay even if a caller regresses.
struct RelayVerifiedEntitlement: Equatable, Sendable {
    let signedTransaction: String

    init?(signedTransaction: String) {
        let segments = signedTransaction.split(separator: ".", omittingEmptySubsequences: false)
        guard segments.count == 3, segments.allSatisfy({ !$0.isEmpty }) else {
            return nil
        }
        self.signedTransaction = signedTransaction
    }
}

enum RelayEntitlementAvailability: Equatable, Sendable {
    case verified(RelayVerifiedEntitlement)
    case verificationRequired
    case inactive
}

enum RelayReprovisionTrigger: String, CaseIterable, Sendable {
    case appUpdate
    case tokenRotation
    case purchase
    case restore
    case renewal
    case manualRetry
    case periodicRefresh

    var onlyDevicesNeedingMigration: Bool {
        self == .appUpdate
    }
}

struct RelayReprovisionOutcome: Equatable, Sendable {
    let statuses: [String: RelayProvisionStatus]
    let attemptedDeviceIDs: [String]
    let provisionedDeviceIDs: [String]
    /// Relay/network outcomes never revoke a verified local StoreKit state.
    let subscriptionRemainsActive: Bool

    var allAttemptsSucceeded: Bool {
        !attemptedDeviceIDs.isEmpty && attemptedDeviceIDs.count == provisionedDeviceIDs.count
    }
}

/// Pure, injected effect runner. Production supplies the real relay closure;
/// tests supply a recorder. State is published after every homeserver so a
/// later failure cannot roll back an earlier success.
@MainActor
enum RelayReprovisioning {
    typealias Provision = (_ deviceID: String, _ apnsToken: String, _ signedTransaction: String) async throws -> Void
    typealias StateDidChange = (_ statuses: [String: RelayProvisionStatus]) -> Void

    static func run(
        trigger: RelayReprovisionTrigger,
        deviceIDs: [String],
        statuses initialStatuses: [String: RelayProvisionStatus],
        entitlement: RelayEntitlementAvailability,
        apnsToken: String?,
        isSubscriptionActive: Bool = false,
        provision: Provision,
        stateDidChange: StateDidChange = { _ in }
    ) async -> RelayReprovisionOutcome {
        let uniqueDeviceIDs = Array(Set(deviceIDs)).sorted()
        var statuses = initialStatuses
        for deviceID in uniqueDeviceIDs where statuses[deviceID] == nil {
            statuses[deviceID] = .notAttempted
        }

        let targetDeviceIDs = uniqueDeviceIDs.filter { deviceID in
            !trigger.onlyDevicesNeedingMigration ||
                (statuses[deviceID] ?? .notAttempted).needsVerifiedProvisioning
        }
        guard !targetDeviceIDs.isEmpty else {
            return RelayReprovisionOutcome(
                statuses: statuses,
                attemptedDeviceIDs: [],
                provisionedDeviceIDs: [],
                subscriptionRemainsActive: isSubscriptionActive
            )
        }

        switch entitlement {
        case .verificationRequired:
            for deviceID in targetDeviceIDs {
                statuses[deviceID] = .storeKitVerificationRequired
            }
            stateDidChange(statuses)
            return RelayReprovisionOutcome(
                statuses: statuses,
                attemptedDeviceIDs: [],
                provisionedDeviceIDs: [],
                subscriptionRemainsActive: isSubscriptionActive
            )
        case .inactive:
            return RelayReprovisionOutcome(
                statuses: statuses,
                attemptedDeviceIDs: [],
                provisionedDeviceIDs: [],
                subscriptionRemainsActive: isSubscriptionActive
            )
        case .verified(let entitlement):
            guard let apnsToken, !apnsToken.isEmpty else {
                let reason = L10n.tr("Push registration is not ready yet.")
                for deviceID in targetDeviceIDs {
                    statuses[deviceID] = .temporarilyFailed(reason: reason)
                }
                stateDidChange(statuses)
                return RelayReprovisionOutcome(
                    statuses: statuses,
                    attemptedDeviceIDs: [],
                    provisionedDeviceIDs: [],
                    subscriptionRemainsActive: isSubscriptionActive
                )
            }

            var provisionedDeviceIDs: [String] = []
            for deviceID in targetDeviceIDs {
                statuses[deviceID] = .inProgress
                stateDidChange(statuses)
                do {
                    try await provision(deviceID, apnsToken, entitlement.signedTransaction)
                    statuses[deviceID] = .provisionedVerified
                    provisionedDeviceIDs.append(deviceID)
                } catch {
                    statuses[deviceID] = .temporarilyFailed(reason: error.localizedDescription)
                }
                stateDidChange(statuses)
            }

            return RelayReprovisionOutcome(
                statuses: statuses,
                attemptedDeviceIDs: targetDeviceIDs,
                provisionedDeviceIDs: provisionedDeviceIDs,
                subscriptionRemainsActive: isSubscriptionActive
            )
        }
    }
}

/// Per-homeserver migration persistence. The legacy v1 key is read but not
/// removed: an older app after rollback still sees its last-known registrations.
enum RelayProvisionStatusStore {
    static let statusesKey = "relay-provision-statuses-v2"
    static let legacyProvisionedDeviceIDsKey = "relay-provisioned-device-ids"

    static func load(defaults: UserDefaults = .standard) -> [String: RelayProvisionStatus] {
        let rawStatuses = defaults.dictionary(forKey: statusesKey) as? [String: String] ?? [:]
        var statuses = rawStatuses.mapValues(status(from:))

        let legacyIDs = defaults.stringArray(forKey: legacyProvisionedDeviceIDsKey) ?? []
        for deviceID in legacyIDs where statuses[deviceID] == nil {
            statuses[deviceID] = .migrationRequired
        }
        return statuses
    }

    static func save(
        _ statuses: [String: RelayProvisionStatus],
        preserveLegacyProvisionedIDs: Bool = true,
        defaults: UserDefaults = .standard
    ) {
        defaults.set(statuses.mapValues(\.stateKey), forKey: statusesKey)

        let verifiedIDs = statuses
            .filter { $0.value.isProvisionedWithVerifiedEntitlement }
            .map { $0.key }
        let legacyIDs = preserveLegacyProvisionedIDs
            ? defaults.stringArray(forKey: legacyProvisionedDeviceIDsKey) ?? []
            : []
        defaults.set(Array(Set(legacyIDs + verifiedIDs)).sorted(), forKey: legacyProvisionedDeviceIDsKey)
    }

    private static func status(from stateKey: String) -> RelayProvisionStatus {
        switch stateKey {
        case "migration_required", "in_progress":
            return .migrationRequired
        case "provisioned_verified":
            return .provisionedVerified
        case "temporarily_failed":
            return .temporarilyFailed(reason: L10n.tr("The previous update did not complete."))
        case "storekit_verification_required":
            return .storeKitVerificationRequired
        default:
            return .notAttempted
        }
    }
}

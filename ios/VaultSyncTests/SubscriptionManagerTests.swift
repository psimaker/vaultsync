import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Relay Provision State Machine", .serialized)
struct SubscriptionManagerTests {
    private static let invalidStoredDeviceJSON =
        "[\"P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ3\"]"

    @Test("RelayProvisionStatus exposes stable summary contract")
    func relayProvisionStatusContract() {
        #expect(RelayProvisionStatus.notAttempted.stateKey == "not_attempted")
        #expect(RelayProvisionStatus.migrationRequired.stateKey == "migration_required")
        #expect(RelayProvisionStatus.inProgress.stateKey == "in_progress")
        #expect(RelayProvisionStatus.provisionedVerified.stateKey == "provisioned_verified")
        #expect(RelayProvisionStatus.storeKitVerificationRequired.stateKey == "storekit_verification_required")

        let failed = RelayProvisionStatus.temporarilyFailed(reason: "network issue")
        #expect(failed.stateKey == "temporarily_failed")
        #expect(failed.summary == L10n.tr("Temporarily failed"))
        #expect(failed.failureReason == "network issue")
    }

    @Test("Retry seeds provisioning state entries without crashing")
    func retrySeedsProvisionEntries() async {
        TestSupport.resetRelayState()

        let manager = SubscriptionManager(
            relayDeviceIDStorageEnvironment: RelayDeviceIDStorage.Environment(
                load: { .notFound },
                encode: { ids in
                    String(decoding: try JSONEncoder().encode(ids), as: UTF8.self)
                },
                write: { _ in true }
            ),
            startsLiveWork: false
        )
        let deviceID = TestSupport.samplePeerDeviceID
        await manager.retryRelayProvisioning(homeserverDeviceIDs: [deviceID])

        #expect(manager.relayProvisionStatuses[deviceID] == .notAttempted)
    }

    @Test("Manual retry surfaces a device ID storage failure (#148)")
    func issue148ManualRetrySurfacesStorageFailure() async {
        TestSupport.resetRelayState()

        let manager = SubscriptionManager(
            relayDeviceIDStorageEnvironment: RelayDeviceIDStorage.Environment(
                load: { .notFound },
                encode: { ids in
                    String(decoding: try JSONEncoder().encode(ids), as: UTF8.self)
                },
                write: { _ in false }
            ),
            startsLiveWork: false
        )
        let deviceID = TestSupport.samplePeerDeviceID
        await manager.retryRelayProvisioning(homeserverDeviceIDs: [deviceID])

        #expect(
            manager.relayProvisionStatuses[deviceID]
                == .notAttempted
        )
        #expect(
            manager.relayDeviceIDStorageErrorMessage
                == L10n.tr("Cloud Relay provisioning did not complete.")
        )
        #expect(manager.relayProvisioningNeedsAttention)
    }

    @Test("Invalid stored IDs use the existing generic storage failure without rewrite (#161)")
    func issue161InvalidStoredIDsSurfaceGenericFailure() async {
        TestSupport.resetRelayState()
        defer { TestSupport.resetRelayState() }

        var encodeCount = 0
        var writeCount = 0
        let manager = SubscriptionManager(
            relayDeviceIDStorageEnvironment: RelayDeviceIDStorage.Environment(
                load: {
                    RelayDeviceIDStorage.decodeStoredValue(Self.invalidStoredDeviceJSON)
                },
                encode: { _ in
                    encodeCount += 1
                    return "encoded"
                },
                write: { _ in
                    writeCount += 1
                    return true
                }
            ),
            startsLiveWork: false
        )

        await manager.retryRelayProvisioning(homeserverDeviceIDs: [])

        #expect(
            manager.relayDeviceIDStorageErrorMessage
                == L10n.tr("Cloud Relay provisioning did not complete.")
        )
        #expect(manager.relayProvisioningNeedsAttention)
        #expect(encodeCount == 0)
        #expect(writeCount == 0)
    }
}

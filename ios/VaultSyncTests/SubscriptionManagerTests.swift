import Testing
@testable import VaultSync

@MainActor
@Suite("Relay Provision State Machine", .serialized)
struct SubscriptionManagerTests {
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

        let manager = SubscriptionManager()
        let deviceID = TestSupport.samplePeerDeviceID
        await manager.retryRelayProvisioning(homeserverDeviceIDs: [deviceID])

        #expect(manager.relayProvisionStatuses[deviceID] == .notAttempted)
    }
}

import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Verified Relay re-provisioning migration", .serialized)
struct RelayReprovisioningTests {
    private static let signedTransaction = "header.payload.signature"
    private static let firstDevice = "server-a"
    private static let secondDevice = "server-b"

    private struct NetworkFailure: Error, LocalizedError {
        var errorDescription: String? { "temporary network failure" }
    }

    @MainActor
    private final class ProvisionRecorder {
        struct Call: Equatable {
            let deviceID: String
            let token: String
            let signedTransaction: String
        }

        var calls: [Call] = []
        var failingDeviceIDs: Set<String> = []

        func provision(deviceID: String, token: String, signedTransaction: String) async throws {
            calls.append(Call(
                deviceID: deviceID,
                token: token,
                signedTransaction: signedTransaction
            ))
            if failingDeviceIDs.contains(deviceID) {
                throw NetworkFailure()
            }
        }
    }

    private func verifiedAvailability() -> RelayEntitlementAvailability {
        .verified(RelayVerifiedEntitlement(signedTransaction: Self.signedTransaction)!)
    }

    @Test("No verified JWS means no network request")
    func noVerifiedJWSNoRequest() async {
        let recorder = ProvisionRecorder()
        let outcome = await RelayReprovisioning.run(
            trigger: .manualRetry,
            deviceIDs: [Self.firstDevice],
            statuses: [Self.firstDevice: .migrationRequired],
            entitlement: .verificationRequired,
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls.isEmpty)
        #expect(outcome.statuses[Self.firstDevice] == .storeKitVerificationRequired)
        #expect(outcome.attemptedDeviceIDs.isEmpty)
    }

    @Test("Synthetic fallback values are rejected before request construction", arguments: [
        "manual-retry",
        "token-refresh",
        "startup-refresh",
    ])
    func fallbackValuesNeverReachWire(value: String) throws {
        #expect(RelayVerifiedEntitlement(signedTransaction: value) == nil)
        #expect(throws: RelayService.RelayError.self) {
            _ = try RelayService.makeProvisionRequest(
                baseURL: "https://relay.invalid",
                deviceID: Self.firstDevice,
                apnsToken: "token",
                signedTransaction: value
            )
        }
    }

    @Test("Verified active subscription sends the unchanged JWS wire field")
    func verifiedSubscriptionProvisionsWithJWS() async throws {
        let recorder = ProvisionRecorder()
        let outcome = await RelayReprovisioning.run(
            trigger: .purchase,
            deviceIDs: [Self.firstDevice],
            statuses: [:],
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls == [ProvisionRecorder.Call(
            deviceID: Self.firstDevice,
            token: "token",
            signedTransaction: Self.signedTransaction
        )])
        #expect(outcome.statuses[Self.firstDevice] == .provisionedVerified)

        let request = try RelayService.makeProvisionRequest(
            baseURL: "https://relay.invalid",
            deviceID: Self.firstDevice,
            apnsToken: "token",
            signedTransaction: Self.signedTransaction
        )
        let body = try #require(request.httpBody)
        let json = try #require(JSONSerialization.jsonObject(with: body) as? [String: String])
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path == "/api/v1/provision")
        #expect(Set(json.keys) == ["device_id", "apns_token", "transaction_id"])
        #expect(json["transaction_id"] == Self.signedTransaction)
        #expect(RelayService.isSuccessfulProvisionStatusCode(200))
        #expect(!RelayService.isSuccessfulProvisionStatusCode(409))
    }

    @Test("Purchase, Restore, Renewal, token rotation, and app update all provision with verified proof", arguments: [
        RelayReprovisionTrigger.purchase,
        .restore,
        .renewal,
        .tokenRotation,
        .appUpdate,
    ])
    func requiredTriggersProvision(trigger: RelayReprovisionTrigger) async {
        let recorder = ProvisionRecorder()
        let outcome = await RelayReprovisioning.run(
            trigger: trigger,
            deviceIDs: [Self.firstDevice],
            statuses: [Self.firstDevice: .migrationRequired],
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls.count == 1)
        #expect(recorder.calls.first?.signedTransaction == Self.signedTransaction)
        #expect(outcome.statuses[Self.firstDevice] == .provisionedVerified)
    }

    @Test("Multiple homeservers persist success independently after partial failure")
    func partialMultiHomeserverSuccess() async {
        let recorder = ProvisionRecorder()
        recorder.failingDeviceIDs = [Self.firstDevice]
        var snapshots: [[String: RelayProvisionStatus]] = []

        let outcome = await RelayReprovisioning.run(
            trigger: .renewal,
            deviceIDs: [Self.firstDevice, Self.secondDevice],
            statuses: [
                Self.firstDevice: .migrationRequired,
                Self.secondDevice: .migrationRequired,
            ],
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            provision: recorder.provision,
            stateDidChange: { snapshots.append($0) }
        )

        #expect(recorder.calls.count == 2)
        #expect(outcome.statuses[Self.firstDevice]?.stateKey == "temporarily_failed")
        #expect(outcome.statuses[Self.secondDevice] == .provisionedVerified)
        #expect(snapshots.contains { $0[Self.secondDevice] == .provisionedVerified })
    }

    @Test("A network failure remains retryable and the next run succeeds")
    func retryAfterNetworkFailure() async {
        let recorder = ProvisionRecorder()
        recorder.failingDeviceIDs = [Self.firstDevice]
        let failed = await RelayReprovisioning.run(
            trigger: .appUpdate,
            deviceIDs: [Self.firstDevice],
            statuses: [Self.firstDevice: .migrationRequired],
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            provision: recorder.provision
        )

        recorder.failingDeviceIDs = []
        let retried = await RelayReprovisioning.run(
            trigger: .manualRetry,
            deviceIDs: [Self.firstDevice],
            statuses: failed.statuses,
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls.count == 2)
        #expect(retried.statuses[Self.firstDevice] == .provisionedVerified)
    }

    @Test("Expired subscription never provisions")
    func expiredSubscription() async {
        await assertInactiveEntitlementDoesNotProvision()
    }

    @Test("Revoked subscription never provisions")
    func revokedSubscription() async {
        await assertInactiveEntitlementDoesNotProvision()
    }

    @Test("Unverified StoreKit transaction requests confirmation without provisioning")
    func unverifiedTransaction() async {
        let recorder = ProvisionRecorder()
        let outcome = await RelayReprovisioning.run(
            trigger: .renewal,
            deviceIDs: [Self.firstDevice],
            statuses: [Self.firstDevice: .migrationRequired],
            entitlement: .verificationRequired,
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls.isEmpty)
        #expect(outcome.statuses[Self.firstDevice] == .storeKitVerificationRequired)
    }

    @Test("Existing user remains locally active after a transient migration failure")
    func existingUserStateSurvivesFailure() async {
        let recorder = ProvisionRecorder()
        recorder.failingDeviceIDs = [Self.firstDevice]
        let localSubscriptionActive = true
        let completedOnboarding = true
        let deviceIdentity = "unchanged-device-identity"
        let folderMapping = "/unchanged/vault/path"

        let outcome = await RelayReprovisioning.run(
            trigger: .appUpdate,
            deviceIDs: [Self.firstDevice],
            statuses: [Self.firstDevice: .migrationRequired],
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            isSubscriptionActive: localSubscriptionActive,
            provision: recorder.provision
        )

        #expect(outcome.subscriptionRemainsActive)
        #expect(completedOnboarding)
        #expect(deviceIdentity == "unchanged-device-identity")
        #expect(folderMapping == "/unchanged/vault/path")
        #expect(outcome.statuses[Self.firstDevice]?.stateKey == "temporarily_failed")
    }

    @Test("App-update migration is idempotent for already verified homeservers")
    func appUpdateMigrationIsIdempotent() async {
        let recorder = ProvisionRecorder()
        let outcome = await RelayReprovisioning.run(
            trigger: .appUpdate,
            deviceIDs: [Self.firstDevice],
            statuses: [Self.firstDevice: .provisionedVerified],
            entitlement: verifiedAvailability(),
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls.isEmpty)
        #expect(outcome.statuses[Self.firstDevice] == .provisionedVerified)
    }

    @Test("Legacy state migrates conservatively and partial success survives relaunch and rollback")
    func migrationPersistenceAndRollback() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "relay-reprovision-state")
        defaults.set(
            [Self.firstDevice, Self.secondDevice],
            forKey: RelayProvisionStatusStore.legacyProvisionedDeviceIDsKey
        )

        var statuses = RelayProvisionStatusStore.load(defaults: defaults)
        #expect(statuses[Self.firstDevice] == .migrationRequired)
        #expect(statuses[Self.secondDevice] == .migrationRequired)

        statuses[Self.firstDevice] = .temporarilyFailed(reason: "temporary")
        statuses[Self.secondDevice] = .provisionedVerified
        RelayProvisionStatusStore.save(statuses, defaults: defaults)

        let reloaded = RelayProvisionStatusStore.load(defaults: defaults)
        #expect(reloaded[Self.firstDevice]?.stateKey == "temporarily_failed")
        #expect(reloaded[Self.secondDevice] == .provisionedVerified)
        #expect(Set(defaults.stringArray(
            forKey: RelayProvisionStatusStore.legacyProvisionedDeviceIDsKey
        ) ?? []) == Set([Self.firstDevice, Self.secondDevice]))
    }

    private func assertInactiveEntitlementDoesNotProvision() async {
        let recorder = ProvisionRecorder()
        let initial: [String: RelayProvisionStatus] = [Self.firstDevice: .migrationRequired]
        let outcome = await RelayReprovisioning.run(
            trigger: .renewal,
            deviceIDs: [Self.firstDevice],
            statuses: initial,
            entitlement: .inactive,
            apnsToken: "token",
            provision: recorder.provision
        )

        #expect(recorder.calls.isEmpty)
        #expect(outcome.statuses == initial)
    }
}

import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Relay observation and honest proof hierarchy (#91 Stage 2)")
struct RelayStatusTests {
    private static let firstDevice = "server-a"
    private static let secondDevice = "server-b"
    private static let signedTransaction = "header.payload.signature"

    private struct NetworkFailure: Error {}

    @MainActor
    private final class FetchRecorder {
        var calls: [(String, String)] = []
        var failures: Set<String> = []
        var observations: [String: RelayServerObservation] = [:]

        func fetch(deviceID: String, signedTransaction: String) async throws -> RelayServerObservation {
            calls.append((deviceID, signedTransaction))
            if failures.contains(deviceID) { throw NetworkFailure() }
            return observations[deviceID] ?? RelayServerObservation(
                triggerObserved: false,
                lastTriggerObservedAt: nil,
                checkedAt: Date(timeIntervalSince1970: 1_000)
            )
        }
    }

    private func verifiedEntitlement() -> RelayEntitlementAvailability {
        .verified(RelayVerifiedEntitlement(signedTransaction: Self.signedTransaction)!)
    }

    @Test("Missing, unverified, expired, or revoked entitlement sends no status request", arguments: [
        RelayEntitlementAvailability.verificationRequired,
        .inactive,
    ])
    func invalidEntitlementSendsNoRequest(entitlement: RelayEntitlementAvailability) async {
        let recorder = FetchRecorder()
        let outcome = await RelayStatusChecking.run(
            deviceIDs: [Self.firstDevice],
            provisionStatuses: [Self.firstDevice: .provisionedVerified],
            initialObservations: [:],
            initialFailures: [:],
            isSubscriptionActive: entitlement != .inactive,
            entitlement: entitlement,
            fetch: recorder.fetch
        )

        #expect(recorder.calls.isEmpty)
        #expect(outcome.requestedDeviceIDs.isEmpty)
    }

    @Test("Status request contains only the additive wire contract")
    func statusRequestContract() throws {
        #expect(throws: RelayService.RelayError.self) {
            _ = try RelayService.makeStatusRequest(
                baseURL: "https://relay.invalid",
                deviceID: Self.firstDevice,
                signedTransaction: "missing-proof"
            )
        }
        let request = try RelayService.makeStatusRequest(
            baseURL: "https://relay.invalid",
            deviceID: Self.firstDevice,
            signedTransaction: Self.signedTransaction
        )
        let body = try #require(request.httpBody)
        let json = try #require(JSONSerialization.jsonObject(with: body) as? [String: String])
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path == "/api/v1/status")
        #expect(Set(json.keys) == ["device_id", "signed_transaction"])
        #expect(json["signed_transaction"] == Self.signedTransaction)
        #expect(json["apns_token"] == nil)
        #expect(json["transaction_id"] == nil)
    }

    @Test("Homeserver without verified provisioning sends no status request")
    func unverifiedProvisioningSendsNoRequest() async {
        let recorder = FetchRecorder()
        let outcome = await RelayStatusChecking.run(
            deviceIDs: [Self.firstDevice],
            provisionStatuses: [Self.firstDevice: .migrationRequired],
            initialObservations: [:],
            initialFailures: [:],
            isSubscriptionActive: true,
            entitlement: verifiedEntitlement(),
            fetch: recorder.fetch
        )
        #expect(recorder.calls.isEmpty)
        #expect(outcome.requestedDeviceIDs.isEmpty)
    }

    @Test("Status response decoder accepts only consistent minimal observation")
    func statusResponseDecoder() throws {
        let data = Data(#"{"v1_trigger_observed":true,"last_trigger_observed_at":"2026-07-12T10:00:00Z","checked_at":"2026-07-12T10:01:00Z"}"#.utf8)
        let observation = try RelayService.decodeStatusResponse(data)
        #expect(observation.triggerObserved)
        #expect(observation.lastTriggerObservedAt != nil)

        let inconsistent = Data(#"{"v1_trigger_observed":true,"checked_at":"2026-07-12T10:01:00Z"}"#.utf8)
        #expect(throws: RelayService.RelayError.self) {
            _ = try RelayService.decodeStatusResponse(inconsistent)
        }
    }

    @Test("Multiple homeservers keep partial success independent")
    func partialMultiServerSuccess() async {
        let recorder = FetchRecorder()
        recorder.failures = [Self.firstDevice]
        recorder.observations[Self.secondDevice] = RelayServerObservation(
            triggerObserved: true,
            lastTriggerObservedAt: Date(timeIntervalSince1970: 900),
            checkedAt: Date(timeIntervalSince1970: 1_000)
        )
        let existing = RelayServerObservation(
            triggerObserved: false,
            lastTriggerObservedAt: nil,
            checkedAt: Date(timeIntervalSince1970: 500)
        )

        let outcome = await RelayStatusChecking.run(
            deviceIDs: [Self.firstDevice, Self.secondDevice],
            provisionStatuses: [
                Self.firstDevice: .provisionedVerified,
                Self.secondDevice: .provisionedVerified,
            ],
            initialObservations: [Self.firstDevice: existing],
            initialFailures: [:],
            isSubscriptionActive: true,
            entitlement: verifiedEntitlement(),
            fetch: recorder.fetch
        )

        #expect(outcome.observations[Self.firstDevice] == existing)
        #expect(outcome.failures[Self.firstDevice] == .temporarilyUnavailable)
        #expect(outcome.observations[Self.secondDevice]?.triggerObserved == true)
        #expect(outcome.failures[Self.secondDevice] == nil)
        #expect(recorder.calls.count == 2)
    }

    @Test("Network failure remains retryable without changing provisioning")
    func retryAfterNetworkFailure() async {
        let recorder = FetchRecorder()
        let provisioning = [Self.firstDevice: RelayProvisionStatus.provisionedVerified]
        recorder.failures = [Self.firstDevice]
        let failed = await RelayStatusChecking.run(
            deviceIDs: [Self.firstDevice],
            provisionStatuses: provisioning,
            initialObservations: [:],
            initialFailures: [:],
            isSubscriptionActive: true,
            entitlement: verifiedEntitlement(),
            fetch: recorder.fetch
        )
        recorder.failures = []
        let retried = await RelayStatusChecking.run(
            deviceIDs: [Self.firstDevice],
            provisionStatuses: provisioning,
            initialObservations: failed.observations,
            initialFailures: failed.failures,
            isSubscriptionActive: true,
            entitlement: verifiedEntitlement(),
            fetch: recorder.fetch
        )

        #expect(retried.failures[Self.firstDevice] == nil)
        #expect(retried.observations[Self.firstDevice] != nil)
        #expect(provisioning[Self.firstDevice] == .provisionedVerified)
    }

    @Test("Concurrent status checks are single-flight")
    func concurrentChecksAreSingleFlight() {
        let gate = RelayStatusCheckGate()

        #expect(gate.begin())
        #expect(!gate.begin())
        gate.end()
        #expect(gate.begin())
        gate.end()
    }

    @Test("Deprovision resets server observation evidence and invalidates in-flight outcomes")
    func deprovisionInvalidatesObservationEvidence() {
        let reset = RelayStatusCacheLifecycle.reset(currentGeneration: 4)

        #expect(reset.observations.isEmpty)
        #expect(reset.failures.isEmpty)
        #expect(reset.generation == 5)
        #expect(!RelayStatusCacheLifecycle.shouldApplyOutcome(
            startedAtGeneration: 4,
            currentGeneration: reset.generation,
            isSubscriptionActive: true,
            hasVerifiedEntitlement: true,
            isCancelled: false
        ))
    }

    @Test("Only a current active verified non-cancelled status outcome may update evidence")
    func statusOutcomeApplicationGate() {
        #expect(RelayStatusCacheLifecycle.shouldApplyOutcome(
            startedAtGeneration: 2,
            currentGeneration: 2,
            isSubscriptionActive: true,
            hasVerifiedEntitlement: true,
            isCancelled: false
        ))
        #expect(!RelayStatusCacheLifecycle.shouldApplyOutcome(
            startedAtGeneration: 2,
            currentGeneration: 2,
            isSubscriptionActive: false,
            hasVerifiedEntitlement: true,
            isCancelled: false
        ))
        #expect(!RelayStatusCacheLifecycle.shouldApplyOutcome(
            startedAtGeneration: 2,
            currentGeneration: 2,
            isSubscriptionActive: true,
            hasVerifiedEntitlement: false,
            isCancelled: false
        ))
        #expect(!RelayStatusCacheLifecycle.shouldApplyOutcome(
            startedAtGeneration: 2,
            currentGeneration: 2,
            isSubscriptionActive: true,
            hasVerifiedEntitlement: true,
            isCancelled: true
        ))
    }

    @Test("Polling is allowed only in waiting and diagnostics screens")
    func pollingContextGate() {
        #expect(RelayStatusPollingContext.waitingView.allowsStatusPolling)
        #expect(RelayStatusPollingContext.diagnostics.allowsStatusPolling)
        #expect(!RelayStatusPollingContext.onboarding.allowsStatusPolling)
        #expect(!RelayStatusPollingContext.backgroundSync.allowsStatusPolling)
    }

    @Test("Polling checks immediately then uses bounded slow backoff")
    func boundedBackoff() async {
        let policy = RelayStatusPollingPolicy(
            retryDelays: [.seconds(15), .seconds(30), .seconds(60)]
        )
        var checks = 0
        var sleeps: [Duration] = []
        let attempts = await RelayStatusPolling.run(
            policy: policy,
            check: {
                checks += 1
                return emptyOutcome()
            },
            shouldContinue: { true },
            sleep: { sleeps.append($0) }
        )

        #expect(checks == 4)
        #expect(attempts == policy.maximumAttempts)
        #expect(sleeps == policy.retryDelays)
    }

    @Test("Polling stops when the screen leaves")
    func pollStopsOnLeave() async {
        var checks = 0
        let attempts = await RelayStatusPolling.run(
            policy: .waitingView,
            check: {
                checks += 1
                return emptyOutcome()
            },
            shouldContinue: { false },
            sleep: { _ in Issue.record("sleep must not run after leaving") }
        )
        #expect(attempts == 1)
        #expect(checks == 1)
    }

    @Test("Polling stops after a local wake-up")
    func pollStopsAfterWakeUp() async {
        var wakeUpReceived = false
        let attempts = await RelayStatusPolling.run(
            policy: .waitingView,
            check: {
                wakeUpReceived = true
                return emptyOutcome()
            },
            shouldContinue: { !wakeUpReceived },
            sleep: { _ in Issue.record("sleep must not run after local wake-up") }
        )
        #expect(attempts == 1)
    }

    @Test("Rate limiting stops the bounded poll")
    func rateLimitStopsPoll() async {
        var checks = 0
        let attempts = await RelayStatusPolling.run(
            policy: .waitingView,
            check: {
                checks += 1
                return RelayStatusCheckOutcome(
                    observations: [:],
                    failures: [Self.firstDevice: .rateLimited],
                    requestedDeviceIDs: [Self.firstDevice]
                )
            },
            shouldContinue: { true },
            sleep: { _ in Issue.record("rate-limited poll must not sleep/retry") }
        )
        #expect(attempts == 1)
        #expect(checks == 1)
    }

    @Test("Presentation distinguishes never, grace, delayed, local receipt, stale, and errors")
    func honestPresentationStates() {
        let now = Date(timeIntervalSince1970: 10_000)
        let never = RelayServerObservation(triggerObserved: false, lastTriggerObservedAt: nil, checkedAt: now)
        let recent = RelayServerObservation(
            triggerObserved: true,
            lastTriggerObservedAt: now.addingTimeInterval(-30),
            checkedAt: now
        )
        let delayed = RelayServerObservation(
            triggerObserved: true,
            lastTriggerObservedAt: now.addingTimeInterval(-300),
            checkedAt: now
        )
        let stale = RelayServerObservation(
            triggerObserved: true,
            lastTriggerObservedAt: now.addingTimeInterval(-72 * 60 * 60),
            checkedAt: now
        )

        #expect(RelayStatusPresentation.status(observation: never, failure: nil, localWakeUpReceivedAt: nil, now: now) == .waitingForFirstSignal)
        #expect(RelayStatusPresentation.status(observation: recent, failure: nil, localWakeUpReceivedAt: nil, now: now) == .relayObservedWithinGrace)
        #expect(RelayStatusPresentation.status(observation: delayed, failure: nil, localWakeUpReceivedAt: nil, now: now) == .relayObservedWaitingForWakeUp)
        #expect(RelayStatusPresentation.status(observation: delayed, failure: nil, localWakeUpReceivedAt: now, now: now) == .wakeUpReceived)
        #expect(RelayStatusPresentation.status(observation: stale, failure: nil, localWakeUpReceivedAt: nil, now: now) == .quietCanBeNormal)
        #expect(RelayStatusPresentation.status(observation: delayed, failure: .temporarilyUnavailable, localWakeUpReceivedAt: nil, now: now) == .statusUnavailable)
    }

    @Test("Health and relay observation never imply stronger delivery or sync proof")
    func proofHierarchyDoesNotEscalate() {
        let observation = Date(timeIntervalSince1970: 1_000)
        let proof = RelayProofSnapshot(
            storeKitEntitlementVerified: true,
            relayProvisioningConfirmed: true,
            relayBackendReachable: true,
            relayTriggerObservedAt: observation,
            silentPushReceivedAt: nil,
            backgroundSyncStartedAt: nil,
            syncProgressObservedAt: nil
        )
        #expect(proof.relayBackendReachable)
        #expect(proof.relayTriggerObservedAt == observation)
        #expect(proof.silentPushReceivedAt == nil)
        #expect(proof.backgroundSyncStartedAt == nil)
        #expect(proof.syncProgressObservedAt == nil)
    }

    @Test("Normal status copy contains no protocol or entitlement jargon")
    func normalCopyAvoidsTechnicalTerms() {
        let forbidden = [
            "trigger v1", "device-id", "jws", "original transaction id",
            "apns", "hmac", "nonce", "legacy provisioning",
        ]
        let statuses: [RelayUserStatus] = [
            .checking, .waitingForFirstSignal, .relayObservedWithinGrace,
            .relayObservedWaitingForWakeUp, .wakeUpReceived,
            .quietCanBeNormal, .statusUnavailable,
        ]
        for status in statuses {
            let copy = (status.userFacingTitle + " " + status.userFacingDetail).lowercased()
            for term in forbidden {
                #expect(!copy.contains(term))
            }
        }
    }

    private func emptyOutcome() -> RelayStatusCheckOutcome {
        RelayStatusCheckOutcome(observations: [:], failures: [:], requestedDeviceIDs: [])
    }
}

import Foundation
import Testing
@testable import VaultSync

@Suite("Honest synchronization-path proof model (2.0)")
struct RelaySyncPathCheckTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    @Test("Weaker evidence never sets local progress, direction, or roundtrip")
    func weakerEvidenceDoesNotEscalate() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)
        let evidence = SyncPathScopedEvidence(checkID: checkID, targetID: target, observedAt: now)

        proof.observe(.backgroundSyncStarted(evidence))
        proof.observe(.syncthingQueried(evidence))
        proof.observe(.localScanCompleted(evidence))
        proof.observe(.remoteIndexObserved(evidence))
        proof.observe(.folderIdle(evidence))

        #expect(proof.backgroundSyncStartedAt == now)
        #expect(proof.syncthingQueriedAt == now)
        #expect(proof.localDataProgressObservedAt == nil)
        #expect(proof.uploadConfirmedAt == nil)
        #expect(proof.downloadConfirmedAt == nil)
        #expect(proof.roundTripConfirmedAt == nil)
    }

    @Test("Local data progress remains separate from upload and download proof")
    func localProgressDoesNotSetDirection() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)

        proof.observe(.localDataProgress(.init(checkID: checkID, targetID: target, observedAt: now)))

        #expect(proof.localDataProgressObservedAt == now)
        #expect(proof.uploadConfirmedAt == nil)
        #expect(proof.downloadConfirmedAt == nil)
        #expect(proof.roundTripConfirmedAt == nil)
    }

    @Test("Upload and download stay independent; roundtrip requires matching causal evidence")
    func roundTripRequiresMatchingCorrelation() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        let uploadCorrelation = UUID()
        let otherCorrelation = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)

        proof.observe(.uploadConfirmed(.init(
            checkID: checkID,
            targetID: target,
            observedAt: now.addingTimeInterval(1),
            correlationID: uploadCorrelation
        )))
        #expect(proof.uploadConfirmedAt != nil)
        #expect(proof.downloadConfirmedAt == nil)
        #expect(proof.roundTripConfirmedAt == nil)

        proof.observe(.downloadConfirmed(.init(
            checkID: checkID,
            targetID: target,
            observedAt: now.addingTimeInterval(2),
            correlationID: otherCorrelation
        )))
        #expect(proof.downloadConfirmedAt != nil)
        #expect(proof.roundTripConfirmedAt == nil)

        proof.observe(.uploadConfirmed(.init(
            checkID: checkID,
            targetID: target,
            observedAt: now.addingTimeInterval(3),
            correlationID: otherCorrelation
        )))
        #expect(proof.roundTripConfirmedAt == nil)

        proof.observe(.downloadConfirmed(.init(
            checkID: checkID,
            targetID: target,
            observedAt: now.addingTimeInterval(4),
            correlationID: otherCorrelation
        )))
        #expect(proof.roundTripConfirmedAt == now.addingTimeInterval(4))
    }

    @Test("Older directional evidence cannot replace a newer correlation")
    func directionalEvidenceIsReplacedAtomically() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        let correlation = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)

        proof.observe(.uploadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(5), correlationID: correlation
        )))
        proof.observe(.uploadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(4), correlationID: UUID()
        )))
        proof.observe(.downloadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(6), correlationID: correlation
        )))

        #expect(proof.uploadConfirmedAt == now.addingTimeInterval(5))
        #expect(proof.roundTripConfirmedAt == now.addingTimeInterval(6))
    }

    @Test("Download before upload is not a roundtrip")
    func roundTripRequiresUploadThenDownload() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        let correlation = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)

        proof.observe(.downloadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(1), correlationID: correlation
        )))
        proof.observe(.uploadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(2), correlationID: correlation
        )))
        #expect(proof.roundTripConfirmedAt == nil)

        proof.observe(.downloadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(3), correlationID: correlation
        )))
        #expect(proof.roundTripConfirmedAt == now.addingTimeInterval(3))
    }

    @Test("A confirmed roundtrip is not erased by later unrelated evidence")
    func roundTripProofIsMonotonic() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        let correlation = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)

        proof.observe(.uploadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(1), correlationID: correlation
        )))
        proof.observe(.downloadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(2), correlationID: correlation
        )))
        proof.observe(.uploadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(3), correlationID: UUID()
        )))

        #expect(proof.roundTripConfirmedAt == now.addingTimeInterval(2))
    }

    @Test("Diagnostics exposes every proof stage as a separate row")
    func diagnosticsStagesStaySeparate() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        let correlation = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)
        proof.observe(.syncthingQueried(.init(
            checkID: checkID, targetID: target, observedAt: now.addingTimeInterval(1)
        )))
        proof.observe(.localDataProgress(.init(
            checkID: checkID, targetID: target, observedAt: now.addingTimeInterval(2)
        )))
        proof.observe(.uploadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(3), correlationID: correlation
        )))
        proof.observe(.downloadConfirmed(.init(
            checkID: checkID, targetID: target,
            observedAt: now.addingTimeInterval(4), correlationID: correlation
        )))

        #expect(SyncPathDiagnosticStage.allCases == [
            .syncthingQueried, .localDataProgress, .upload, .download, .roundTrip,
        ])
        #expect(Set(SyncPathDiagnosticStage.allCases.map(\.title)).count == 5)
        #expect(SyncPathDiagnosticStage.syncthingQueried.timestamp(in: proof) == now.addingTimeInterval(1))
        #expect(SyncPathDiagnosticStage.localDataProgress.timestamp(in: proof) == now.addingTimeInterval(2))
        #expect(SyncPathDiagnosticStage.upload.timestamp(in: proof) == now.addingTimeInterval(3))
        #expect(SyncPathDiagnosticStage.download.timestamp(in: proof) == now.addingTimeInterval(4))
        #expect(SyncPathDiagnosticStage.roundTrip.timestamp(in: proof) == now.addingTimeInterval(4))
    }

    @Test("Evidence from another check, target, or earlier time is rejected")
    func foreignAndOldEvidenceRejected() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)

        proof.observe(.localDataProgress(.init(
            checkID: UUID(), targetID: target, observedAt: now.addingTimeInterval(1)
        )))
        proof.observe(.localDataProgress(.init(
            checkID: checkID,
            targetID: .init(deviceID: "server-b", folderID: "folder-a"),
            observedAt: now.addingTimeInterval(1)
        )))
        proof.observe(.localDataProgress(.init(
            checkID: checkID, targetID: target, observedAt: now.addingTimeInterval(-1)
        )))

        #expect(proof.localDataProgressObservedAt == nil)
    }

    @Test("Only fresh successful file apply events count as local data progress")
    func eventClassification() {
        let fresh = bridgeEvent(
            id: 11,
            type: "ItemFinished",
            time: "2027-01-15T08:00:01Z",
            data: ["folder": "folder-a", "type": "file", "action": "update", "error": ""]
        )
        let startedAt = isoDate("2027-01-15T08:00:00Z")

        #expect(fresh.localDataProgressDate(startedAt: startedAt, cursor: 10) != nil)
        #expect(fresh.localDataProgressDate(startedAt: startedAt, cursor: 11) == nil)
        #expect(bridgeEvent(
            id: 12,
            type: "ItemFinished",
            time: "2027-01-15T08:00:00.500Z",
            data: ["folder": "folder-a", "type": "file", "action": "update"]
        ).localDataProgressDate(
            startedAt: isoDate("2027-01-15T08:00:00.100Z"), cursor: 11
        ) != nil)
        #expect(bridgeEvent(
            id: 13,
            type: "ItemFinished",
            time: "2027-01-15T07:59:59.999Z",
            data: ["folder": "folder-a", "type": "file", "action": "update"]
        ).localDataProgressDate(startedAt: startedAt, cursor: 11) == nil)
        #expect(bridgeEvent(
            id: 14,
            type: "ItemFinished",
            time: "2027-01-15T08:00:01Z",
            data: ["folder": "folder-a", "type": "file", "action": "metadata"]
        ).localDataProgressDate(startedAt: startedAt, cursor: 10) == nil)
        #expect(bridgeEvent(
            id: 15,
            type: "ItemFinished",
            time: "2027-01-15T08:00:01Z",
            data: ["folder": "folder-a", "type": "file", "action": "update", "error": "failed"]
        ).localDataProgressDate(startedAt: startedAt, cursor: 10) == nil)
        #expect(bridgeEvent(
            id: 16,
            type: "RemoteIndexUpdated",
            time: "2027-01-15T08:00:01Z",
            data: ["folder": "folder-a"]
        ).localDataProgressDate(startedAt: startedAt, cursor: 10) == nil)
    }

    @Test("Old v1 timestamp is never reused as v2 local data progress")
    func oldPersistedEvidenceIgnored() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "old-proof")
        defaults.set(now, forKey: "relay-sync-progress-observed-at")

        #expect(RelaySyncProofStore.localDataProgressObservedAt(defaults: defaults) == nil)

        RelaySyncProofStore.markLocalDataProgressObserved(at: now, defaults: defaults)
        #expect(RelaySyncProofStore.localDataProgressObservedAt(defaults: defaults) == now)
    }

    @Test("One folder can progress while another times out")
    func multipleServersAndFoldersRemainPartial() async {
        let script = ScriptedCheckEnvironment(now: isoDate("2027-01-15T08:00:00Z"))
        script.eventResponses = [
            "[]",
            eventsJSON([
                bridgeEvent(
                    id: 1,
                    type: "ItemFinished",
                    time: "2027-01-15T08:00:01Z",
                    data: ["folder": "folder-a", "type": "file", "action": "update"]
                ),
            ]),
            "[]",
        ]
        let session = await SyncPathChecking.run(
            devices: [device("server-a"), device("server-b")],
            folders: [folder("folder-a", devices: ["server-a"]), folder("folder-b", devices: ["server-b"])],
            protectedDataAvailable: true,
            policy: .init(retryDelays: [.zero]),
            environment: script.environment,
            lease: SyncPathCheckLease()
        )

        let a = session.results[.init(deviceID: "server-a", folderID: "folder-a")]
        let b = session.results[.init(deviceID: "server-b", folderID: "folder-b")]
        #expect(a?.status == .localDataProgressObserved)
        #expect(a?.proof.localDataProgressObservedAt != nil)
        #expect(b?.status == .timedOut)
        #expect(b?.proof.localDataProgressObservedAt == nil)
    }

    @Test("Polling is finite and uses the exact bounded backoff")
    func timeoutAndBackoff() async {
        let script = ScriptedCheckEnvironment(now: now)
        script.eventResponses = ["[]", "[]", "[]", "[]"]
        let policy = SyncPathCheckPolicy(retryDelays: [.seconds(2), .seconds(4)])

        let session = await SyncPathChecking.run(
            devices: [device("server-a")],
            folders: [folder("folder-a", devices: ["server-a"])],
            protectedDataAvailable: true,
            policy: policy,
            environment: script.environment,
            lease: SyncPathCheckLease()
        )

        #expect(session.attempt == policy.maximumAttempts)
        #expect(script.sleeps == policy.retryDelays)
        #expect(script.eventCursors == [0, 0, 0, 0])
        #expect(session.results.values.first?.status == .timedOut)
    }

    @Test("Engine generation change interrupts without applying a result")
    func engineRestartInterrupts() async {
        let script = ScriptedCheckEnvironment(now: now)
        script.generations = [4, 4, 4, 5]
        script.eventResponses = ["[]", eventsJSON([
            bridgeEvent(
                id: 1,
                type: "ItemFinished",
                time: "2027-01-15T08:00:01Z",
                data: ["folder": "folder-a", "type": "file", "action": "update"]
            ),
        ])]

        let session = await SyncPathChecking.run(
            devices: [device("server-a")],
            folders: [folder("folder-a", devices: ["server-a"])],
            protectedDataAvailable: true,
            policy: .init(retryDelays: []),
            environment: script.environment,
            lease: SyncPathCheckLease()
        )

        #expect(session.phase == .interrupted)
        #expect(session.results.values.first?.status == .interrupted(.engineRestarted))
        #expect(session.results.values.first?.proof.localDataProgressObservedAt == nil)
    }

    @Test("Protected data and stopped engine interrupt honestly")
    func lifecycleGuards() async {
        let protectedScript = ScriptedCheckEnvironment(now: now)
        let protectedSession = await SyncPathChecking.run(
            devices: [device("server-a")],
            folders: [folder("folder-a", devices: ["server-a"])],
            protectedDataAvailable: false,
            policy: .init(retryDelays: []),
            environment: protectedScript.environment,
            lease: SyncPathCheckLease()
        )
        #expect(protectedSession.results.values.first?.status == .interrupted(.protectedDataUnavailable))

        let stoppedScript = ScriptedCheckEnvironment(now: now)
        stoppedScript.engineRunning = [false]
        let stoppedSession = await SyncPathChecking.run(
            devices: [device("server-a")],
            folders: [folder("folder-a", devices: ["server-a"])],
            protectedDataAvailable: true,
            policy: .init(retryDelays: []),
            environment: stoppedScript.environment,
            lease: SyncPathCheckLease()
        )
        #expect(stoppedSession.results.values.first?.status == .interrupted(.engineStopped))
    }

    @Test("Cancellation stops the poll and infers no progress")
    func taskCancellation() async {
        let script = ScriptedCheckEnvironment(now: now)
        script.eventResponses = ["[]", "[]"]
        let environment = script.environmentWithSleep { _ in
            try await Task.sleep(for: .seconds(60))
        }
        let task = Task {
            await SyncPathChecking.run(
                devices: [device("server-a")],
                folders: [folder("folder-a", devices: ["server-a"])],
                protectedDataAvailable: true,
                policy: .init(retryDelays: [.seconds(60)]),
                environment: environment,
                lease: SyncPathCheckLease()
            )
        }

        try? await Task.sleep(for: .milliseconds(50))
        task.cancel()
        let session = await task.value

        #expect(session.phase == .cancelled)
        #expect(session.results.values.first?.status == .cancelled(.user))
        #expect(session.results.values.first?.proof.localDataProgressObservedAt == nil)
    }

    @Test("Concurrent checks conflict without querying the event stream")
    func concurrentCheckConflict() async {
        let lease = SyncPathCheckLease()
        let heldID = UUID()
        #expect(await lease.acquire(checkID: heldID))
        let script = ScriptedCheckEnvironment(now: now)

        let session = await SyncPathChecking.run(
            devices: [device("server-a")],
            folders: [folder("folder-a", devices: ["server-a"])],
            protectedDataAvailable: true,
            policy: .init(retryDelays: []),
            environment: script.environment,
            lease: lease
        )
        await lease.release(checkID: heldID)

        #expect(session.phase == .conflicting)
        #expect(session.results.values.first?.status == .conflictingCheck)
        #expect(script.eventCursors.isEmpty)
    }

    @Test("Folder and server support matrix stays per target")
    func supportMatrix() {
        let checkID = UUID()
        let results = SyncPathTargetBuilding.makeResults(
            checkID: checkID,
            startedAt: now,
            devices: [
                device("online"),
                device("offline", connected: false),
                device("paused", paused: true),
            ],
            folders: [
                folder("eligible", devices: ["online"]),
                folder("offline-folder", devices: ["offline"]),
                folder("paused-device-folder", devices: ["paused"]),
                folder("paused-folder", paused: true, devices: ["online"]),
                folder("send-only", type: "sendonly", devices: ["online"]),
                folder("encrypted", type: "receiveencrypted", devices: ["online"]),
                folder("multi", devices: ["online", "offline"]),
                folder("unshared", devices: []),
            ]
        )

        #expect(results[.init(deviceID: "online", folderID: "eligible")]?.status == .checking)
        #expect(results[.init(deviceID: "offline", folderID: "offline-folder")]?.status == .unavailable(.deviceOffline))
        #expect(results[.init(deviceID: "paused", folderID: "paused-device-folder")]?.status == .unsupported(.devicePaused))
        #expect(results[.init(deviceID: "online", folderID: "paused-folder")]?.status == .unsupported(.folderPaused))
        #expect(results[.init(deviceID: "online", folderID: "send-only")]?.status == .unsupported(.sendOnlyFolder))
        #expect(results[.init(deviceID: "online", folderID: "encrypted")]?.status == .unsupported(.encryptedFolder))
        #expect(results[.init(deviceID: "online", folderID: "multi")]?.status == .unsupported(.multiplePeers))
        #expect(results[.init(deviceID: nil, folderID: "unshared")]?.status == .unsupported(.folderNotShared))
    }

    @Test("Stale semantics are explicit and boundary-tested")
    func stalePresentation() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let checkID = UUID()
        var proof = SyncPathTargetProof(checkID: checkID, targetID: target, startedAt: now)
        proof.observe(.localDataProgress(.init(checkID: checkID, targetID: target, observedAt: now)))
        let result = SyncPathTargetResult(targetID: target, proof: proof, status: .localDataProgressObserved)

        #expect(SyncPathCheckPresentation.state(for: result, now: now.addingTimeInterval(899)) == .localDataProgressObserved)
        #expect(SyncPathCheckPresentation.state(for: result, now: now.addingTimeInterval(900)) == .stale)
    }

    @Test("A success status without proof cannot render as success")
    func presentationRequiresProofTimestamp() {
        let target = SyncPathTargetID(deviceID: "server-a", folderID: "folder-a")
        let proof = SyncPathTargetProof(checkID: UUID(), targetID: target, startedAt: now)
        let result = SyncPathTargetResult(targetID: target, proof: proof, status: .localDataProgressObserved)

        #expect(SyncPathCheckPresentation.state(for: result, now: now) == .incomplete)
    }

    @Test("The passive check mutates no mapping input")
    func noPathOrMappingMutation() async {
        let devices = [device("server-a"), device("server-b", connected: false)]
        let folders = [
            folder("folder-a", devices: ["server-a"]),
            folder("folder-b", devices: ["server-b"]),
        ]
        let originalDevices = devices
        let originalFolders = folders
        let script = ScriptedCheckEnvironment(now: now)
        script.eventResponses = ["[]", "[]"]

        _ = await SyncPathChecking.run(
            devices: devices,
            folders: folders,
            protectedDataAvailable: true,
            policy: .init(retryDelays: []),
            environment: script.environment,
            lease: SyncPathCheckLease()
        )

        #expect(devices == originalDevices)
        #expect(folders == originalFolders)
    }

    @Test("Persistent local proof contains timestamps only")
    func proofStoreWritesOnlyTimestamps() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "proof-storage-shape")
        RelaySyncProofStore.markBackgroundSyncStarted(at: now, defaults: defaults)
        RelaySyncProofStore.markLocalDataProgressObserved(at: now, defaults: defaults)

        #expect(defaults.object(forKey: "relay-background-sync-started-at") is Date)
        #expect(defaults.object(forKey: "relay-local-data-progress-observed-at-v2") is Date)
        #expect(defaults.string(forKey: "relay-background-sync-started-at") == nil)
        #expect(defaults.string(forKey: "relay-local-data-progress-observed-at-v2") == nil)
    }

    @Test("Normal check copy contains no internal protocol terms")
    func normalCopyAvoidsTechnicalTerms() {
        let forbidden = [
            "event id", "completion api", "sequence id", "marker file", "hmac",
            "nonce", "jws", "apns", "device id", "correlation id",
        ]
        let states: [SyncPathPresentedState] = [
            .checking, .localDataProgressObserved, .stale, .incomplete,
            .cancelled, .interrupted, .unsupported, .unavailable, .conflicting,
        ]

        for state in states {
            let copy = (state.userFacingTitle + " " + state.userFacingDetail).lowercased()
            for term in forbidden {
                #expect(!copy.contains(term))
            }
        }
    }

    private func device(
        _ id: String,
        connected: Bool = true,
        paused: Bool = false
    ) -> SyncPathDeviceSnapshot {
        SyncPathDeviceSnapshot(id: id, connected: connected, paused: paused)
    }

    private func folder(
        _ id: String,
        type: String = "sendreceive",
        paused: Bool = false,
        devices: [String]
    ) -> SyncPathFolderSnapshot {
        SyncPathFolderSnapshot(id: id, type: type, paused: paused, deviceIDs: devices)
    }

    private func bridgeEvent(
        id: Int,
        type: String,
        time: String,
        data: [String: String]? = nil
    ) -> SyncPathBridgeEvent {
        SyncPathBridgeEvent(id: id, type: type, time: time, data: data)
    }

    private func isoDate(_ value: String) -> Date {
        SyncBridgeService.parseBridgeTimestamp(value)!
    }

    private func eventsJSON(_ events: [SyncPathBridgeEvent]) -> String {
        let objects: [[String: Any]] = events.map { event in
            var object: [String: Any] = ["id": event.id, "type": event.type, "time": event.time]
            if let data = event.data { object["data"] = data }
            return object
        }
        let data = try! JSONSerialization.data(withJSONObject: objects, options: [.sortedKeys])
        return String(decoding: data, as: UTF8.self)
    }
}

@MainActor
@Suite("Synchronization-path view lifecycle")
struct RelaySyncPathViewLifecycleTests {
    @Test("Leaving the view cancels the active check and cleanup is idempotent")
    func leavingViewCancelsAndResetIsIdempotent() async throws {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let script = ScriptedCheckEnvironment(now: now)
        script.eventResponses = ["[]", "[]"]
        let environment = script.environmentWithSleep { _ in
            try await Task.sleep(for: .seconds(60))
        }
        let controller = SyncPathCheckController(
            environment: environment,
            policy: .init(retryDelays: [.seconds(60)]),
            lease: SyncPathCheckLease()
        )
        let deviceData = Data(#"[{"deviceID":"server-a","name":"Server","connected":true,"paused":false}]"#.utf8)
        let devices = try JSONDecoder().decode([SyncthingManager.DeviceInfo].self, from: deviceData)
        let folders = [SyncthingManager.FolderInfo(
            id: "folder-a",
            label: "Folder",
            path: "/not-touched",
            type: "sendreceive",
            paused: false,
            deviceIDs: ["server-a"]
        )]

        #expect(controller.start(devices: devices, folders: folders, protectedDataAvailable: true))
        try? await Task.sleep(for: .milliseconds(50))
        controller.cancel(reason: .viewLeft)

        #expect(controller.isRunning)
        #expect(controller.isCancellationPending)
        #expect(controller.session?.phase == .cancelled)
        #expect(controller.session?.results.values.first?.status == .cancelled(.viewLeft))

        await waitUntilStopped(controller)
        #expect(!controller.isRunning)
        #expect(!controller.isCancellationPending)

        controller.reset()
        controller.reset()
        #expect(controller.session == nil)

        let freshController = SyncPathCheckController(
            environment: environment,
            policy: .init(retryDelays: []),
            lease: SyncPathCheckLease()
        )
        #expect(freshController.session == nil)
    }

    @Test("Immediate cancellation cannot race a retry or retain the shared lease")
    func immediateCancellationAndRetry() async throws {
        let script = ScriptedCheckEnvironment(now: Date())
        script.eventResponses = ["[]", "[]", "[]", "[]"]
        let environment = script.environmentWithSleep { _ in
            try await Task.sleep(for: .seconds(60))
        }
        let lease = SyncPathCheckLease()
        let controller = SyncPathCheckController(
            environment: environment,
            policy: .init(retryDelays: [.seconds(60)]),
            lease: lease
        )
        let deviceData = Data(#"[{"deviceID":"server-a","name":"Server","connected":true,"paused":false}]"#.utf8)
        let devices = try JSONDecoder().decode([SyncthingManager.DeviceInfo].self, from: deviceData)
        let folders = [SyncthingManager.FolderInfo(
            id: "folder-a", label: "Folder", path: "/not-touched",
            type: "sendreceive", paused: false, deviceIDs: ["server-a"]
        )]

        #expect(controller.start(devices: devices, folders: folders, protectedDataAvailable: true))
        let firstCheckID = controller.session?.checkID
        controller.cancel(reason: .user)

        #expect(controller.session?.checkID == firstCheckID)
        #expect(controller.session?.phase == .cancelled)
        #expect(!controller.start(devices: devices, folders: folders, protectedDataAvailable: true))

        await waitUntilStopped(controller)
        #expect(controller.start(devices: devices, folders: folders, protectedDataAvailable: true))
        controller.cancel(reason: .user)
        await waitUntilStopped(controller)
        #expect(!controller.isRunning)
    }

    @Test("App lifecycle interruption uses its own cancellation reason")
    func lifecycleCancellationReason() async throws {
        let script = ScriptedCheckEnvironment(now: Date())
        script.eventResponses = ["[]", "[]"]
        let environment = script.environmentWithSleep { _ in
            try await Task.sleep(for: .seconds(60))
        }
        let controller = SyncPathCheckController(
            environment: environment,
            policy: .init(retryDelays: [.seconds(60)]),
            lease: SyncPathCheckLease()
        )
        let deviceData = Data(#"[{"deviceID":"server-a","name":"Server","connected":true,"paused":false}]"#.utf8)
        let devices = try JSONDecoder().decode([SyncthingManager.DeviceInfo].self, from: deviceData)
        let folders = [SyncthingManager.FolderInfo(
            id: "folder-a", label: "Folder", path: "/not-touched",
            type: "sendreceive", paused: false, deviceIDs: ["server-a"]
        )]

        #expect(controller.start(devices: devices, folders: folders, protectedDataAvailable: true))
        try? await Task.sleep(for: .milliseconds(50))
        controller.cancel(reason: .appLifecycle)

        #expect(controller.session?.results.values.first?.status == .cancelled(.appLifecycle))
        await waitUntilStopped(controller)
    }

    private func waitUntilStopped(_ controller: SyncPathCheckController) async {
        for _ in 0..<100 where controller.isRunning {
            try? await Task.sleep(for: .milliseconds(10))
        }
    }
}

private final class ScriptedCheckEnvironment: @unchecked Sendable {
    private let lock = NSLock()
    private let fixedNow: Date
    private var generationIndex = 0
    private var engineIndex = 0
    private var responseIndex = 0
    private var recordedSleeps: [Duration] = []
    private var recordedCursors: [Int] = []

    var generations: [Int64] = [1]
    var engineRunning: [Bool] = [true]
    var eventResponses: [String] = ["[]"]

    init(now: Date) {
        fixedNow = now
    }

    var sleeps: [Duration] { withLock { recordedSleeps } }
    var eventCursors: [Int] { withLock { recordedCursors } }

    var environment: SyncPathCheckEnvironment {
        environmentWithSleep { [weak self] duration in
            guard let self else { return }
            self.withLock { self.recordedSleeps.append(duration) }
        }
    }

    func environmentWithSleep(
        _ sleep: @escaping @Sendable (Duration) async throws -> Void
    ) -> SyncPathCheckEnvironment {
        SyncPathCheckEnvironment(
            now: { [fixedNow] in fixedNow },
            isEngineRunning: { [weak self] in self?.nextEngineState() ?? false },
            eventStreamGeneration: { [weak self] in self?.nextGeneration() ?? 0 },
            eventsSince: { [weak self] cursor in self?.nextEvents(cursor: cursor) ?? "[]" },
            sleep: sleep
        )
    }

    private func nextEngineState() -> Bool {
        withLock {
            defer { engineIndex += 1 }
            return engineRunning[min(engineIndex, engineRunning.count - 1)]
        }
    }

    private func nextGeneration() -> Int64 {
        withLock {
            defer { generationIndex += 1 }
            return generations[min(generationIndex, generations.count - 1)]
        }
    }

    private func nextEvents(cursor: Int) -> String {
        withLock {
            recordedCursors.append(cursor)
            defer { responseIndex += 1 }
            return eventResponses[min(responseIndex, eventResponses.count - 1)]
        }
    }

    @discardableResult
    private func withLock<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }
}

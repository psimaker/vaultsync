import CryptoKit
import Foundation
import Testing
@testable import VaultSync

@Suite("Foreground diagnostics upload runtime (M5)", .serialized)
@MainActor
struct DiagnosticsForegroundUploadRuntimeTests {
    @Test("Explicit run accepts only the exact byte-identical pinned query chain")
    func exactForegroundUpload() async throws {
        let identifier = UUID().uuidString.lowercased()
        let support = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-upload-support-\(identifier)", isDirectory: true)
        let folder = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-upload-folder-\(identifier)", isDirectory: true)
        let keychain = InMemoryDiagnosticsKeychain()
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.upload.tests.\(identifier)",
            keychain: keychain
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
            try? FileManager.default.removeItem(at: folder)
        }
        _ = try store.installationCredential()

        let uploadFixture = try M5UploadFixtureLoader.load()
        let appSeed = try Data(m1Hex: uploadFixture.appSeedHex)
        let helperKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(m1Hex: uploadFixture.helperSeedHex)
        )
        var record = makeRuntimeRecord(
            appSeed: appSeed,
            helperPublic: helperKey.publicKey.rawRepresentation,
            homeserver: try Data(m1Hex: uploadFixture.homeserverBindingHex),
            folder: try Data(m1Hex: uploadFixture.folderBindingHex),
            folderID: "foreground-upload",
            appEpoch: uploadFixture.appEpoch,
            helperEpoch: uploadFixture.helperEpoch
        )
        let wall = Date(timeIntervalSince1970: TimeInterval(uploadFixture.requestIssuedAt))
        let appKey = try Curve25519.Signing.PrivateKey(rawRepresentation: appSeed)
        let enablement = try DiagnosticsNamespaceProtocol.makeEnablement(
            record: record,
            appKey: appKey,
            nonce: Data(repeating: 0x19, count: 32),
            now: wall
        )
        let m4Fixture = try loadDiagnosticsHexFixture(named: "diagnostics-namespace-m4")
        let goldenRoot = try DiagnosticsDeterministicCBOR.decode(
            Data(m1Hex: #require(m4Fixture["02_root_manifest"]))
        )
        let rootData = try makeNamespaceRoot(
            enablement: enablement,
            record: record,
            helperKey: helperKey,
            readmeDigest: try #require(goldenRoot.bytes(for: 29, count: 32)),
            createdAt: uploadFixture.requestIssuedAt + 1
        )
        let root = try DiagnosticsNamespaceProtocol.validateRootManifest(
            rootData,
            enablement: enablement,
            record: record
        )
        let candidate = try DiagnosticsNamespaceProtocol.makeInitialAuthorization(
            record: record,
            root: root,
            appKey: appKey,
            nonce: Data(repeating: 0x30, count: 32),
            now: wall.addingTimeInterval(2)
        )
        let completed = try countersignInitialAuthorization(candidate.message, helperKey: helperKey)
        let authorizationDigest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorization(
            completed,
            candidate: candidate,
            record: record,
            root: root
        )
        record.state = .namespaceActive
        record.lastOutgoing = candidate.message
        record.lastIncoming = completed
        record.namespaceID = root.namespaceID
        record.namespaceInitialAppKeyID = record.appKeyID
        record.namespaceEnablement = enablement
        record.namespaceRootDigest = root.rootDigest
        record.namespaceManifestDigest = root.manifestDigest
        record.namespaceManifestEpoch = record.helperEpoch
        record.namespaceAuthorizationDigest = authorizationDigest
        record.namespaceAuthorizationEpoch = 1
        try store.save(record)

        let namespace = folder.appendingPathComponent(
            DiagnosticsNamespaceProtocol.rootName,
            isDirectory: true
        )
        try FileManager.default.createDirectory(at: namespace, withIntermediateDirectories: true)
        try rootData.write(
            to: namespace.appendingPathComponent(DiagnosticsNamespaceProtocol.rootManifestName),
            options: .atomic
        )
        let authorizationRelative = try DiagnosticsNamespaceProtocol.authorizationRelativePath(
            installationBinding: candidate.installationBinding
        )
        let authorizationURL = namespace.appendingPathComponent(authorizationRelative)
        try FileManager.default.createDirectory(
            at: authorizationURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try completed.write(to: authorizationURL, options: .atomic)
        let operationDirectory = namespace
            .appendingPathComponent("installations", isDirectory: true)
            .appendingPathComponent(
                DiagnosticsNamespaceProtocol.base32LowerNoPadding(candidate.installationBinding),
                isDirectory: true
            )
            .appendingPathComponent("operations", isDirectory: true)
        try FileManager.default.createDirectory(
            at: operationDirectory,
            withIntermediateDirectories: true
        )

        let clock = LockedDiagnosticsClock(wall, continuous: 1_000)
        let requestBox = LockedUploadRequestBox()
        let responseComponents = try DiagnosticsNamespaceProtocol.operationResponseComponents(
            installationBinding: candidate.installationBinding,
            operationID: try Data(m1Hex: uploadFixture.operationIdHex)
        )
        let responseRelativePath = responseComponents.joined(separator: "/")
        let eventBox = LockedDownloadEventBox()
        let happyRecord = record
        let happyFolder = folder
        let transport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { requestBox.value() },
            acceptAfter: 2,
            respond: { authorization in
                let response = try makeHelperResponseArtifact(
                    authorization: authorization,
                    record: happyRecord,
                    helperKey: helperKey,
                    now: clock.value()
                )
                let url = responseComponents.reduce(happyFolder) {
                    $0.appendingPathComponent($1)
                }
                try response.write(to: url, options: .atomic)
                eventBox.append(DiagnosticsResponseProtocol.DownloadEvent(
                    id: eventBox.nextID(),
                    type: "ItemFinished",
                    time: iso8601WithNanoseconds(clock.value().addingTimeInterval(0.5)),
                    data: [
                        "folder": happyRecord.folderID,
                        "item": responseRelativePath,
                        "type": "file",
                        "action": "update",
                        "error": "",
                    ]
                ))
            }
        )
        let random = LockedUploadRandom(values: [
            try Data(m1Hex: uploadFixture.operationIdHex),
            try Data(m1Hex: uploadFixture.requestNonceHex),
            try Data(m1Hex: uploadFixture.queryNonceHex),
            try Data(m1Hex: uploadFixture.requestPayloadHex),
            Data(repeating: 0x45, count: 32),
        ])
        let controller = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { clock.value() },
            continuousNow: { clock.continuousValue() },
            uploadRandomBytes: { try random.next(count: $0) },
            uploadFileWriter: { path, components, data in
                try DiagnosticsUploadFileStore.createImmutable(
                    folderPath: path,
                    components: components,
                    data: data
                )
                requestBox.set(data)
            },
            uploadSleep: {
                clock.advance(by: TimeInterval($0))
                await Task.yield()
            }
        )
        controller.refresh()
        await controller.checkCapability(recordID: record.id)
        #expect(controller.capabilityStates[record.id] == .available)

        controller.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                DiagnosticsUploadPreflight(
                    folderID: record.folderID,
                    folderPath: folder.path,
                    peerID: record.homeserverDeviceID,
                    engineGeneration: 7,
                    engineRunning: true,
                    pathsSettled: true,
                    folderMode: "sendreceive",
                    folderPaused: false,
                    folderHealthy: true,
                    designatedPeerIDs: [record.homeserverDeviceID],
                    peerConnected: true,
                    peerPaused: false,
                    pathOverlap: false,
                    namespacePathAllowed: true,
                    operationSlotEmpty: requireEmptySlot
                )
            },
            rescan: { true },
            events: { sinceID in
                DiagnosticsResponseProtocol.DownloadEventSnapshot(
                    generation: 7,
                    events: eventBox.events(after: sinceID)
                )
            }
        )
        await waitForTerminalUpload(controller: controller, recordID: record.id)

        let status = try #require(controller.uploadStatuses[record.id])
        #expect(status.phase == .downloadObserved)
        #expect(status.evidence.uploadObserved)
        #expect(status.evidence.downloadObserved)
        #expect(!status.evidence.roundtripConfirmed)
        #expect(status.completedPolls == 2)
        #expect(status.completedResponsePolls == 1)
        let queries = await transport.uploadQueries()
        #expect(queries.count == 2)
        #expect(queries[0] == queries[1])
        #expect(requestBox.value() != nil)
        let authorizations = await transport.responseAuthorizations()
        #expect(authorizations.count == 1)

        let lateRequestBox = LockedUploadRequestBox()
        let lateTransport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { lateRequestBox.value() },
            acceptAfter: 1,
            holdAcceptedResponse: true
        )
        let lateRandom = LockedUploadRandom(values: [
            Data(repeating: 0x41, count: 32),
            Data(repeating: 0x42, count: 32),
            Data(repeating: 0x43, count: 32),
            Data(repeating: 0x44, count: DiagnosticsUploadProtocol.payloadByteCount),
        ])
        let lateController = makeUploadController(
            store: store,
            transport: lateTransport,
            clock: clock,
            random: lateRandom,
            requestBox: lateRequestBox
        )
        lateController.refresh()
        await lateController.checkCapability(recordID: record.id)
        lateController.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                self.validPreflight(
                    record: record,
                    folderPath: folder.path,
                    requireEmptySlot: requireEmptySlot
                )
            },
            rescan: { true },
            events: self.emptyDownloadEvents
        )
        await waitForHeldResponse(lateTransport)
        #expect(await lateTransport.isHoldingAcceptedResponse())
        lateController.cancelForegroundUpload(recordID: record.id)
        #expect(lateController.uploadStatuses[record.id]?.phase == .cancelled)
        await lateTransport.releaseAcceptedResponse()
        await waitForReturnedResponse(lateTransport)
        for _ in 0..<100 { await Task.yield() }
        #expect(lateController.uploadStatuses[record.id]?.phase == .cancelled)
        #expect(lateController.uploadStatuses[record.id]?.evidence.uploadObserved == false)

        let restartRequestBox = LockedUploadRequestBox()
        let restartTransport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { restartRequestBox.value() },
            acceptAfter: 1,
            holdAcceptedResponse: true
        )
        let restartRandom = LockedUploadRandom(values: [
            Data(repeating: 0x51, count: 32),
            Data(repeating: 0x52, count: 32),
            Data(repeating: 0x53, count: 32),
            Data(repeating: 0x54, count: DiagnosticsUploadProtocol.payloadByteCount),
        ])
        let restartController = makeUploadController(
            store: store,
            transport: restartTransport,
            clock: clock,
            random: restartRandom,
            requestBox: restartRequestBox
        )
        restartController.refresh()
        await restartController.checkCapability(recordID: record.id)
        restartController.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                self.validPreflight(
                    record: record,
                    folderPath: folder.path,
                    requireEmptySlot: requireEmptySlot
                )
            },
            rescan: { true },
            events: self.emptyDownloadEvents
        )
        await waitForHeldResponse(restartTransport)
        restartController.refresh()
        #expect(restartController.uploadStatuses[record.id] == nil)
        await restartTransport.releaseAcceptedResponse()
        await waitForReturnedResponse(restartTransport)
        for _ in 0..<100 { await Task.yield() }
        #expect(restartController.uploadStatuses[record.id] == nil)
        #expect(restartController.lastError == nil)

        let racedRequestBox = LockedUploadRequestBox()
        let racedTransport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { racedRequestBox.value() },
            acceptAfter: 1,
            holdAcceptedResponse: true
        )
        let racedOperationID = Data(repeating: 0x81, count: 32)
        let racedController = makeUploadController(
            store: store,
            transport: racedTransport,
            clock: clock,
            random: LockedUploadRandom(values: [
                racedOperationID,
                Data(repeating: 0x82, count: 32),
                Data(repeating: 0x83, count: 32),
                Data(repeating: 0x84, count: DiagnosticsUploadProtocol.payloadByteCount),
            ]),
            requestBox: racedRequestBox
        )
        racedController.refresh()
        await racedController.checkCapability(recordID: record.id)
        racedController.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                self.validPreflight(
                    record: record,
                    folderPath: folder.path,
                    requireEmptySlot: requireEmptySlot
                )
            },
            rescan: { true },
            events: self.emptyDownloadEvents
        )
        await waitForHeldResponse(racedTransport)
        let racedComponents = try DiagnosticsNamespaceProtocol.operationRequestComponents(
            installationBinding: candidate.installationBinding,
            operationID: racedOperationID
        )
        let racedRequestURL = racedComponents.reduce(folder) {
            $0.appendingPathComponent($1)
        }
        try Data([0xa0]).write(to: racedRequestURL)
        await racedTransport.releaseAcceptedResponse()
        await waitForReturnedResponse(racedTransport)
        await waitForTerminalUpload(controller: racedController, recordID: record.id)
        #expect(racedController.uploadStatuses[record.id]?.phase == .conflict)
        #expect(racedController.uploadStatuses[record.id]?.evidence.uploadObserved == false)

        let rateRequestBox = LockedUploadRequestBox()
        let rateTransport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { rateRequestBox.value() },
            acceptAfter: 1
        )
        var rateValues: [Data] = []
        for operation in 0..<4 {
            let first = UInt8(0x61 + operation * 5)
            rateValues.append(Data(repeating: first, count: 32))
            rateValues.append(Data(repeating: first + 1, count: 32))
            rateValues.append(Data(repeating: first + 2, count: 32))
            rateValues.append(Data(
                repeating: first + 3,
                count: DiagnosticsUploadProtocol.payloadByteCount
            ))
            rateValues.append(Data(repeating: first + 4, count: 32))
        }
        let rateController = makeUploadController(
            store: store,
            transport: rateTransport,
            clock: clock,
            random: LockedUploadRandom(values: rateValues),
            requestBox: rateRequestBox
        )
        rateController.refresh()
        for _ in 0..<3 {
            await rateController.checkCapability(recordID: record.id)
            rateController.beginForegroundUpload(
                recordID: record.id,
                preflight: { _, _, requireEmptySlot in
                    self.validPreflight(
                        record: record,
                        folderPath: folder.path,
                        requireEmptySlot: requireEmptySlot
                    )
                },
                rescan: { true },
                events: self.emptyDownloadEvents
            )
            await waitForTerminalUpload(controller: rateController, recordID: record.id)
            let rateStatus = rateController.uploadStatuses[record.id]
            #expect(rateStatus?.phase == .timedOut)
            #expect(rateStatus?.evidence.uploadObserved == true)
            #expect(rateStatus?.evidence.downloadObserved == false)
        }
        #expect(rateRequestBox.count() == 3)
        await rateController.checkCapability(recordID: record.id)
        rateController.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                self.validPreflight(
                    record: record,
                    folderPath: folder.path,
                    requireEmptySlot: requireEmptySlot
                )
            },
            rescan: { true },
            events: self.emptyDownloadEvents
        )
        await waitForTerminalUpload(controller: rateController, recordID: record.id)
        #expect(rateController.uploadStatuses[record.id]?.phase == .rateLimited)
        #expect(rateController.uploadStatuses[record.id]?.evidence.uploadObserved == false)
        #expect(rateRequestBox.count() == 3)
        #expect(await rateTransport.uploadQueries().count == 3)

        let timeoutRequestBox = LockedUploadRequestBox()
        let timeoutTransport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { timeoutRequestBox.value() },
            acceptAfter: 99
        )
        let timeoutController = makeUploadController(
            store: store,
            transport: timeoutTransport,
            clock: clock,
            random: LockedUploadRandom(values: [
                Data(repeating: 0x79, count: 32),
                Data(repeating: 0x7a, count: 32),
                Data(repeating: 0x7b, count: 32),
                Data(repeating: 0x7c, count: DiagnosticsUploadProtocol.payloadByteCount),
            ]),
            requestBox: timeoutRequestBox
        )
        timeoutController.refresh()
        await timeoutController.checkCapability(recordID: record.id)
        timeoutController.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                self.validPreflight(
                    record: record,
                    folderPath: folder.path,
                    requireEmptySlot: requireEmptySlot
                )
            },
            rescan: { true },
            events: self.emptyDownloadEvents
        )
        await waitForTerminalUpload(controller: timeoutController, recordID: record.id)
        let timeout = try #require(timeoutController.uploadStatuses[record.id])
        #expect(timeout.phase == .timedOut)
        #expect(timeout.completedPolls == DiagnosticsUploadProtocol.pollDelays.count)
        #expect(!timeout.evidence.uploadObserved)
        let timeoutQueries = await timeoutTransport.uploadQueries()
        #expect(timeoutQueries.count == DiagnosticsUploadProtocol.pollDelays.count)
        #expect(Set(timeoutQueries).count == 1)

        let rejectedRequestBox = LockedUploadRequestBox()
        let rejectedTransport = ForegroundUploadTransport(
            record: record,
            helperKey: helperKey,
            clock: clock,
            request: { rejectedRequestBox.value() },
            acceptAfter: 1
        )
        let rejectedController = makeUploadController(
            store: store,
            transport: rejectedTransport,
            clock: clock,
            random: LockedUploadRandom(values: [
                Data(repeating: 0x7d, count: 32),
                Data(repeating: 0x7e, count: 32),
                Data(repeating: 0x7f, count: 32),
                Data(repeating: 0x80, count: DiagnosticsUploadProtocol.payloadByteCount),
            ]),
            requestBox: rejectedRequestBox
        )
        rejectedController.refresh()
        await rejectedController.checkCapability(recordID: record.id)
        rejectedController.beginForegroundUpload(
            recordID: record.id,
            preflight: { _, _, requireEmptySlot in
                let valid = self.validPreflight(
                    record: record,
                    folderPath: folder.path,
                    requireEmptySlot: requireEmptySlot
                )
                return DiagnosticsUploadPreflight(
                    folderID: valid.folderID,
                    folderPath: valid.folderPath,
                    peerID: valid.peerID,
                    engineGeneration: valid.engineGeneration,
                    engineRunning: valid.engineRunning,
                    pathsSettled: valid.pathsSettled,
                    folderMode: valid.folderMode,
                    folderPaused: valid.folderPaused,
                    folderHealthy: valid.folderHealthy,
                    designatedPeerIDs: [valid.peerID, "ambiguous-peer"],
                    peerConnected: valid.peerConnected,
                    peerPaused: valid.peerPaused,
                    pathOverlap: valid.pathOverlap,
                    namespacePathAllowed: valid.namespacePathAllowed,
                    operationSlotEmpty: valid.operationSlotEmpty
                )
            },
            rescan: { true },
            events: self.emptyDownloadEvents
        )
        await waitForTerminalUpload(controller: rejectedController, recordID: record.id)
        #expect(rejectedController.uploadStatuses[record.id]?.phase == .unsupported)
        #expect(rejectedRequestBox.count() == 0)
        #expect(await rejectedTransport.uploadQueries().isEmpty)
    }

    @Test("Preflight rejects ambiguous peers and unavailable connectivity before any artifact")
    func strictPreflight() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let appKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(m1Hex: fixture.appSeedHex)
        )
        let helperKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(m1Hex: fixture.helperSeedHex)
        )
        var record = makeRuntimeRecord(
            appSeed: appKey.rawRepresentation,
            helperPublic: helperKey.publicKey.rawRepresentation,
            homeserver: try Data(m1Hex: fixture.homeserverBindingHex),
            folder: try Data(m1Hex: fixture.folderBindingHex)
        )
        record.state = .namespaceActive
        let valid = DiagnosticsUploadPreflight(
            folderID: record.folderID,
            folderPath: "/tmp/exact-folder",
            peerID: record.homeserverDeviceID,
            engineGeneration: 1,
            engineRunning: true,
            pathsSettled: true,
            folderMode: "sendreceive",
            folderPaused: false,
            folderHealthy: true,
            designatedPeerIDs: [record.homeserverDeviceID],
            peerConnected: true,
            peerPaused: false,
            pathOverlap: false,
            namespacePathAllowed: true,
            operationSlotEmpty: true
        )
        try valid.validate(record: record, requireEmptySlot: true)

        let ambiguous = DiagnosticsUploadPreflight(
            folderID: valid.folderID,
            folderPath: valid.folderPath,
            peerID: valid.peerID,
            engineGeneration: valid.engineGeneration,
            engineRunning: true,
            pathsSettled: true,
            folderMode: "sendreceive",
            folderPaused: false,
            folderHealthy: true,
            designatedPeerIDs: [valid.peerID, "another-peer"],
            peerConnected: true,
            peerPaused: false,
            pathOverlap: false,
            namespacePathAllowed: true,
            operationSlotEmpty: true
        )
        #expect(throws: DiagnosticsProtocolError.unsupported) {
            try ambiguous.validate(record: record, requireEmptySlot: true)
        }

        let disconnected = DiagnosticsUploadPreflight(
            folderID: valid.folderID,
            folderPath: valid.folderPath,
            peerID: valid.peerID,
            engineGeneration: valid.engineGeneration,
            engineRunning: true,
            pathsSettled: true,
            folderMode: "sendreceive",
            folderPaused: false,
            folderHealthy: true,
            designatedPeerIDs: [valid.peerID],
            peerConnected: false,
            peerPaused: false,
            pathOverlap: false,
            namespacePathAllowed: true,
            operationSlotEmpty: true
        )
        #expect(throws: DiagnosticsProtocolError.unavailable) {
            try disconnected.validate(record: record, requireEmptySlot: true)
        }
    }

    private func waitForTerminalUpload(
        controller: DiagnosticsPairingController,
        recordID: String
    ) async {
        for _ in 0..<10_000 {
            if let phase = controller.uploadStatuses[recordID]?.phase,
               ![.preflighting, .checking, .uploadObserved].contains(phase) {
                return
            }
            await Task.yield()
        }
        Issue.record("foreground upload task did not reach a terminal state")
    }

    private func makeUploadController(
        store: DiagnosticsCredentialStore,
        transport: ForegroundUploadTransport,
        clock: LockedDiagnosticsClock,
        random: LockedUploadRandom,
        requestBox: LockedUploadRequestBox
    ) -> DiagnosticsPairingController {
        DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { clock.value() },
            continuousNow: { clock.continuousValue() },
            uploadRandomBytes: { try random.next(count: $0) },
            uploadFileWriter: { path, components, data in
                try DiagnosticsUploadFileStore.createImmutable(
                    folderPath: path,
                    components: components,
                    data: data
                )
                requestBox.set(data)
            },
            uploadSleep: {
                clock.advance(by: TimeInterval($0))
                await Task.yield()
            }
        )
    }

    private func validPreflight(
        record: DiagnosticsPairingRecord,
        folderPath: String,
        requireEmptySlot: Bool
    ) -> DiagnosticsUploadPreflight {
        DiagnosticsUploadPreflight(
            folderID: record.folderID,
            folderPath: folderPath,
            peerID: record.homeserverDeviceID,
            engineGeneration: 7,
            engineRunning: true,
            pathsSettled: true,
            folderMode: "sendreceive",
            folderPaused: false,
            folderHealthy: true,
            designatedPeerIDs: [record.homeserverDeviceID],
            peerConnected: true,
            peerPaused: false,
            pathOverlap: false,
            namespacePathAllowed: true,
            operationSlotEmpty: requireEmptySlot
        )
    }

    private func emptyDownloadEvents(
        _ sinceID: Int64
    ) -> DiagnosticsResponseProtocol.DownloadEventSnapshot? {
        DiagnosticsResponseProtocol.DownloadEventSnapshot(generation: 7, events: [])
    }

    private func waitForHeldResponse(_ transport: ForegroundUploadTransport) async {
        for _ in 0..<1_000 {
            if await transport.isHoldingAcceptedResponse() { return }
            await Task.yield()
        }
        Issue.record("upload transport did not hold the accepted response")
    }

    private func waitForReturnedResponse(_ transport: ForegroundUploadTransport) async {
        for _ in 0..<1_000 {
            if await transport.hasReturnedAcceptedResponse() { return }
            await Task.yield()
        }
        Issue.record("upload transport did not return the accepted response")
    }
}

final class LockedDownloadEventBox: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [DiagnosticsResponseProtocol.DownloadEvent] = []
    private var lastID: Int64 = 0

    func nextID() -> Int64 {
        lock.lock()
        defer { lock.unlock() }
        lastID += 1
        return lastID
    }

    func append(_ event: DiagnosticsResponseProtocol.DownloadEvent) {
        lock.lock()
        events.append(event)
        lock.unlock()
    }

    func events(after id: Int64) -> [DiagnosticsResponseProtocol.DownloadEvent] {
        lock.lock()
        defer { lock.unlock() }
        return events.filter { $0.id > id }
    }
}

func iso8601WithNanoseconds(_ date: Date) -> String {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return formatter.string(from: date)
}

func makeHelperResponseArtifact(
    authorization authorizationBytes: Data,
    record: DiagnosticsPairingRecord,
    helperKey: Curve25519.Signing.PrivateKey,
    now: Date,
    payload: Data? = nil,
    nonce: Data = Data(repeating: 0x66, count: 32),
    tamperSignature: Bool = false
) throws -> Data {
    let authorization = try DiagnosticsResponseProtocol.decode(authorizationBytes, record: record)
    guard let operationID = authorization.value.bytes(for: 11, count: 32),
          let requestDigest = authorization.value.bytes(for: 17, count: 32),
          let attestationDigest = authorization.value.bytes(for: 20, count: 32),
          let expiry = authorization.value.unsigned(for: 13) else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    let issued = UInt64(now.timeIntervalSince1970.rounded(.down))
    let responsePayload = payload
        ?? Data(repeating: 0x77, count: DiagnosticsUploadProtocol.payloadByteCount)
    let value = DiagnosticsCBORValue.map([
        DiagnosticsCBORField(label: 1, value: .text(DiagnosticsUploadProtocol.capability)),
        DiagnosticsCBORField(label: 2, value: .unsigned(1)),
        DiagnosticsCBORField(label: 3, value: .unsigned(1)),
        DiagnosticsCBORField(label: 4, value: .unsigned(7)),
        DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
        DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
        DiagnosticsCBORField(label: 7, value: .bytes(record.appKeyID)),
        DiagnosticsCBORField(label: 8, value: .bytes(record.helperKeyID)),
        DiagnosticsCBORField(label: 9, value: .unsigned(record.appEpoch)),
        DiagnosticsCBORField(label: 10, value: .unsigned(record.helperEpoch)),
        DiagnosticsCBORField(label: 11, value: .bytes(operationID)),
        DiagnosticsCBORField(label: 12, value: .unsigned(issued)),
        DiagnosticsCBORField(label: 13, value: .unsigned(expiry)),
        DiagnosticsCBORField(label: 17, value: .bytes(requestDigest)),
        DiagnosticsCBORField(label: 20, value: .bytes(attestationDigest)),
        DiagnosticsCBORField(label: 22, value: .bytes(authorization.digest)),
        DiagnosticsCBORField(label: 23, value: .bytes(nonce)),
        DiagnosticsCBORField(label: 24, value: .bytes(responsePayload)),
        DiagnosticsCBORField(label: 25, value: .bytes(DiagnosticsCrypto.sha256(responsePayload))),
    ])
    let body = try DiagnosticsDeterministicCBOR.encode(value)
    var input = Data("eu.vaultsync.roundtrip/v1/response-artifact\0".utf8)
    input.append(body)
    var signature = try helperKey.signature(for: input)
    if tamperSignature {
        signature[0] ^= 0x01
    }
    guard case .map(var fields) = value else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    fields.append(DiagnosticsCBORField(label: 255, value: .bytes(signature)))
    return try DiagnosticsDeterministicCBOR.encode(.map(fields))
}

final class LockedUploadRequestBox: @unchecked Sendable {
    private let lock = NSLock()
    private var request: Data?
    private var writes = 0

    func set(_ value: Data) {
        lock.lock()
        request = value
        writes += 1
        lock.unlock()
    }

    func value() -> Data? {
        lock.lock()
        defer { lock.unlock() }
        return request
    }

    func count() -> Int {
        lock.lock()
        defer { lock.unlock() }
        return writes
    }
}

final class LockedUploadRandom: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [Data]

    init(values: [Data]) {
        self.values = values
    }

    func next(count: Int) throws -> Data {
        lock.lock()
        defer { lock.unlock() }
        guard !values.isEmpty else { throw DiagnosticsProtocolError.unavailable }
        let value = values.removeFirst()
        guard value.count == count else { throw DiagnosticsProtocolError.invalidMessage }
        return value
    }
}

actor ForegroundUploadTransport: DiagnosticsTransporting {
    private let record: DiagnosticsPairingRecord
    private let helperKey: Curve25519.Signing.PrivateKey
    private let clock: LockedDiagnosticsClock
    private let request: @Sendable () -> Data?
    private let acceptAfter: Int
    private let holdAcceptedResponse: Bool
    private let respond: (@Sendable (Data) async throws -> Void)?
    private var queries: [Data] = []
    private var authorizations: [Data] = []
    private var acceptedResponseContinuation: CheckedContinuation<Void, Never>?
    private var acceptedResponseReleased = false
    private var holdingAcceptedResponse = false
    private var returnedAcceptedResponse = false

    init(
        record: DiagnosticsPairingRecord,
        helperKey: Curve25519.Signing.PrivateKey,
        clock: LockedDiagnosticsClock,
        request: @escaping @Sendable () -> Data?,
        acceptAfter: Int,
        holdAcceptedResponse: Bool = false,
        respond: (@Sendable (Data) async throws -> Void)? = nil
    ) {
        self.record = record
        self.helperKey = helperKey
        self.clock = clock
        self.request = request
        self.acceptAfter = acceptAfter
        self.holdAcceptedResponse = holdAcceptedResponse
        self.respond = respond
    }

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        switch path {
        case DiagnosticsCapabilityProtocol.path:
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return try makeCapabilityResponseForQuery(body, helperKey: helperKey)
        case DiagnosticsResponseProtocol.path:
            guard !responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            authorizations.append(body)
            try await respond?(body)
            return nil
        case DiagnosticsUploadProtocol.path:
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            queries.append(body)
            guard queries.count >= acceptAfter else { return nil }
            guard let requestBytes = request() else { throw DiagnosticsProtocolError.unavailable }
            if holdAcceptedResponse, !acceptedResponseReleased {
                holdingAcceptedResponse = true
                await withCheckedContinuation { continuation in
                    if acceptedResponseReleased {
                        continuation.resume()
                    } else {
                        acceptedResponseContinuation = continuation
                    }
                }
                holdingAcceptedResponse = false
            }
            let requestMessage = try DiagnosticsUploadProtocol.decode(requestBytes, record: record)
            let queryMessage = try DiagnosticsUploadProtocol.decode(body, record: record)
            let now = UInt64(clock.value().timeIntervalSince1970.rounded(.down))
            let requestExpiry = try #require(requestMessage.value.unsigned(for: 13))
            let queryExpiry = try #require(queryMessage.value.unsigned(for: 13))
            let expiry = min(
                min(requestExpiry, queryExpiry),
                now + DiagnosticsUploadProtocol.maximumLifetime
            )
            let value = DiagnosticsCBORValue.map([
                DiagnosticsCBORField(label: 1, value: .text(DiagnosticsUploadProtocol.capability)),
                DiagnosticsCBORField(label: 2, value: .unsigned(1)),
                DiagnosticsCBORField(label: 3, value: .unsigned(1)),
                DiagnosticsCBORField(label: 4, value: .unsigned(5)),
                DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
                DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
                DiagnosticsCBORField(label: 7, value: .bytes(record.appKeyID)),
                DiagnosticsCBORField(label: 8, value: .bytes(record.helperKeyID)),
                DiagnosticsCBORField(label: 9, value: .unsigned(record.appEpoch)),
                DiagnosticsCBORField(label: 10, value: .unsigned(record.helperEpoch)),
                DiagnosticsCBORField(
                    label: 11,
                    value: .bytes(try #require(queryMessage.value.bytes(for: 11, count: 32)))
                ),
                DiagnosticsCBORField(label: 12, value: .unsigned(now)),
                DiagnosticsCBORField(label: 13, value: .unsigned(expiry)),
                DiagnosticsCBORField(
                    label: 16,
                    value: .bytes(try #require(requestMessage.value.bytes(for: 16, count: 32)))
                ),
                DiagnosticsCBORField(label: 17, value: .bytes(requestMessage.digest)),
                DiagnosticsCBORField(
                    label: 18,
                    value: .bytes(DiagnosticsCrypto.sha256(requestMessage.digest))
                ),
                DiagnosticsCBORField(label: 19, value: .unsigned(now)),
                DiagnosticsCBORField(
                    label: 30,
                    value: .bytes(try #require(queryMessage.value.bytes(for: 30, count: 32)))
                ),
                DiagnosticsCBORField(label: 31, value: .bytes(queryMessage.digest)),
            ])
            let unsigned = try DiagnosticsDeterministicCBOR.encode(value)
            var input = Data("eu.vaultsync.roundtrip/v1/upload-attestation\0".utf8)
            input.append(unsigned)
            guard case .map(var fields) = value else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            fields.append(DiagnosticsCBORField(
                label: 255,
                value: .bytes(try helperKey.signature(for: input))
            ))
            let response = try DiagnosticsDeterministicCBOR.encode(.map(fields))
            returnedAcceptedResponse = true
            return response
        default:
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    func uploadQueries() -> [Data] { queries }

    func responseAuthorizations() -> [Data] { authorizations }

    func isHoldingAcceptedResponse() -> Bool { holdingAcceptedResponse }

    func releaseAcceptedResponse() {
        acceptedResponseReleased = true
        let continuation = acceptedResponseContinuation
        acceptedResponseContinuation = nil
        continuation?.resume()
    }

    func hasReturnedAcceptedResponse() -> Bool { returnedAcceptedResponse }
}

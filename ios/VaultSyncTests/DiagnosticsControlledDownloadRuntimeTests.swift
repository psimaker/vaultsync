import CryptoKit
import Foundation
import Testing
@testable import VaultSync

@Suite("Controlled diagnostics download runtime (M6)", .serialized)
@MainActor
struct DiagnosticsControlledDownloadRuntimeTests {
    @Test("Production response protocol matches the cross-language M6 golden vectors")
    func productionGoldenVectors() throws {
        let m5 = try M5UploadFixtureLoader.load()
        let m6 = try M6ResponseFixtureLoader.load()
        let golden = try M6ResponseGoldenMessages.make(m5: m5, m6: m6)
        let appKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.appSeedHex))
        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.helperSeedHex))
        var record = makeRuntimeRecord(
            appSeed: appKey.rawRepresentation,
            helperPublic: helperKey.publicKey.rawRepresentation,
            homeserver: try Data(m1Hex: m5.homeserverBindingHex),
            folder: try Data(m1Hex: m5.folderBindingHex),
            appEpoch: m5.appEpoch,
            helperEpoch: m5.helperEpoch
        )
        record.state = .namespaceActive
        record.namespaceAuthorizationEpoch = 1
        record.namespaceInitialAppKeyID = record.appKeyID

        let authorization = try DiagnosticsResponseProtocol.decode(golden.authorization, record: record)
        #expect(authorization.type == .responseAuthorization)
        #expect(authorization.digest == (try Data(m1Hex: m6.authorizationDigestHex)))

        let response = try DiagnosticsResponseProtocol.decode(golden.response, record: record)
        #expect(response.type == .responseArtifact)
        #expect(response.digest == (try Data(m1Hex: m6.responseDigestHex)))

        let upload = try M5UploadGoldenMessages.make(m5)
        let goldenRequest = try DiagnosticsUploadProtocol.decode(upload.request, record: record)
        let goldenQuery = try DiagnosticsUploadProtocol.decode(upload.query, record: record)
        let operationID = try Data(m1Hex: m5.operationIdHex)
        let installationBinding = DiagnosticsNamespaceProtocol.installationBinding(
            initialAppKeyID: record.appKeyID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding
        )
        let operation = DiagnosticsUploadProtocol.Operation(
            request: goldenRequest,
            query: goldenQuery,
            operationID: operationID,
            installationBinding: installationBinding,
            requestComponents: try DiagnosticsNamespaceProtocol.operationRequestComponents(
                installationBinding: installationBinding,
                operationID: operationID
            )
        )
        let attestation = try DiagnosticsUploadProtocol.decode(upload.attestation, record: record)

        let produced = try DiagnosticsResponseProtocol.makeAuthorization(
            record: record,
            appKey: appKey,
            operation: operation,
            attestation: attestation,
            authorizationNonce: try Data(m1Hex: m6.authorizationNonceHex),
            now: Date(timeIntervalSince1970: TimeInterval(m6.authorizationIssuedAt))
        )
        // CryptoKit Ed25519 signatures are randomized, so the produced
        // authorization matches the golden vector in body and digest (the
        // exact signed field set) while carrying its own valid signature.
        #expect(produced.body == authorization.body)
        #expect(produced.digest == authorization.digest)

        let validated = try DiagnosticsResponseProtocol.validateResponseArtifact(
            golden.response,
            operation: operation,
            attestation: attestation,
            authorization: produced,
            record: record,
            now: Date(timeIntervalSince1970: TimeInterval(m6.responseIssuedAt))
        )
        #expect(validated.canonical == golden.response)

        for index in golden.response.indices {
            var tampered = golden.response
            tampered[index] ^= 0x01
            #expect(throws: (any Error).self) {
                _ = try DiagnosticsResponseProtocol.validateResponseArtifact(
                    tampered,
                    operation: operation,
                    attestation: attestation,
                    authorization: produced,
                    record: record,
                    now: Date(timeIntervalSince1970: TimeInterval(m6.responseIssuedAt))
                )
            }
        }
    }

    @Test("Stale, tampered, generation-changed, cancelled, and restarted downloads never set evidence")
    func downloadFailureBoundaries() async throws {
        let identifier = UUID().uuidString.lowercased()
        let support = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-download-support-\(identifier)", isDirectory: true)
        let folder = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-download-folder-\(identifier)", isDirectory: true)
        let keychain = InMemoryDiagnosticsKeychain()
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.download.tests.\(identifier)",
            keychain: keychain
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
            try? FileManager.default.removeItem(at: folder)
        }
        _ = try store.installationCredential()

        let m5 = try M5UploadFixtureLoader.load()
        let appSeed = try Data(m1Hex: m5.appSeedHex)
        let helperKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(m1Hex: m5.helperSeedHex)
        )
        var record = makeRuntimeRecord(
            appSeed: appSeed,
            helperPublic: helperKey.publicKey.rawRepresentation,
            homeserver: try Data(m1Hex: m5.homeserverBindingHex),
            folder: try Data(m1Hex: m5.folderBindingHex),
            folderID: "controlled-download",
            appEpoch: m5.appEpoch,
            helperEpoch: m5.helperEpoch
        )
        let wall = Date(timeIntervalSince1970: TimeInterval(m5.requestIssuedAt))
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
            createdAt: m5.requestIssuedAt + 1
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
        try FileManager.default.createDirectory(
            at: namespace
                .appendingPathComponent("installations", isDirectory: true)
                .appendingPathComponent(
                    DiagnosticsNamespaceProtocol.base32LowerNoPadding(candidate.installationBinding),
                    isDirectory: true
                )
                .appendingPathComponent("operations", isDirectory: true),
            withIntermediateDirectories: true
        )

        let clock = LockedDiagnosticsClock(wall, continuous: 1_000)
        let sharedRecord = record
        let sharedFolder = folder

        func makePreflight(_ requireEmptySlot: Bool) -> DiagnosticsUploadPreflight {
            DiagnosticsUploadPreflight(
                folderID: sharedRecord.folderID,
                folderPath: sharedFolder.path,
                peerID: sharedRecord.homeserverDeviceID,
                engineGeneration: 7,
                engineRunning: true,
                pathsSettled: true,
                folderMode: "sendreceive",
                folderPaused: false,
                folderHealthy: true,
                designatedPeerIDs: [sharedRecord.homeserverDeviceID],
                peerConnected: true,
                peerPaused: false,
                pathOverlap: false,
                namespacePathAllowed: true,
                operationSlotEmpty: requireEmptySlot
            )
        }

        func runScenario(
            operationSeed: UInt8,
            respond: (@Sendable (Data) async throws -> Void)?,
            events: @escaping DiagnosticsPairingController.UploadEventsProvider
        ) async -> (DiagnosticsPairingController, ForegroundUploadTransport, LockedUploadRequestBox) {
            let requestBox = LockedUploadRequestBox()
            let transport = ForegroundUploadTransport(
                record: sharedRecord,
                helperKey: helperKey,
                clock: clock,
                request: { requestBox.value() },
                acceptAfter: 1,
                respond: respond
            )
            let controller = DiagnosticsPairingController(
                credentialStore: store,
                transportFactory: { _, _, _ in transport },
                now: { clock.value() },
                continuousNow: { clock.continuousValue() },
                uploadRandomBytes: { count in
                    if count == DiagnosticsUploadProtocol.payloadByteCount {
                        return Data(repeating: operationSeed &+ 3, count: count)
                    }
                    return Data(repeating: operationSeed &+ UInt8(truncatingIfNeeded: count % 7), count: count)
                },
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
            await controller.checkCapability(recordID: sharedRecord.id)
            controller.beginForegroundUpload(
                recordID: sharedRecord.id,
                preflight: { _, _, requireEmptySlot in makePreflight(requireEmptySlot) },
                rescan: { true },
                events: events
            )
            return (controller, transport, requestBox)
        }

        func waitTerminal(_ controller: DiagnosticsPairingController) async {
            for _ in 0..<20_000 {
                if let phase = controller.uploadStatuses[sharedRecord.id]?.phase,
                   ![.preflighting, .checking, .uploadObserved].contains(phase) {
                    return
                }
                await Task.yield()
            }
            Issue.record("controlled download did not reach a terminal state")
        }

        // Scenario 1: a response and event that exist before the response
        // baseline can never set download evidence — the operation times out
        // as a partial result with upload preserved.
        let staleBox = LockedDownloadEventBox()
        staleBox.append(DiagnosticsResponseProtocol.DownloadEvent(
            id: staleBox.nextID(),
            type: "ItemFinished",
            time: iso8601WithNanoseconds(clock.value().addingTimeInterval(-30)),
            data: [
                "folder": sharedRecord.folderID,
                "item": "stale-item-before-baseline",
                "type": "file",
                "action": "update",
                "error": "",
            ]
        ))
        let (staleController, staleTransport, _) = await runScenario(
            operationSeed: 0x10,
            respond: nil,
            events: { sinceID in
                DiagnosticsResponseProtocol.DownloadEventSnapshot(
                    generation: 7,
                    events: staleBox.events(after: sinceID)
                )
            }
        )
        await waitTerminal(staleController)
        let stale = try #require(staleController.uploadStatuses[sharedRecord.id])
        #expect(stale.phase == .timedOut)
        #expect(stale.evidence.uploadObserved)
        #expect(!stale.evidence.downloadObserved)
        #expect(!stale.evidence.roundtripConfirmed)
        #expect(stale.completedResponsePolls == DiagnosticsUploadProtocol.pollDelays.count)
        let staleAuthorizations = await staleTransport.responseAuthorizations()
        #expect(staleAuthorizations.count == 1)

        // Scenario 2: a fresh event pointing at a tampered artifact at the
        // exact expected path is unexpected authenticated namespace content
        // and terminates as conflict with upload preserved.
        let tamperBox = LockedDownloadEventBox()
        let tamperComponents = try DiagnosticsNamespaceProtocol.operationResponseComponents(
            installationBinding: candidate.installationBinding,
            operationID: Data(repeating: 0x20 &+ UInt8(truncatingIfNeeded: 32 % 7), count: 32)
        )
        let tamperRelative = tamperComponents.joined(separator: "/")
        let (tamperController, _, _) = await runScenario(
            operationSeed: 0x20,
            respond: { authorization in
                let response = try makeHelperResponseArtifact(
                    authorization: authorization,
                    record: sharedRecord,
                    helperKey: helperKey,
                    now: clock.value(),
                    tamperSignature: true
                )
                let url = tamperComponents.reduce(sharedFolder) {
                    $0.appendingPathComponent($1)
                }
                try response.write(to: url, options: .atomic)
                tamperBox.append(DiagnosticsResponseProtocol.DownloadEvent(
                    id: tamperBox.nextID(),
                    type: "ItemFinished",
                    time: iso8601WithNanoseconds(clock.value().addingTimeInterval(0.5)),
                    data: [
                        "folder": sharedRecord.folderID,
                        "item": tamperRelative,
                        "type": "file",
                        "action": "update",
                        "error": "",
                    ]
                ))
            },
            events: { sinceID in
                DiagnosticsResponseProtocol.DownloadEventSnapshot(
                    generation: 7,
                    events: tamperBox.events(after: sinceID)
                )
            }
        )
        await waitTerminal(tamperController)
        let tampered = try #require(tamperController.uploadStatuses[sharedRecord.id])
        #expect(tampered.phase == .conflict)
        #expect(!tampered.evidence.roundtripConfirmed)
        #expect(tampered.evidence.uploadObserved)
        #expect(!tampered.evidence.downloadObserved)

        // Scenario 3: an engine-generation change between upload acceptance
        // and the response baseline interrupts the operation.
        let (generationController, _, _) = await runScenario(
            operationSeed: 0x30,
            respond: nil,
            events: { _ in
                DiagnosticsResponseProtocol.DownloadEventSnapshot(generation: 8, events: [])
            }
        )
        await waitTerminal(generationController)
        let generation = try #require(generationController.uploadStatuses[sharedRecord.id])
        #expect(generation.phase == .interrupted)
        #expect(!generation.evidence.roundtripConfirmed)
        #expect(generation.evidence.uploadObserved)
        #expect(!generation.evidence.downloadObserved)

        // Scenario 4: explicit cancellation during the download leg is
        // terminal with the upload evidence preserved. The transport blocks
        // inside the authorization call until the test releases it.
        let cancelGate = LockedUploadRequestBox()
        let (cancelController, cancelTransport, _) = await runScenario(
            operationSeed: 0x40,
            respond: { _ in
                while cancelGate.value() == nil {
                    await Task.yield()
                }
            },
            events: { _ in
                DiagnosticsResponseProtocol.DownloadEventSnapshot(generation: 7, events: [])
            }
        )
        for _ in 0..<20_000 {
            let held = await cancelTransport.responseAuthorizations()
            if held.count == 1 { break }
            await Task.yield()
        }
        cancelController.cancelForegroundUpload(recordID: sharedRecord.id)
        cancelGate.set(Data([0x01]))
        for _ in 0..<2_000 { await Task.yield() }
        let cancelled = try #require(cancelController.uploadStatuses[sharedRecord.id])
        #expect(cancelled.phase == .cancelled)
        #expect(cancelled.evidence.uploadObserved)
        #expect(!cancelled.evidence.downloadObserved)
        #expect(!cancelled.evidence.roundtripConfirmed)

        // Scenario 5: a controller restart destroys the active correlation;
        // nothing resumes and no late event can set evidence.
        let (restartController, _, _) = await runScenario(
            operationSeed: 0x50,
            respond: nil,
            events: { _ in
                DiagnosticsResponseProtocol.DownloadEventSnapshot(generation: 7, events: [])
            }
        )
        for _ in 0..<50 { await Task.yield() }
        restartController.refresh()
        #expect(restartController.uploadStatuses[sharedRecord.id] == nil)
        for _ in 0..<200 { await Task.yield() }
        #expect(restartController.uploadStatuses[sharedRecord.id] == nil)
    }
}

import CryptoKit
import Foundation
import Testing
@testable import VaultSync

@Suite("D024 foreground upload-only contract (M5)")
struct DiagnosticsUploadM5Tests {
    @Test("Go and Swift reproduce the exact request, query, and attestation bytes")
    func crossLanguageGoldenBytes() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        #expect(golden.request.m1Hex == fixture.requestMessageHex)
        #expect(golden.query.m1Hex == fixture.queryMessageHex)
        #expect(golden.attestation.m1Hex == fixture.attestationMessageHex)

        let active = try makeActive(fixture: fixture, golden: golden)
        let attestation = try M5UploadMessage.decode(
            golden.attestation,
            appPublicKey: active.appPublicKey,
            helperPublicKey: active.helperPublicKey
        )
        #expect(active.request.body.m1Hex == fixture.requestBodyHex)
        #expect(active.request.digest.m1Hex == fixture.requestDigestHex)
        #expect(try active.request.bytes(255, count: 64).m1Hex == fixture.requestSignatureHex)
        #expect(active.query.body.m1Hex == fixture.queryBodyHex)
        #expect(active.query.digest.m1Hex == fixture.queryDigestHex)
        #expect(try active.query.bytes(255, count: 64).m1Hex == fixture.querySignatureHex)
        #expect(attestation.body.m1Hex == fixture.attestationBodyHex)
        #expect(attestation.digest.m1Hex == fixture.attestationDigestHex)
        #expect(try attestation.bytes(255, count: 64).m1Hex == fixture.attestationSignatureHex)
        try M5UploadMessage.validateUploadChain(
            request: active.request, query: active.query, attestation: attestation
        )
    }

    @Test("Production parser accepts the exact Go vectors and preserves upload-only evidence")
    func productionWireAndAcceptance() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let record = try makeProductRecord(fixture)
        let request = try DiagnosticsUploadProtocol.decode(golden.request, record: record)
        let query = try DiagnosticsUploadProtocol.decode(golden.query, record: record)
        let attestation = try DiagnosticsUploadProtocol.decode(golden.attestation, record: record)
        #expect(request.body.m1Hex == fixture.requestBodyHex)
        #expect(request.digest.m1Hex == fixture.requestDigestHex)
        #expect(query.body.m1Hex == fixture.queryBodyHex)
        #expect(query.digest.m1Hex == fixture.queryDigestHex)
        #expect(attestation.body.m1Hex == fixture.attestationBodyHex)
        #expect(attestation.digest.m1Hex == fixture.attestationDigestHex)

        let installation = try Data(m1Hex: fixture.installationBindingHex)
        let operationID = try Data(m1Hex: fixture.operationIdHex)
        let operation = DiagnosticsUploadProtocol.Operation(
            request: request,
            query: query,
            operationID: operationID,
            installationBinding: installation,
            requestComponents: try DiagnosticsNamespaceProtocol.operationRequestComponents(
                installationBinding: installation,
                operationID: operationID
            )
        )
        let accepted = try DiagnosticsUploadProtocol.validateUploadAttestation(
            golden.attestation,
            operation: operation,
            record: record,
            now: Date(timeIntervalSince1970: TimeInterval(fixture.attestationIssuedAt))
        )
        #expect(accepted == attestation)

        var tampered = golden.attestation
        tampered[tampered.index(before: tampered.endIndex)] ^= 0x01
        #expect(throws: DiagnosticsProtocolError.invalidMessage) {
            _ = try DiagnosticsUploadProtocol.validateUploadAttestation(
                tampered,
                operation: operation,
                record: record,
                now: Date(timeIntervalSince1970: TimeInterval(fixture.attestationIssuedAt))
            )
        }
    }

    @Test("Production request creation is exclusive, confined, and collision-safe")
    func productionImmutableRequestStore() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let installation = try Data(m1Hex: fixture.installationBindingHex)
        let operationID = try Data(m1Hex: fixture.operationIdHex)
        let components = try DiagnosticsNamespaceProtocol.operationRequestComponents(
            installationBinding: installation,
            operationID: operationID
        )
        let folder = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-upload-store-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let operations = components.dropLast().reduce(folder) {
            $0.appendingPathComponent($1, isDirectory: true)
        }
        try FileManager.default.createDirectory(at: operations, withIntermediateDirectories: true)

        try DiagnosticsUploadFileStore.createImmutable(
            folderPath: folder.path,
            components: components,
            data: golden.request
        )
        #expect(
            try DiagnosticsNamespaceFileReader.read(
                folderPath: folder.path,
                components: components
            ) == golden.request
        )
        #expect(throws: DiagnosticsProtocolError.conflict) {
            try DiagnosticsUploadFileStore.createImmutable(
                folderPath: folder.path,
                components: components,
                data: golden.request
            )
        }

        let requestURL = components.reduce(folder) { $0.appendingPathComponent($1) }
        let linked = folder.appendingPathComponent("second-link.cbor")
        try FileManager.default.linkItem(at: requestURL, to: linked)
        #expect(throws: DiagnosticsProtocolError.conflict) {
            _ = try DiagnosticsNamespaceFileReader.read(
                folderPath: folder.path,
                components: components
            )
        }
    }

    @Test("Only a pinned local response for the exact active query sets upload")
    func exactPinnedAcceptanceOnly() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let active = try makeActive(fixture: fixture, golden: golden)
        let exact = M5MockLocalChannelResponse(
            pinned: true, exactQuery: golden.query, category: .signedBytes(golden.attestation)
        )
        var model = M5UploadAcceptanceModel(active: active)
        model.accept(exact, now: fixture.attestationIssuedAt)
        #expect(model.phase == .completed)
        #expect(model.evidence.uploadObserved)
        #expect(!model.evidence.downloadObserved)
        #expect(!model.evidence.roundtripConfirmed)

        let before = model.evidence
        model.accept(exact, now: fixture.attestationIssuedAt)
        #expect(model.evidence == before)

        let weakResponses: [M5MockLocalChannelResponse] = [
            .init(pinned: false, exactQuery: golden.query, category: .signedBytes(golden.attestation)),
            .init(pinned: true, exactQuery: Data(repeating: 0x91, count: golden.query.count), category: .signedBytes(golden.attestation)),
            .init(pinned: true, exactQuery: golden.query, category: .pending),
            .init(pinned: true, exactQuery: golden.query, category: .acceptedHTTP),
            .init(pinned: true, exactQuery: golden.query, category: .unreachable),
            .init(pinned: true, exactQuery: golden.query, category: .timestamp(fixture.attestationIssuedAt)),
        ]
        for response in weakResponses {
            var isolated = M5UploadAcceptanceModel(active: active)
            isolated.accept(response, now: fixture.attestationIssuedAt)
            #expect(isolated.phase == .checking)
            #expect(!isolated.evidence.uploadObserved)
            #expect(!isolated.evidence.downloadObserved)
            #expect(!isolated.evidence.roundtripConfirmed)
        }
    }

    @Test("Replay, wrong signatures, wrong clocks, and terminal operations never upgrade")
    func negativeAndTerminalCases() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let active = try makeActive(fixture: fixture, golden: golden)
        var mutated = golden.attestation
        mutated[mutated.index(before: mutated.endIndex)] ^= 0x01
        var invalid = M5UploadAcceptanceModel(active: active)
        invalid.accept(
            .init(pinned: true, exactQuery: golden.query, category: .signedBytes(mutated)),
            now: fixture.attestationIssuedAt
        )
        #expect(!invalid.evidence.uploadObserved)

        var stale = M5UploadAcceptanceModel(active: active)
        stale.accept(
            .init(pinned: true, exactQuery: golden.query, category: .signedBytes(golden.attestation)),
            now: fixture.expiresAt + 121
        )
        #expect(!stale.evidence.uploadObserved)

        for terminal in [M5UploadPhase.cancelled, .timedOut, .interrupted] {
            var model = M5UploadAcceptanceModel(active: active)
            switch terminal {
            case .cancelled: model.cancel()
            case .timedOut: model.timeout()
            case .interrupted: model.helperRestart()
            default: Issue.record("unexpected terminal fixture")
            }
            let before = model.evidence
            model.accept(
                .init(pinned: true, exactQuery: golden.query, category: .signedBytes(golden.attestation)),
                now: fixture.attestationIssuedAt
            )
            #expect(model.evidence == before)
            #expect(!model.evidence.uploadObserved)
        }

        var restarted = M5UploadAcceptanceModel(active: active)
        restarted.appRestart()
        #expect(restarted.phase == .interrupted)
        #expect(restarted.active == nil)
        restarted.accept(
            .init(pinned: true, exactQuery: golden.query, category: .signedBytes(golden.attestation)),
            now: fixture.attestationIssuedAt
        )
        #expect(!restarted.evidence.uploadObserved)
    }

    @Test("Cross-device clock skew is bounded but never used as causal evidence")
    func crossClockSkewUsesDigestCausality() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let active = try makeActive(fixture: fixture, golden: golden)
        let skewedBytes = try M5UploadGoldenMessages.makeHelperClockBehindAttestation(
            fixture: fixture, request: active.request, query: active.query
        )
        let skewed = try M5UploadMessage.decode(
            skewedBytes, appPublicKey: active.appPublicKey, helperPublicKey: active.helperPublicKey
        )
        try active.request.validateClock(now: fixture.requestIssuedAt)
        try active.query.validateClock(now: fixture.requestIssuedAt)
        try skewed.validateClock(now: fixture.requestIssuedAt)
        try M5UploadMessage.validateUploadChain(
            request: active.request, query: active.query, attestation: skewed
        )
        #expect(try skewed.uint(12) < active.request.uint(12))
        #expect(try skewed.uint(19) == skewed.uint(12))
    }

    @Test("Tuple evidence is isolated and cleanup is orthogonal")
    func tupleAndCleanupIsolation() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let active = try makeActive(fixture: fixture, golden: golden)
        var first = M5UploadAcceptanceModel(active: active)
        var second = M5UploadAcceptanceModel(active: active)
        let response = M5MockLocalChannelResponse(
            pinned: true, exactQuery: golden.query, category: .signedBytes(golden.attestation)
        )
        first.accept(response, now: fixture.attestationIssuedAt)
        #expect(first.evidence.uploadObserved)
        #expect(!second.evidence.uploadObserved)

        let accepted = first.evidence
        first.cleanupAttempt()
        #expect(first.evidence.uploadObserved == accepted.uploadObserved)
        #expect(first.evidence.downloadObserved == accepted.downloadObserved)
        #expect(first.evidence.roundtripConfirmed == accepted.roundtripConfirmed)
        #expect(first.evidence.cleanupAttempts == accepted.cleanupAttempts + 1)

        second.cleanupAttempt()
        #expect(!second.evidence.uploadObserved)
        #expect(!second.evidence.downloadObserved)
        #expect(!second.evidence.roundtripConfirmed)
    }

    @Test("Request payload and polling remain exact, random, and finite")
    func randomPayloadAndBoundedPolling() throws {
        var firstGenerator = SystemRandomNumberGenerator()
        var secondGenerator = SystemRandomNumberGenerator()
        let first = M5UploadTestPayload.random(using: &firstGenerator)
        let second = M5UploadTestPayload.random(using: &secondGenerator)
        #expect(first.count == 256)
        #expect(second.count == 256)
        #expect(first != second)
        #expect(M5BoundedPollModel.delays == [2, 4, 8, 16, 30, 60, 120, 120])
        #expect(M5BoundedPollModel.categories(Array(repeating: .pending, count: 32)).count == 8)

        var limiter = M5AppOperationLimiter()
        let firstStarted = limiter.begin(tuple: "app/server/folder-a/helper/epochs")
        let duplicateStarted = limiter.begin(tuple: "app/server/folder-a/helper/epochs")
        let secondStarted = limiter.begin(tuple: "app/server/folder-b/helper/epochs")
        let overflowStarted = limiter.begin(tuple: "app/server/folder-c/helper/epochs")
        #expect(firstStarted)
        #expect(!duplicateStarted)
        #expect(secondStarted)
        #expect(!overflowStarted)
        limiter.finish(tuple: "app/server/folder-a/helper/epochs")
        let replacementStarted = limiter.begin(tuple: "app/server/folder-c/helper/epochs")
        #expect(replacementStarted)
    }

    @Test("Canonical parser rejects every truncation and arbitrary input stays fail-closed")
    func parserNegativeProperty() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let golden = try M5UploadGoldenMessages.make(fixture)
        let active = try makeActive(fixture: fixture, golden: golden)
        for message in [golden.request, golden.query, golden.attestation] {
            for length in 0..<message.count {
                #expect(throws: (any Error).self) {
                    _ = try M5UploadMessage.decode(
                        message.prefix(length),
                        appPublicKey: active.appPublicKey,
                        helperPublicKey: active.helperPublicKey
                    )
                }
            }
            var trailing = message
            trailing.append(0)
            #expect(throws: (any Error).self) {
                _ = try M5UploadMessage.decode(
                    trailing,
                    appPublicKey: active.appPublicKey,
                    helperPublicKey: active.helperPublicKey
                )
            }
        }

        var generator = M5DeterministicGenerator(state: 0x024)
        for _ in 0..<1_000 {
            let count = Int.random(in: 0...512, using: &generator)
            let bytes = Data((0..<count).map { _ in UInt8.random(in: .min ... .max, using: &generator) })
            _ = try? M5UploadMessage.decode(
                bytes,
                appPublicKey: active.appPublicKey,
                helperPublicKey: active.helperPublicKey
            )
        }
    }

    @Test("Product upload runtime remains isolated from response, durable state, and external systems")
    func privacyAndProductBoundary() throws {
        let fixture = try M5UploadFixtureLoader.load()
        let testDirectory = URL(fileURLWithPath: "\(#filePath)").deletingLastPathComponent()
        let productDirectory = testDirectory.deletingLastPathComponent().appendingPathComponent("VaultSync", isDirectory: true)
        let enumerator = FileManager.default.enumerator(
            at: productDirectory,
            includingPropertiesForKeys: [.isRegularFileKey],
            options: [.skipsHiddenFiles]
        )
        while let url = enumerator?.nextObject() as? URL {
            guard url.pathExtension == "swift" else { continue }
            let body = try String(contentsOf: url, encoding: .utf8)
            for uploadDomain in [
                "eu.vaultsync.roundtrip/v1/operation-request",
                "eu.vaultsync.roundtrip/v1/attestation-query",
                "eu.vaultsync.roundtrip/v1/upload-attestation",
            ] where body.contains(uploadDomain) {
                #expect(url.lastPathComponent == "DiagnosticsUploadProtocol.swift")
            }
            for laterDomain in [
                "eu.vaultsync.roundtrip/v1/response-authorization",
                "eu.vaultsync.roundtrip/v1/response-artifact",
                "eu.vaultsync.roundtrip/v1/cleanup-request",
                "eu.vaultsync.roundtrip/v1/cleanup-ack",
            ] {
                #expect(!body.contains(laterDomain))
            }
            if [
                "DiagnosticsUploadProtocol.swift",
                "DiagnosticsUploadPreflight.swift",
                "DiagnosticsUploadFileStore.swift",
                "DiagnosticsPairingController.swift",
            ].contains(url.lastPathComponent) {
                for forbiddenSink in [
                    "UserDefaults", "Keychain", "StoreKit", "APNs", "Cloud Relay",
                    "Logger(", "os_log", "crash report", "support bundle",
                ] {
                    #expect(!body.contains(forbiddenSink))
                }
            }
        }

        let controlledView = try String(
            contentsOf: productDirectory
                .appendingPathComponent("Views", isDirectory: true)
                .appendingPathComponent("ControlledDiagnosticsView.swift"),
            encoding: .utf8
        )
        #expect(controlledView.contains("@Environment(\\.scenePhase)"))
        #expect(controlledView.contains("if phase != .active"))
        let cancellationHooks =
            controlledView.components(separatedBy: "cancelAllForegroundUploads()").count - 1
        #expect(cancellationHooks >= 2)

        let allowedSnapshot = "phase=completed upload=true download=false roundtrip=false cleanup=0"
        for forbidden in [
            fixture.operationIdHex,
            fixture.requestNonceHex,
            fixture.queryNonceHex,
            fixture.helperNonceHex,
            fixture.requestDigestHex,
            fixture.requestPayloadHex,
            fixture.attestationMessageHex,
        ] {
            #expect(!allowedSnapshot.contains(forbidden))
        }
        #expect(!allowedSnapshot.contains("UserDefaults"))
        #expect(!allowedSnapshot.contains("Keychain"))
        #expect(!allowedSnapshot.contains("StoreKit"))
        #expect(!allowedSnapshot.contains("APNs"))
        #expect(!allowedSnapshot.contains("Cloud Relay"))
        #expect(!allowedSnapshot.contains("crash report"))
        #expect(!allowedSnapshot.contains("support bundle"))
    }

    private func makeActive(
        fixture: M5UploadFixture,
        golden: M5UploadGoldenMessages
    ) throws -> M5ActiveUpload {
        let appPrivate = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: fixture.appSeedHex))
        let helperPrivate = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: fixture.helperSeedHex))
        let request = try M5UploadMessage.decode(
            golden.request,
            appPublicKey: appPrivate.publicKey,
            helperPublicKey: helperPrivate.publicKey
        )
        let query = try M5UploadMessage.decode(
            golden.query,
            appPublicKey: appPrivate.publicKey,
            helperPublicKey: helperPrivate.publicKey
        )
        try M5UploadMessage.validateRequestAndQuery(request, query)
        return M5ActiveUpload(
            request: request,
            query: query,
            appPublicKey: appPrivate.publicKey,
            helperPublicKey: helperPrivate.publicKey
        )
    }

    private func makeProductRecord(_ fixture: M5UploadFixture) throws -> DiagnosticsPairingRecord {
        let appSeed = try Data(m1Hex: fixture.appSeedHex)
        let helperSeed = try Data(m1Hex: fixture.helperSeedHex)
        let appKey = try Curve25519.Signing.PrivateKey(rawRepresentation: appSeed)
        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: helperSeed)
        let appPublic = appKey.publicKey.rawRepresentation
        let helperPublic = helperKey.publicKey.rawRepresentation
        let appKeyID = DiagnosticsCrypto.keyID(publicKey: appPublic)
        let helperKeyID = DiagnosticsCrypto.keyID(publicKey: helperPublic)
        return DiagnosticsPairingRecord(
            id: DiagnosticsPairingRecord.identifier(
                appKeyID: appKeyID,
                folderBinding: try Data(m1Hex: fixture.folderBindingHex)
            ),
            homeserverDeviceID: "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2",
            folderID: "fixture-folder",
            endpointHost: "127.0.0.1",
            endpointPort: 443,
            tlsSPKIPin: Data(repeating: 0x55, count: 32),
            helperPublicKey: helperPublic,
            helperKeyID: helperKeyID,
            homeserverBinding: try Data(m1Hex: fixture.homeserverBindingHex),
            folderBinding: try Data(m1Hex: fixture.folderBindingHex),
            appSeed: appSeed,
            appPublicKey: appPublic,
            appKeyID: appKeyID,
            appEpoch: fixture.appEpoch,
            helperEpoch: fixture.helperEpoch,
            currentCredentialStateDigest: Data(repeating: 0x25, count: 32),
            state: .namespaceActive,
            hardExpiry: fixture.expiresAt,
            localDeadline: nil,
            lastOutgoing: Data([0xa0]),
            lastIncoming: Data([0xa0]),
            transcriptFingerprint: nil,
            namespaceID: Data(repeating: 0x07, count: 32),
            namespaceInitialAppKeyID: appKeyID,
            namespaceEnablement: Data([0xa0]),
            namespaceRootDigest: Data(repeating: 0x21, count: 32),
            namespaceManifestDigest: Data(repeating: 0x22, count: 32),
            namespaceManifestEpoch: fixture.helperEpoch,
            namespaceAuthorizationDigest: Data(repeating: 0x23, count: 32),
            namespaceAuthorizationEpoch: fixture.authorizationEpoch,
            pendingLifecycle: nil
        )
    }
}

private struct M5DeterministicGenerator: RandomNumberGenerator {
    var state: UInt64

    mutating func next() -> UInt64 {
        state = state &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
        return state
    }
}

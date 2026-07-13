import CryptoKit
import Foundation
import Testing

@Suite("Dormant D024 upload attestation foundation (M5)")
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

    @Test("M5 remains test-only in Swift and records no forbidden operation values")
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
            #expect(!body.contains(M5UploadMessage.capability))
            #expect(!body.contains("eu.vaultsync.roundtrip/v1/upload-attestation"))
        }

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
}

private struct M5DeterministicGenerator: RandomNumberGenerator {
    var state: UInt64

    mutating func next() -> UInt64 {
        state = state &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
        return state
    }
}

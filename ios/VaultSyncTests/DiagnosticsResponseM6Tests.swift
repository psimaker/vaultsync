import CryptoKit
import XCTest

final class DiagnosticsResponseM6Tests: XCTestCase {
    func testDecision024ResponseCleanupCrossLanguageGoldenBytesAndChains() throws {
        let m5 = try M5UploadFixtureLoader.load()
        let m6 = try M6ResponseFixtureLoader.load()
        XCTAssertEqual(m6.fixtureVersion, 1)
        XCTAssertEqual(m6.sourceDecision, "024")
        XCTAssertEqual(m6.baseFixture, "diagnostics-upload-m5.json")

        let uploadGolden = try M5UploadGoldenMessages.make(m5)
        let responseGolden = try M6ResponseGoldenMessages.make(m5: m5, m6: m6)
        XCTAssertEqual(responseGolden.authorization.m1Hex, m6.authorizationMessageHex)
        XCTAssertEqual(responseGolden.response.m1Hex, m6.responseMessageHex)
        XCTAssertEqual(responseGolden.cleanupRequest.m1Hex, m6.cleanupRequestMessageHex)
        XCTAssertEqual(responseGolden.cleanupAck.m1Hex, m6.cleanupAckMessageHex)

        let appPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.appSeedHex))
        let helperPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.helperSeedHex))
        let appPublicKey = appPrivateKey.publicKey
        let helperPublicKey = helperPrivateKey.publicKey
        let request = try M5UploadMessage.decode(
            uploadGolden.request, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )
        let attestation = try M5UploadMessage.decode(
            uploadGolden.attestation, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )
        let authorization = try M6ResponseMessage.decode(
            responseGolden.authorization, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )
        let response = try M6ResponseMessage.decode(
            responseGolden.response, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )
        let cleanupRequest = try M6ResponseMessage.decode(
            responseGolden.cleanupRequest, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )
        let cleanupAck = try M6ResponseMessage.decode(
            responseGolden.cleanupAck, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )

        XCTAssertEqual(authorization.body.m1Hex, m6.authorizationBodyHex)
        XCTAssertEqual(authorization.digest.m1Hex, m6.authorizationDigestHex)
        XCTAssertEqual(try authorization.bytes(255, count: 64).m1Hex, m6.authorizationSignatureHex)
        XCTAssertEqual(response.body.m1Hex, m6.responseBodyHex)
        XCTAssertEqual(response.digest.m1Hex, m6.responseDigestHex)
        XCTAssertEqual(try response.bytes(255, count: 64).m1Hex, m6.responseSignatureHex)
        XCTAssertEqual(cleanupRequest.body.m1Hex, m6.cleanupRequestBodyHex)
        XCTAssertEqual(cleanupRequest.digest.m1Hex, m6.cleanupRequestDigestHex)
        XCTAssertEqual(try cleanupRequest.bytes(255, count: 64).m1Hex, m6.cleanupRequestSignatureHex)
        XCTAssertEqual(cleanupAck.body.m1Hex, m6.cleanupAckBodyHex)
        XCTAssertEqual(cleanupAck.digest.m1Hex, m6.cleanupAckDigestHex)
        XCTAssertEqual(try cleanupAck.bytes(255, count: 64).m1Hex, m6.cleanupAckSignatureHex)

        try M6ResponseMessage.validateAuthorizationChain(
            request: request, attestation: attestation, authorization: authorization
        )
        try M6ResponseMessage.validateResponseChain(
            request: request,
            attestation: attestation,
            authorization: authorization,
            response: response
        )
        try M6ResponseMessage.validateCleanupChain(request: cleanupRequest, acknowledgment: cleanupAck)
        try authorization.validateClock(now: m6.authorizationIssuedAt)
        try response.validateClock(now: m6.responseIssuedAt)
        try cleanupRequest.validateClock(now: m6.cleanupIssuedAt)
        try cleanupAck.validateClock(now: m6.cleanupAckIssuedAt)

        XCTAssertEqual(try cleanupRequest.targets().map(\.m1Hex), m6.cleanupTargetsHex)
        XCTAssertEqual(try cleanupAck.targets().map(\.m1Hex), m6.cleanupTargetsHex)
        XCTAssertEqual(try cleanupAck.results(), m6.cleanupResults)
        XCTAssertEqual(try response.bytes(24, count: 256).m1Hex, m6.responsePayloadHex)
        XCTAssertEqual(try response.bytes(23, count: 32).m1Hex, m6.responseNonceHex)
        XCTAssertEqual(try authorization.bytes(21, count: 32).m1Hex, m6.authorizationNonceHex)
    }

    func testResponseCleanupParserRejectsTruncationTrailingBytesWrongKeysAndUploadTypes() throws {
        let m5 = try M5UploadFixtureLoader.load()
        let m6 = try M6ResponseFixtureLoader.load()
        let uploadGolden = try M5UploadGoldenMessages.make(m5)
        let responseGolden = try M6ResponseGoldenMessages.make(m5: m5, m6: m6)
        let appPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.appSeedHex))
        let helperPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.helperSeedHex))
        let appPublicKey = appPrivateKey.publicKey
        let helperPublicKey = helperPrivateKey.publicKey

        for encoded in [
            responseGolden.authorization,
            responseGolden.response,
            responseGolden.cleanupRequest,
            responseGolden.cleanupAck,
        ] {
            for length in 0 ..< encoded.count {
                XCTAssertThrowsError(
                    try M6ResponseMessage.decode(
                        encoded.prefix(length), appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
                    )
                )
            }
            var trailing = encoded
            trailing.append(0)
            XCTAssertThrowsError(
                try M6ResponseMessage.decode(
                    trailing, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
                )
            )
        }

        let wrongHelper = Curve25519.Signing.PrivateKey().publicKey
        XCTAssertThrowsError(
            try M6ResponseMessage.decode(
                responseGolden.response, appPublicKey: appPublicKey, helperPublicKey: wrongHelper
            )
        )
        XCTAssertThrowsError(
            try M6ResponseMessage.decode(
                uploadGolden.attestation, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
            )
        )
        XCTAssertThrowsError(
            try M5UploadMessage.decode(
                responseGolden.authorization, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
            )
        )

        var corrupted = responseGolden.response
        corrupted[corrupted.index(before: corrupted.endIndex)] ^= 0x01
        XCTAssertThrowsError(
            try M6ResponseMessage.decode(
                corrupted, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
            )
        )
    }

    func testResponseCleanupFoundationFixtureCreatesNoAppEvidenceOrRoundtripClaim() throws {
        let m5 = try M5UploadFixtureLoader.load()
        let m6 = try M6ResponseFixtureLoader.load()
        let responseGolden = try M6ResponseGoldenMessages.make(m5: m5, m6: m6)
        XCTAssertFalse(responseGolden.authorization.isEmpty)
        XCTAssertFalse(responseGolden.response.isEmpty)
        XCTAssertFalse(responseGolden.cleanupRequest.isEmpty)
        XCTAssertFalse(responseGolden.cleanupAck.isEmpty)

        var evidence = M5UploadEvidence()
        evidence.uploadObserved = true
        evidence.cleanupAttempts += 1
        XCTAssertTrue(evidence.uploadObserved)
        XCTAssertFalse(evidence.downloadObserved)
        XCTAssertFalse(evidence.roundtripConfirmed)
    }
}

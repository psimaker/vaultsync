import CryptoKit
import Foundation

struct M5UploadFixture: Decodable {
    let fixtureVersion: Int
    let sourceDecision: String
    let appSeedHex: String
    let helperSeedHex: String
    let homeserverBindingHex: String
    let folderBindingHex: String
    let installationBindingHex: String
    let appEpoch: UInt64
    let helperEpoch: UInt64
    let authorizationEpoch: UInt64
    let operationIdHex: String
    let requestNonceHex: String
    let queryNonceHex: String
    let helperNonceHex: String
    let requestIssuedAt: UInt64
    let queryIssuedAt: UInt64
    let attestationIssuedAt: UInt64
    let expiresAt: UInt64
    let requestPayloadHex: String
    let requestBodyHex: String
    let requestDigestHex: String
    let requestSignatureHex: String
    let requestMessageHex: String
    let queryBodyHex: String
    let queryDigestHex: String
    let querySignatureHex: String
    let queryMessageHex: String
    let attestationBodyHex: String
    let attestationDigestHex: String
    let attestationSignatureHex: String
    let attestationMessageHex: String
}

private final class M5UploadFixtureBundleToken {}

enum M5UploadFixtureLoader {
    static func load(filePath: StaticString = #filePath) throws -> M5UploadFixture {
        let bundle = Bundle(for: M5UploadFixtureBundleToken.self)
        let bundled = bundle.url(forResource: "diagnostics-upload-m5", withExtension: "json")
        let sourceFallback = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures", isDirectory: true)
            .appendingPathComponent("diagnostics-upload-m5.json")
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(M5UploadFixture.self, from: Data(contentsOf: bundled ?? sourceFallback))
    }
}

enum M5UploadMessageType: UInt64 {
    case operationRequest = 3
    case attestationQuery = 4
    case uploadAttestation = 5
}

struct M5UploadMessage {
    static let capability = "eu.vaultsync.diagnostics.correlated-roundtrip/1"
    static let domains: [M5UploadMessageType: String] = [
        .operationRequest: "eu.vaultsync.roundtrip/v1/operation-request\0",
        .attestationQuery: "eu.vaultsync.roundtrip/v1/attestation-query\0",
        .uploadAttestation: "eu.vaultsync.roundtrip/v1/upload-attestation\0",
    ]

    let type: M5UploadMessageType
    let canonical: Data
    let body: Data
    let digest: Data
    let fields: [UInt64: M1CBORValue]

    static func decode(
        _ data: Data,
        appPublicKey: Curve25519.Signing.PublicKey,
        helperPublicKey: Curve25519.Signing.PublicKey
    ) throws -> M5UploadMessage {
        let value = try M1DeterministicCBOR.decode(data)
        guard case .map(let fieldList) = value,
              let typeValue = fieldList.first(where: { $0.label == 4 })?.value,
              case .unsigned(let rawType) = typeValue,
              let type = M5UploadMessageType(rawValue: rawType),
              let expectedLabels = expectedLabels[type],
              fieldList.map(\.label) == expectedLabels else {
            throw M1ContractTestError.invalid("unknown, missing, or reordered upload field")
        }
        let fields = Dictionary(uniqueKeysWithValues: fieldList.map { ($0.label, $0.value) })
        try validateFields(fields, type: type, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey)
        guard case .bytes(let signature) = fields[255], signature.count == 64,
              let domain = domains[type] else {
            throw M1ContractTestError.invalid("missing upload signature")
        }
        let unsigned = M1CBORValue.map(fieldList.filter { $0.label != 255 })
        let body = try M1DeterministicCBOR.encode(unsigned)
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        let signer = type == .uploadAttestation ? helperPublicKey : appPublicKey
        guard signer.isValidSignature(signature, for: signedInput) else {
            throw M1ContractTestError.invalid("invalid upload signature")
        }
        return M5UploadMessage(
            type: type,
            canonical: data,
            body: body,
            digest: M1ContractCrypto.sha256(domain: domain, body: body),
            fields: fields
        )
    }

    static func validateRequestAndQuery(_ request: M5UploadMessage, _ query: M5UploadMessage) throws {
        guard request.type == .operationRequest, query.type == .attestationQuery,
              commonFieldsEqual(request, query),
              try query.bytes(17, count: 32) == request.digest,
              try query.uint(12) >= request.uint(12),
              try query.uint(13) <= request.uint(13) else {
            throw M1ContractTestError.invalid("request/query chain mismatch")
        }
    }

    static func validateUploadChain(
        request: M5UploadMessage,
        query: M5UploadMessage,
        attestation: M5UploadMessage
    ) throws {
        try validateRequestAndQuery(request, query)
        guard attestation.type == .uploadAttestation, commonFieldsEqual(request, attestation),
              try attestation.bytes(16, count: 32) == request.bytes(16, count: 32),
              try attestation.bytes(17, count: 32) == request.digest,
              try attestation.bytes(30, count: 32) == query.bytes(30, count: 32),
              try attestation.bytes(31, count: 32) == query.digest,
              try attestation.uint(13) <= request.uint(13),
              try attestation.uint(13) <= query.uint(13) else {
            throw M1ContractTestError.invalid("attestation chain mismatch")
        }
    }

    func validateClock(now: UInt64) throws {
        let issued = try uint(12)
        let expires = try uint(13)
        if issued > now, issued - now > 120 {
            throw M1ContractTestError.invalid("issued too far in the future")
        }
        if now > expires, now - expires > 120 {
            throw M1ContractTestError.invalid("expired upload message")
        }
    }

    func bytes(_ label: UInt64, count: Int) throws -> Data {
        guard case .bytes(let value) = fields[label], value.count == count else {
            throw M1ContractTestError.invalid("wrong byte field")
        }
        return value
    }

    func uint(_ label: UInt64) throws -> UInt64 {
        guard case .unsigned(let value) = fields[label] else {
            throw M1ContractTestError.invalid("wrong uint field")
        }
        return value
    }

    private static let expectedLabels: [M5UploadMessageType: [UInt64]] = [
        .operationRequest: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 255],
        .attestationQuery: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 30, 255],
        .uploadAttestation: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 16, 17, 18, 19, 30, 31, 255],
    ]

    private static func validateFields(
        _ fields: [UInt64: M1CBORValue],
        type: M5UploadMessageType,
        appPublicKey: Curve25519.Signing.PublicKey,
        helperPublicKey: Curve25519.Signing.PublicKey
    ) throws {
        guard fields[1] == .text(capability), fields[2] == .unsigned(1), fields[3] == .unsigned(1),
              fields[4] == .unsigned(type.rawValue) else {
            throw M1ContractTestError.invalid("wrong upload capability/version/type")
        }
        for label in [5, 6, 7, 8, 11] {
            guard case .bytes(let value) = fields[UInt64(label)], value.count == 32, value.contains(where: { $0 != 0 }) else {
                throw M1ContractTestError.invalid("wrong upload binding/key/operation")
            }
        }
        guard fields[7] == .bytes(M1ContractCrypto.keyID(publicKey: appPublicKey.rawRepresentation)),
              fields[8] == .bytes(M1ContractCrypto.keyID(publicKey: helperPublicKey.rawRepresentation)),
              case .unsigned(let appEpoch) = fields[9], appEpoch > 0,
              case .unsigned(let helperEpoch) = fields[10], helperEpoch > 0,
              case .unsigned(let issued) = fields[12], issued > 0,
              case .unsigned(let expires) = fields[13], expires > issued, expires - issued <= 600 else {
            throw M1ContractTestError.invalid("wrong upload key or lifetime")
        }
        switch type {
        case .operationRequest:
            guard case .bytes(let nonce) = fields[14], nonce.count == 32, nonce.contains(where: { $0 != 0 }),
                  case .bytes(let payload) = fields[15], payload.count == 256,
                  fields[16] == .bytes(M1ContractCrypto.sha256(payload)) else {
                throw M1ContractTestError.invalid("wrong request payload")
            }
        case .attestationQuery:
            guard case .bytes(let digest) = fields[17], digest.count == 32,
                  case .bytes(let nonce) = fields[30], nonce.count == 32, nonce.contains(where: { $0 != 0 }) else {
                throw M1ContractTestError.invalid("wrong attestation query")
            }
        case .uploadAttestation:
            for label in [16, 17, 18, 30, 31] {
                guard case .bytes(let value) = fields[UInt64(label)], value.count == 32 else {
                    throw M1ContractTestError.invalid("wrong attestation field")
                }
            }
            guard case .bytes(let helperNonce) = fields[18], helperNonce.contains(where: { $0 != 0 }),
                  case .bytes(let queryNonce) = fields[30], queryNonce.contains(where: { $0 != 0 }),
                  case .unsigned(let observed) = fields[19], observed > 0, observed <= issued else {
                throw M1ContractTestError.invalid("wrong helper observation")
            }
        }
    }

    private static func commonFieldsEqual(_ left: M5UploadMessage, _ right: M5UploadMessage) -> Bool {
        [1, 2, 3, 5, 6, 7, 8, 9, 10, 11].allSatisfy { left.fields[$0] == right.fields[$0] }
    }
}

struct M5UploadGoldenMessages {
    let request: Data
    let query: Data
    let attestation: Data

    static func make(_ fixture: M5UploadFixture) throws -> M5UploadGoldenMessages {
        let appPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: fixture.appSeedHex))
        let helperPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: fixture.helperSeedHex))
        let appKeyID = M1ContractCrypto.keyID(publicKey: appPrivateKey.publicKey.rawRepresentation)
        let helperKeyID = M1ContractCrypto.keyID(publicKey: helperPrivateKey.publicKey.rawRepresentation)
        let homeserver = try Data(m1Hex: fixture.homeserverBindingHex)
        let folder = try Data(m1Hex: fixture.folderBindingHex)
        let operation = try Data(m1Hex: fixture.operationIdHex)
        let payload = try Data(m1Hex: fixture.requestPayloadHex)

        func common(_ type: M5UploadMessageType, issuedAt: UInt64) -> [M1CBORField] {
            [
                M1CBORField(label: 1, value: .text(M5UploadMessage.capability)),
                M1CBORField(label: 2, value: .unsigned(1)),
                M1CBORField(label: 3, value: .unsigned(1)),
                M1CBORField(label: 4, value: .unsigned(type.rawValue)),
                M1CBORField(label: 5, value: .bytes(homeserver)),
                M1CBORField(label: 6, value: .bytes(folder)),
                M1CBORField(label: 7, value: .bytes(appKeyID)),
                M1CBORField(label: 8, value: .bytes(helperKeyID)),
                M1CBORField(label: 9, value: .unsigned(fixture.appEpoch)),
                M1CBORField(label: 10, value: .unsigned(fixture.helperEpoch)),
                M1CBORField(label: 11, value: .bytes(operation)),
                M1CBORField(label: 12, value: .unsigned(issuedAt)),
                M1CBORField(label: 13, value: .unsigned(fixture.expiresAt)),
            ]
        }

        var requestFields = common(.operationRequest, issuedAt: fixture.requestIssuedAt)
        requestFields.append(contentsOf: [
            M1CBORField(label: 14, value: .bytes(try Data(m1Hex: fixture.requestNonceHex))),
            M1CBORField(label: 15, value: .bytes(payload)),
            M1CBORField(label: 16, value: .bytes(M1ContractCrypto.sha256(payload))),
        ])
        let request = try assembleGolden(
            .map(requestFields), as: .operationRequest, with: appPrivateKey,
            expectedSignature: Data(m1Hex: fixture.requestSignatureHex)
        )
        let decodedRequest = try M5UploadMessage.decode(
            request, appPublicKey: appPrivateKey.publicKey, helperPublicKey: helperPrivateKey.publicKey
        )

        var queryFields = common(.attestationQuery, issuedAt: fixture.queryIssuedAt)
        queryFields.append(contentsOf: [
            M1CBORField(label: 17, value: .bytes(decodedRequest.digest)),
            M1CBORField(label: 30, value: .bytes(try Data(m1Hex: fixture.queryNonceHex))),
        ])
        let query = try assembleGolden(
            .map(queryFields), as: .attestationQuery, with: appPrivateKey,
            expectedSignature: Data(m1Hex: fixture.querySignatureHex)
        )
        let decodedQuery = try M5UploadMessage.decode(
            query, appPublicKey: appPrivateKey.publicKey, helperPublicKey: helperPrivateKey.publicKey
        )

        var attestationFields = common(.uploadAttestation, issuedAt: fixture.attestationIssuedAt)
        attestationFields.append(contentsOf: [
            M1CBORField(label: 16, value: .bytes(M1ContractCrypto.sha256(payload))),
            M1CBORField(label: 17, value: .bytes(decodedRequest.digest)),
            M1CBORField(label: 18, value: .bytes(try Data(m1Hex: fixture.helperNonceHex))),
            M1CBORField(label: 19, value: .unsigned(fixture.attestationIssuedAt)),
            M1CBORField(label: 30, value: .bytes(try Data(m1Hex: fixture.queryNonceHex))),
            M1CBORField(label: 31, value: .bytes(decodedQuery.digest)),
        ])
        let attestation = try assembleGolden(
            .map(attestationFields), as: .uploadAttestation, with: helperPrivateKey,
            expectedSignature: Data(m1Hex: fixture.attestationSignatureHex)
        )
        return M5UploadGoldenMessages(request: request, query: query, attestation: attestation)
    }

    static func makeHelperClockBehindAttestation(
        fixture: M5UploadFixture,
        request: M5UploadMessage,
        query: M5UploadMessage
    ) throws -> Data {
        let helperPrivateKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(m1Hex: fixture.helperSeedHex)
        )
        let helperTime = fixture.requestIssuedAt - 60
        let copiedLabels: [UInt64] = [1, 2, 3, 5, 6, 7, 8, 9, 10, 11]
        var fields = try copiedLabels.map { label -> M1CBORField in
            guard let value = request.fields[label] else {
                throw M1ContractTestError.invalid("missing skew fixture field")
            }
            return M1CBORField(label: label, value: value)
        }
        fields.append(contentsOf: [
            M1CBORField(label: 4, value: .unsigned(M5UploadMessageType.uploadAttestation.rawValue)),
            M1CBORField(label: 12, value: .unsigned(helperTime)),
            M1CBORField(label: 13, value: .unsigned(helperTime + 600)),
            M1CBORField(label: 16, value: .bytes(try request.bytes(16, count: 32))),
            M1CBORField(label: 17, value: .bytes(request.digest)),
            M1CBORField(label: 18, value: .bytes(try Data(m1Hex: fixture.helperNonceHex))),
            M1CBORField(label: 19, value: .unsigned(helperTime)),
            M1CBORField(label: 30, value: .bytes(try query.bytes(30, count: 32))),
            M1CBORField(label: 31, value: .bytes(query.digest)),
        ])
        fields.sort { $0.label < $1.label }
        return try signFresh(.map(fields), as: .uploadAttestation, with: helperPrivateKey)
    }

    private static func assembleGolden(
        _ value: M1CBORValue,
        as type: M5UploadMessageType,
        with key: Curve25519.Signing.PrivateKey,
        expectedSignature: Data
    ) throws -> Data {
        let body = try M1DeterministicCBOR.encode(value)
        guard let domain = M5UploadMessage.domains[type], case .map(var fields) = value else {
            throw M1ContractTestError.invalid("missing M5 signature domain")
        }
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        // CryptoKit 26 hedges Ed25519 signing with fresh randomness, so a valid
        // signature is intentionally not byte-stable. The shared fixture pins
        // the RFC 8032 deterministic Go signature; Swift verifies and embeds
        // those exact golden bytes while separately proving its fresh signer.
        guard key.publicKey.isValidSignature(expectedSignature, for: signedInput) else {
            throw M1ContractTestError.invalid("invalid pinned M5 golden signature")
        }
        let freshSignature = try key.signature(for: signedInput)
        guard key.publicKey.isValidSignature(freshSignature, for: signedInput) else {
            throw M1ContractTestError.invalid("invalid fresh M5 CryptoKit signature")
        }
        fields.append(M1CBORField(label: 255, value: .bytes(expectedSignature)))
        return try M1DeterministicCBOR.encode(.map(fields))
    }

    private static func signFresh(
        _ value: M1CBORValue,
        as type: M5UploadMessageType,
        with key: Curve25519.Signing.PrivateKey
    ) throws -> Data {
        let body = try M1DeterministicCBOR.encode(value)
        guard let domain = M5UploadMessage.domains[type], case .map(var fields) = value else {
            throw M1ContractTestError.invalid("missing fresh M5 signature domain")
        }
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        fields.append(M1CBORField(label: 255, value: .bytes(try key.signature(for: signedInput))))
        return try M1DeterministicCBOR.encode(.map(fields))
    }
}

enum M5MockTransportCategory: Equatable {
    case signedBytes(Data)
    case pending
    case acceptedHTTP
    case unreachable
    case timestamp(UInt64)
}

struct M5MockLocalChannelResponse {
    let pinned: Bool
    let exactQuery: Data
    let category: M5MockTransportCategory
}

enum M5UploadPhase: Equatable {
    case checking
    case completed
    case cancelled
    case interrupted
    case timedOut
}

struct M5UploadEvidence: Equatable {
    var uploadObserved = false
    var downloadObserved = false
    var roundtripConfirmed = false
    var cleanupAttempts = 0
}

struct M5ActiveUpload {
    let request: M5UploadMessage
    let query: M5UploadMessage
    let appPublicKey: Curve25519.Signing.PublicKey
    let helperPublicKey: Curve25519.Signing.PublicKey
}

struct M5UploadAcceptanceModel {
    private(set) var phase: M5UploadPhase = .checking
    private(set) var evidence = M5UploadEvidence()
    private(set) var active: M5ActiveUpload?

    init(active: M5ActiveUpload) {
        self.active = active
    }

    mutating func accept(_ response: M5MockLocalChannelResponse, now: UInt64) {
        guard phase == .checking, let active, response.pinned,
              response.exactQuery == active.query.canonical,
              case .signedBytes(let bytes) = response.category else {
            return
        }
        do {
            try active.request.validateClock(now: now)
            try active.query.validateClock(now: now)
            let attestation = try M5UploadMessage.decode(
                bytes, appPublicKey: active.appPublicKey, helperPublicKey: active.helperPublicKey
            )
            try attestation.validateClock(now: now)
            try M5UploadMessage.validateUploadChain(
                request: active.request, query: active.query, attestation: attestation
            )
            evidence.uploadObserved = true
            phase = .completed
        } catch {
            return
        }
    }

    mutating func cleanupAttempt() {
        evidence.cleanupAttempts += 1
    }

    mutating func cancel() {
        terminate(.cancelled)
    }

    mutating func timeout() {
        terminate(.timedOut)
    }

    mutating func appRestart() {
        terminate(.interrupted)
        active = nil
    }

    mutating func helperRestart() {
        terminate(.interrupted)
    }

    private mutating func terminate(_ terminal: M5UploadPhase) {
        guard phase == .checking else { return }
        phase = terminal
    }
}

enum M5UploadTestPayload {
    static let byteCount = 256

    static func random(using generator: inout some RandomNumberGenerator) -> Data {
        Data((0..<byteCount).map { _ in UInt8.random(in: .min ... .max, using: &generator) })
    }
}

enum M5BoundedPollModel {
    static let delays: [UInt64] = [2, 4, 8, 16, 30, 60, 120, 120]

    static func categories(_ input: [M5MockTransportCategory]) -> [M5MockTransportCategory] {
        Array(input.prefix(delays.count))
    }
}

struct M5AppOperationLimiter {
    static let maximumActive = 2

    private(set) var activeTuples: Set<String> = []

    mutating func begin(tuple: String) -> Bool {
        guard !activeTuples.contains(tuple), activeTuples.count < Self.maximumActive else {
            return false
        }
        activeTuples.insert(tuple)
        return true
    }

    mutating func finish(tuple: String) {
        activeTuples.remove(tuple)
    }
}

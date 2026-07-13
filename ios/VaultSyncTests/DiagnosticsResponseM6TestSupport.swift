import CryptoKit
import Foundation

struct M6ResponseFixture: Decodable {
    let fixtureVersion: Int
    let sourceDecision: String
    let baseFixture: String
    let authorizationNonceHex: String
    let responseNonceHex: String
    let responsePayloadHex: String
    let authorizationIssuedAt: UInt64
    let responseIssuedAt: UInt64
    let cleanupIssuedAt: UInt64
    let cleanupAckIssuedAt: UInt64
    let authorizationBodyHex: String
    let authorizationDigestHex: String
    let authorizationSignatureHex: String
    let authorizationMessageHex: String
    let responseBodyHex: String
    let responseDigestHex: String
    let responseSignatureHex: String
    let responseMessageHex: String
    let cleanupTargetsHex: [String]
    let cleanupResults: [UInt64]
    let cleanupRequestBodyHex: String
    let cleanupRequestDigestHex: String
    let cleanupRequestSignatureHex: String
    let cleanupRequestMessageHex: String
    let cleanupAckBodyHex: String
    let cleanupAckDigestHex: String
    let cleanupAckSignatureHex: String
    let cleanupAckMessageHex: String
}

private final class M6ResponseFixtureBundleToken {}

enum M6ResponseFixtureLoader {
    static func load(filePath: StaticString = #filePath) throws -> M6ResponseFixture {
        let bundle = Bundle(for: M6ResponseFixtureBundleToken.self)
        let bundled = bundle.url(forResource: "diagnostics-response-m6", withExtension: "json")
        let sourceFallback = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures", isDirectory: true)
            .appendingPathComponent("diagnostics-response-m6.json")
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(M6ResponseFixture.self, from: Data(contentsOf: bundled ?? sourceFallback))
    }
}

enum M6ResponseMessageType: UInt64 {
    case responseAuthorization = 6
    case responseArtifact = 7
    case cleanupRequest = 8
    case cleanupAck = 9
}

struct M6ResponseMessage {
    static let capability = "eu.vaultsync.diagnostics.correlated-roundtrip/1"
    static let domains: [M6ResponseMessageType: String] = [
        .responseAuthorization: "eu.vaultsync.roundtrip/v1/response-authorization\0",
        .responseArtifact: "eu.vaultsync.roundtrip/v1/response-artifact\0",
        .cleanupRequest: "eu.vaultsync.roundtrip/v1/cleanup-request\0",
        .cleanupAck: "eu.vaultsync.roundtrip/v1/cleanup-ack\0",
    ]

    let type: M6ResponseMessageType
    let canonical: Data
    let body: Data
    let digest: Data
    let fields: [UInt64: M1CBORValue]

    static func decode(
        _ data: Data,
        appPublicKey: Curve25519.Signing.PublicKey,
        helperPublicKey: Curve25519.Signing.PublicKey
    ) throws -> M6ResponseMessage {
        let value = try M1DeterministicCBOR.decode(data)
        guard case .map(let fieldList) = value,
              let typeValue = fieldList.first(where: { $0.label == 4 })?.value,
              case .unsigned(let rawType) = typeValue,
              let type = M6ResponseMessageType(rawValue: rawType),
              let expectedLabels = expectedLabels[type],
              fieldList.map(\.label) == expectedLabels else {
            throw M1ContractTestError.invalid("unknown, missing, or reordered response field")
        }
        let fields = Dictionary(uniqueKeysWithValues: fieldList.map { ($0.label, $0.value) })
        try validateFields(fields, type: type, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey)
        guard case .bytes(let signature) = fields[255], signature.count == 64,
              let domain = domains[type] else {
            throw M1ContractTestError.invalid("missing response signature")
        }
        let unsigned = M1CBORValue.map(fieldList.filter { $0.label != 255 })
        let body = try M1DeterministicCBOR.encode(unsigned)
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        let signer: Curve25519.Signing.PublicKey =
            type == .responseArtifact || type == .cleanupAck ? helperPublicKey : appPublicKey
        guard signer.isValidSignature(signature, for: signedInput) else {
            throw M1ContractTestError.invalid("invalid response signature")
        }
        return M6ResponseMessage(
            type: type,
            canonical: data,
            body: body,
            digest: M1ContractCrypto.sha256(domain: domain, body: body),
            fields: fields
        )
    }

    static func validateAuthorizationChain(
        request: M5UploadMessage,
        attestation: M5UploadMessage,
        authorization: M6ResponseMessage
    ) throws {
        guard request.type == .operationRequest,
              attestation.type == .uploadAttestation,
              authorization.type == .responseAuthorization,
              commonFieldsEqual(request.fields, attestation.fields),
              commonFieldsEqual(request.fields, authorization.fields),
              try attestation.bytes(16, count: 32) == request.bytes(16, count: 32),
              try attestation.bytes(17, count: 32) == request.digest,
              try authorization.bytes(17, count: 32) == request.digest,
              try authorization.bytes(20, count: 32) == attestation.digest,
              try authorization.uint(12) >= request.uint(12),
              try attestation.uint(13) <= request.uint(13),
              try authorization.uint(13) <= request.uint(13),
              try authorization.uint(13) <= attestation.uint(13) else {
            throw M1ContractTestError.invalid("response authorization chain mismatch")
        }
    }

    static func validateResponseChain(
        request: M5UploadMessage,
        attestation: M5UploadMessage,
        authorization: M6ResponseMessage,
        response: M6ResponseMessage
    ) throws {
        try validateAuthorizationChain(request: request, attestation: attestation, authorization: authorization)
        guard response.type == .responseArtifact,
              commonFieldsEqual(authorization.fields, response.fields),
              try response.bytes(17, count: 32) == request.digest,
              try response.bytes(20, count: 32) == attestation.digest,
              try response.bytes(22, count: 32) == authorization.digest,
              try response.uint(13) <= request.uint(13),
              try response.uint(13) <= attestation.uint(13),
              try response.uint(13) <= authorization.uint(13) else {
            throw M1ContractTestError.invalid("response artifact chain mismatch")
        }
    }

    static func validateCleanupChain(request: M6ResponseMessage, acknowledgment: M6ResponseMessage) throws {
        guard request.type == .cleanupRequest,
              acknowledgment.type == .cleanupAck,
              commonFieldsEqual(request.fields, acknowledgment.fields),
              try request.targets() == acknowledgment.targets(),
              try acknowledgment.bytes(31, count: 32) == request.digest,
              try acknowledgment.uint(13) <= request.uint(13) else {
            throw M1ContractTestError.invalid("cleanup acknowledgment chain mismatch")
        }
    }

    func validateClock(now: UInt64) throws {
        let issued = try uint(12)
        let expires = try uint(13)
        if issued > now, issued - now > 120 {
            throw M1ContractTestError.invalid("issued too far in the future")
        }
        if now > expires, now - expires > 120 {
            throw M1ContractTestError.invalid("expired response message")
        }
    }

    func bytes(_ label: UInt64, count: Int) throws -> Data {
        guard case .bytes(let value) = fields[label], value.count == count else {
            throw M1ContractTestError.invalid("wrong response byte field")
        }
        return value
    }

    func uint(_ label: UInt64) throws -> UInt64 {
        guard case .unsigned(let value) = fields[label] else {
            throw M1ContractTestError.invalid("wrong response uint field")
        }
        return value
    }

    func targets() throws -> [Data] {
        guard case .array(let values) = fields[28] else {
            throw M1ContractTestError.invalid("missing cleanup targets")
        }
        return try values.map { value in
            guard case .bytes(let target) = value, target.count == 32 else {
                throw M1ContractTestError.invalid("invalid cleanup target")
            }
            return target
        }
    }

    func results() throws -> [UInt64] {
        guard case .array(let values) = fields[29] else {
            throw M1ContractTestError.invalid("missing cleanup results")
        }
        return try values.map { value in
            guard case .unsigned(let result) = value else {
                throw M1ContractTestError.invalid("invalid cleanup result")
            }
            return result
        }
    }

    private static let expectedLabels: [M6ResponseMessageType: [UInt64]] = [
        .responseAuthorization: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 20, 21, 255],
        .responseArtifact: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 20, 22, 23, 24, 25, 255],
        .cleanupRequest: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 28, 255],
        .cleanupAck: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 28, 29, 31, 255],
    ]

    private static func validateFields(
        _ fields: [UInt64: M1CBORValue],
        type: M6ResponseMessageType,
        appPublicKey: Curve25519.Signing.PublicKey,
        helperPublicKey: Curve25519.Signing.PublicKey
    ) throws {
        guard fields[1] == .text(capability), fields[2] == .unsigned(1), fields[3] == .unsigned(1),
              fields[4] == .unsigned(type.rawValue) else {
            throw M1ContractTestError.invalid("wrong response capability/version/type")
        }
        for label in [5, 6, 7, 8, 11] {
            guard case .bytes(let value) = fields[UInt64(label)], value.count == 32,
                  value.contains(where: { $0 != 0 }) else {
                throw M1ContractTestError.invalid("wrong response binding/key/operation")
            }
        }
        guard fields[7] == .bytes(M1ContractCrypto.keyID(publicKey: appPublicKey.rawRepresentation)),
              fields[8] == .bytes(M1ContractCrypto.keyID(publicKey: helperPublicKey.rawRepresentation)),
              case .unsigned(let appEpoch) = fields[9], appEpoch > 0,
              case .unsigned(let helperEpoch) = fields[10], helperEpoch > 0,
              case .unsigned(let issued) = fields[12], issued > 0,
              case .unsigned(let expires) = fields[13], expires > issued, expires - issued <= 600 else {
            throw M1ContractTestError.invalid("wrong response key or lifetime")
        }
        switch type {
        case .responseAuthorization:
            try validateNonzeroByteFields(fields, labels: [17, 20, 21])
        case .responseArtifact:
            try validateNonzeroByteFields(fields, labels: [17, 20, 22, 23])
            guard case .bytes(let payload) = fields[24], payload.count == 256,
                  fields[25] == .bytes(M1ContractCrypto.sha256(payload)) else {
                throw M1ContractTestError.invalid("wrong response payload")
            }
        case .cleanupRequest:
            _ = try validateTargets(fields)
        case .cleanupAck:
            let targets = try validateTargets(fields)
            guard case .array(let resultValues) = fields[29], resultValues.count == targets.count else {
                throw M1ContractTestError.invalid("cleanup result count mismatch")
            }
            for value in resultValues {
                guard case .unsigned(let result) = value, (1 ... 4).contains(result) else {
                    throw M1ContractTestError.invalid("unknown cleanup result")
                }
            }
            try validateNonzeroByteFields(fields, labels: [31])
        }
    }

    private static func validateNonzeroByteFields(_ fields: [UInt64: M1CBORValue], labels: [UInt64]) throws {
        for label in labels {
            guard case .bytes(let value) = fields[label], value.count == 32,
                  value.contains(where: { $0 != 0 }) else {
                throw M1ContractTestError.invalid("wrong response digest or nonce")
            }
        }
    }

    private static func validateTargets(_ fields: [UInt64: M1CBORValue]) throws -> [Data] {
        guard case .array(let values) = fields[28], (1 ... 3).contains(values.count) else {
            throw M1ContractTestError.invalid("wrong cleanup target count")
        }
        let targets = try values.map { value -> Data in
            guard case .bytes(let target) = value, target.count == 32,
                  target.contains(where: { $0 != 0 }) else {
                throw M1ContractTestError.invalid("wrong cleanup target")
            }
            return target
        }
        for index in targets.indices.dropFirst() where !targets[index - 1].lexicographicallyPrecedes(targets[index]) {
            throw M1ContractTestError.invalid("cleanup targets are not sorted and unique")
        }
        return targets
    }

    private static func commonFieldsEqual(
        _ left: [UInt64: M1CBORValue],
        _ right: [UInt64: M1CBORValue]
    ) -> Bool {
        [1, 2, 3, 5, 6, 7, 8, 9, 10, 11].allSatisfy { left[$0] == right[$0] }
    }
}

struct M6ResponseGoldenMessages {
    let authorization: Data
    let response: Data
    let cleanupRequest: Data
    let cleanupAck: Data

    static func make(m5: M5UploadFixture, m6: M6ResponseFixture) throws -> M6ResponseGoldenMessages {
        let upload = try M5UploadGoldenMessages.make(m5)
        let appPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.appSeedHex))
        let helperPrivateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: m5.helperSeedHex))
        let appPublicKey = appPrivateKey.publicKey
        let helperPublicKey = helperPrivateKey.publicKey
        let request = try M5UploadMessage.decode(upload.request, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey)
        let attestation = try M5UploadMessage.decode(upload.attestation, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey)
        let appKeyID = M1ContractCrypto.keyID(publicKey: appPublicKey.rawRepresentation)
        let helperKeyID = M1ContractCrypto.keyID(publicKey: helperPublicKey.rawRepresentation)
        let homeserver = try Data(m1Hex: m5.homeserverBindingHex)
        let folder = try Data(m1Hex: m5.folderBindingHex)
        let operation = try Data(m1Hex: m5.operationIdHex)

        func common(_ type: M6ResponseMessageType, issuedAt: UInt64) -> [M1CBORField] {
            [
                M1CBORField(label: 1, value: .text(M6ResponseMessage.capability)),
                M1CBORField(label: 2, value: .unsigned(1)),
                M1CBORField(label: 3, value: .unsigned(1)),
                M1CBORField(label: 4, value: .unsigned(type.rawValue)),
                M1CBORField(label: 5, value: .bytes(homeserver)),
                M1CBORField(label: 6, value: .bytes(folder)),
                M1CBORField(label: 7, value: .bytes(appKeyID)),
                M1CBORField(label: 8, value: .bytes(helperKeyID)),
                M1CBORField(label: 9, value: .unsigned(m5.appEpoch)),
                M1CBORField(label: 10, value: .unsigned(m5.helperEpoch)),
                M1CBORField(label: 11, value: .bytes(operation)),
                M1CBORField(label: 12, value: .unsigned(issuedAt)),
                M1CBORField(label: 13, value: .unsigned(m5.expiresAt)),
            ]
        }

        var authorizationFields = common(.responseAuthorization, issuedAt: m6.authorizationIssuedAt)
        authorizationFields.append(contentsOf: [
            M1CBORField(label: 17, value: .bytes(request.digest)),
            M1CBORField(label: 20, value: .bytes(attestation.digest)),
            M1CBORField(label: 21, value: .bytes(try Data(m1Hex: m6.authorizationNonceHex))),
        ])
        let authorization = try assembleGolden(
            .map(authorizationFields), as: .responseAuthorization, with: appPrivateKey,
            expectedSignature: Data(m1Hex: m6.authorizationSignatureHex)
        )
        let decodedAuthorization = try M6ResponseMessage.decode(
            authorization, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )

        let responsePayload = try Data(m1Hex: m6.responsePayloadHex)
        var responseFields = common(.responseArtifact, issuedAt: m6.responseIssuedAt)
        responseFields.append(contentsOf: [
            M1CBORField(label: 17, value: .bytes(request.digest)),
            M1CBORField(label: 20, value: .bytes(attestation.digest)),
            M1CBORField(label: 22, value: .bytes(decodedAuthorization.digest)),
            M1CBORField(label: 23, value: .bytes(try Data(m1Hex: m6.responseNonceHex))),
            M1CBORField(label: 24, value: .bytes(responsePayload)),
            M1CBORField(label: 25, value: .bytes(M1ContractCrypto.sha256(responsePayload))),
        ])
        let response = try assembleGolden(
            .map(responseFields), as: .responseArtifact, with: helperPrivateKey,
            expectedSignature: Data(m1Hex: m6.responseSignatureHex)
        )

        let targetValues = try m6.cleanupTargetsHex.map { M1CBORValue.bytes(try Data(m1Hex: $0)) }
        var cleanupRequestFields = common(.cleanupRequest, issuedAt: m6.cleanupIssuedAt)
        cleanupRequestFields.append(M1CBORField(label: 28, value: .array(targetValues)))
        let cleanupRequest = try assembleGolden(
            .map(cleanupRequestFields), as: .cleanupRequest, with: appPrivateKey,
            expectedSignature: Data(m1Hex: m6.cleanupRequestSignatureHex)
        )
        let decodedCleanupRequest = try M6ResponseMessage.decode(
            cleanupRequest, appPublicKey: appPublicKey, helperPublicKey: helperPublicKey
        )

        var cleanupAckFields = common(.cleanupAck, issuedAt: m6.cleanupAckIssuedAt)
        cleanupAckFields.append(contentsOf: [
            M1CBORField(label: 28, value: .array(targetValues)),
            M1CBORField(label: 29, value: .array(m6.cleanupResults.map(M1CBORValue.unsigned))),
            M1CBORField(label: 31, value: .bytes(decodedCleanupRequest.digest)),
        ])
        let cleanupAck = try assembleGolden(
            .map(cleanupAckFields), as: .cleanupAck, with: helperPrivateKey,
            expectedSignature: Data(m1Hex: m6.cleanupAckSignatureHex)
        )
        return M6ResponseGoldenMessages(
            authorization: authorization,
            response: response,
            cleanupRequest: cleanupRequest,
            cleanupAck: cleanupAck
        )
    }

    private static func assembleGolden(
        _ value: M1CBORValue,
        as type: M6ResponseMessageType,
        with key: Curve25519.Signing.PrivateKey,
        expectedSignature: Data
    ) throws -> Data {
        let body = try M1DeterministicCBOR.encode(value)
        guard let domain = M6ResponseMessage.domains[type], case .map(var fields) = value else {
            throw M1ContractTestError.invalid("missing M6 signature domain")
        }
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        guard key.publicKey.isValidSignature(expectedSignature, for: signedInput) else {
            throw M1ContractTestError.invalid("invalid pinned M6 golden signature")
        }
        let freshSignature = try key.signature(for: signedInput)
        guard key.publicKey.isValidSignature(freshSignature, for: signedInput) else {
            throw M1ContractTestError.invalid("invalid fresh M6 CryptoKit signature")
        }
        fields.append(M1CBORField(label: 255, value: .bytes(expectedSignature)))
        return try M1DeterministicCBOR.encode(.map(fields))
    }
}

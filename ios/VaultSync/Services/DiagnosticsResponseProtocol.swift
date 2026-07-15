import CryptoKit
import Foundation

enum DiagnosticsResponseProtocol {
    static let path = "/api/v1/diagnostics/authorize-response"

    enum MessageType: UInt64, Sendable {
        case responseAuthorization = 6
        case responseArtifact = 7
    }

    struct Message: Equatable, Sendable {
        let type: MessageType
        let canonical: Data
        let value: DiagnosticsCBORValue
        let body: Data
        let digest: Data
    }

    private static let domains: [MessageType: String] = [
        .responseAuthorization: "eu.vaultsync.roundtrip/v1/response-authorization\0",
        .responseArtifact: "eu.vaultsync.roundtrip/v1/response-artifact\0",
    ]

    private static let expectedLabels: [MessageType: [UInt64]] = [
        .responseAuthorization: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 20, 21, 255],
        .responseArtifact: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 20, 22, 23, 24, 25, 255],
    ]

    static func makeAuthorization(
        record: DiagnosticsPairingRecord,
        appKey: Curve25519.Signing.PrivateKey,
        operation: DiagnosticsUploadProtocol.Operation,
        attestation: DiagnosticsUploadProtocol.Message,
        authorizationNonce: Data,
        now: Date
    ) throws -> Message {
        guard record.state == .namespaceActive,
              appKey.publicKey.rawRepresentation == record.appPublicKey,
              attestation.type == .uploadAttestation,
              authorizationNonce.count == 32,
              authorizationNonce.contains(where: { $0 != 0 }),
              let requestIssued = operation.request.value.unsigned(for: 12),
              let requestExpiry = operation.request.value.unsigned(for: 13),
              let attestationExpiry = attestation.value.unsigned(for: 13) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let issuedAt = try unixSeconds(now)
        // The authorization can never outlive the request or attestation it
        // binds; the helper enforces the same chain ordering.
        let expiresAt = min(
            try DiagnosticsPairingProtocol.checkedAdding(issuedAt, DiagnosticsUploadProtocol.maximumLifetime),
            requestExpiry,
            attestationExpiry
        )
        guard issuedAt >= requestIssued, expiresAt > issuedAt else {
            throw DiagnosticsProtocolError.expired
        }
        let fields: [DiagnosticsCBORField] = [
            DiagnosticsCBORField(label: 1, value: .text(DiagnosticsUploadProtocol.capability)),
            DiagnosticsCBORField(label: 2, value: .unsigned(1)),
            DiagnosticsCBORField(label: 3, value: .unsigned(1)),
            DiagnosticsCBORField(label: 4, value: .unsigned(MessageType.responseAuthorization.rawValue)),
            DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
            DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
            DiagnosticsCBORField(label: 7, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 8, value: .bytes(record.helperKeyID)),
            DiagnosticsCBORField(label: 9, value: .unsigned(record.appEpoch)),
            DiagnosticsCBORField(label: 10, value: .unsigned(record.helperEpoch)),
            DiagnosticsCBORField(label: 11, value: .bytes(operation.operationID)),
            DiagnosticsCBORField(label: 12, value: .unsigned(issuedAt)),
            DiagnosticsCBORField(label: 13, value: .unsigned(expiresAt)),
            DiagnosticsCBORField(label: 17, value: .bytes(operation.request.digest)),
            DiagnosticsCBORField(label: 20, value: .bytes(attestation.digest)),
            DiagnosticsCBORField(label: 21, value: .bytes(authorizationNonce)),
        ]
        let authorization = try sign(.map(fields), as: .responseAuthorization, with: appKey, record: record)
        try validateAuthorizationChain(
            operation: operation,
            attestation: attestation,
            authorization: authorization
        )
        return authorization
    }

    static func validateResponseArtifact(
        _ data: Data,
        operation: DiagnosticsUploadProtocol.Operation,
        attestation: DiagnosticsUploadProtocol.Message,
        authorization: Message,
        record: DiagnosticsPairingRecord,
        now: Date
    ) throws -> Message {
        try validateClock(operation.request.value, now: now)
        try validateClock(authorization.value, now: now)
        let response = try decode(data, record: record)
        try validateClock(response.value, now: now)
        try validateAuthorizationChain(
            operation: operation,
            attestation: attestation,
            authorization: authorization
        )
        guard response.type == .responseArtifact,
              commonFieldsEqual(authorization.value, response.value),
              response.value.bytes(for: 17, count: 32) == operation.request.digest,
              response.value.bytes(for: 20, count: 32) == attestation.digest,
              response.value.bytes(for: 22, count: 32) == authorization.digest,
              let responseExpiry = response.value.unsigned(for: 13),
              let requestExpiry = operation.request.value.unsigned(for: 13),
              let attestationExpiry = attestation.value.unsigned(for: 13),
              let authorizationExpiry = authorization.value.unsigned(for: 13),
              responseExpiry <= requestExpiry,
              responseExpiry <= attestationExpiry,
              responseExpiry <= authorizationExpiry else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return response
    }

    static func decode(_ data: Data, record: DiagnosticsPairingRecord) throws -> Message {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        guard let rawType = value.unsigned(for: 4),
              let type = MessageType(rawValue: rawType),
              value.fields?.map(\.label) == expectedLabels[type],
              let domain = domains[type] else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        try validateFields(value, type: type, record: record)
        guard let signature = value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        let publicBytes = type == .responseArtifact ? record.helperPublicKey : record.appPublicKey
        let publicKey = try Curve25519.Signing.PublicKey(rawRepresentation: publicBytes)
        guard publicKey.isValidSignature(signature, for: signedInput) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return Message(
            type: type,
            canonical: data,
            value: value,
            body: body,
            digest: DiagnosticsCrypto.sha256(domain: domain, body: body)
        )
    }

    struct DownloadEvent: Decodable, Equatable, Sendable {
        let id: Int64
        let type: String
        let time: String
        let data: [String: String]?
    }

    struct DownloadEventSnapshot: Equatable, Sendable {
        let generation: Int64
        let events: [DownloadEvent]
    }

    static func eventSnapshot(generation: Int64, json: String) -> DownloadEventSnapshot? {
        guard let data = json.data(using: .utf8),
              let events = try? JSONDecoder().decode([DownloadEvent].self, from: data) else {
            return nil
        }
        return DownloadEventSnapshot(generation: generation, events: events)
    }

    // A response artifact may only be accepted from a successful local apply
    // of the exact expected path that is newer than both the post-upload
    // cursor and wall-clock baselines (D024 step 9); anything else is stale.
    static func freshResponseApply(
        _ event: DownloadEvent,
        folderID: String,
        relativePath: String,
        baseline: Date,
        cursor: Int64
    ) -> Bool {
        guard event.id > cursor,
              event.type == "ItemFinished",
              let data = event.data,
              data["folder"] == folderID,
              data["item"] == relativePath,
              data["type"] == "file",
              data["action"] == "update",
              data["error", default: ""].isEmpty,
              let observedAt = SyncBridgeService.parseBridgeTimestamp(event.time),
              observedAt >= baseline else {
            return false
        }
        return true
    }

    private static func sign(
        _ value: DiagnosticsCBORValue,
        as type: MessageType,
        with key: Curve25519.Signing.PrivateKey,
        record: DiagnosticsPairingRecord
    ) throws -> Message {
        guard let domain = domains[type], case .map(var fields) = value else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value)
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        fields.append(DiagnosticsCBORField(label: 255, value: .bytes(try key.signature(for: signedInput))))
        return try decode(try DiagnosticsDeterministicCBOR.encode(.map(fields)), record: record)
    }

    private static func validateAuthorizationChain(
        operation: DiagnosticsUploadProtocol.Operation,
        attestation: DiagnosticsUploadProtocol.Message,
        authorization: Message
    ) throws {
        guard operation.request.type == .operationRequest,
              attestation.type == .uploadAttestation,
              authorization.type == .responseAuthorization,
              commonFieldsEqual(operation.request.value, authorization.value),
              authorization.value.bytes(for: 17, count: 32) == operation.request.digest,
              authorization.value.bytes(for: 20, count: 32) == attestation.digest,
              let requestIssued = operation.request.value.unsigned(for: 12),
              let requestExpiry = operation.request.value.unsigned(for: 13),
              let attestationExpiry = attestation.value.unsigned(for: 13),
              let authorizationIssued = authorization.value.unsigned(for: 12),
              let authorizationExpiry = authorization.value.unsigned(for: 13),
              authorizationIssued >= requestIssued,
              authorizationExpiry <= requestExpiry,
              authorizationExpiry <= attestationExpiry else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    private static func validateFields(
        _ value: DiagnosticsCBORValue,
        type: MessageType,
        record: DiagnosticsPairingRecord
    ) throws {
        guard value.text(for: 1) == DiagnosticsUploadProtocol.capability,
              value.unsigned(for: 2) == 1,
              value.unsigned(for: 3) == 1,
              value.unsigned(for: 4) == type.rawValue,
              value.bytes(for: 5, count: 32) == record.homeserverBinding,
              value.bytes(for: 6, count: 32) == record.folderBinding,
              value.bytes(for: 7, count: 32) == record.appKeyID,
              value.bytes(for: 8, count: 32) == record.helperKeyID,
              value.unsigned(for: 9) == record.appEpoch,
              value.unsigned(for: 10) == record.helperEpoch,
              value.bytes(for: 11, count: 32)?.contains(where: { $0 != 0 }) == true,
              record.appKeyID == DiagnosticsCrypto.keyID(publicKey: record.appPublicKey),
              record.helperKeyID == DiagnosticsCrypto.keyID(publicKey: record.helperPublicKey),
              let issuedAt = value.unsigned(for: 12), issuedAt > 0,
              let expiresAt = value.unsigned(for: 13),
              expiresAt > issuedAt,
              expiresAt - issuedAt <= DiagnosticsUploadProtocol.maximumLifetime else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        switch type {
        case .responseAuthorization:
            guard value.bytes(for: 17, count: 32) != nil,
                  value.bytes(for: 20, count: 32) != nil,
                  value.bytes(for: 21, count: 32)?.contains(where: { $0 != 0 }) == true else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .responseArtifact:
            guard value.bytes(for: 17, count: 32) != nil,
                  value.bytes(for: 20, count: 32) != nil,
                  value.bytes(for: 22, count: 32) != nil,
                  value.bytes(for: 23, count: 32)?.contains(where: { $0 != 0 }) == true,
                  let payload = value.bytes(for: 24, count: DiagnosticsUploadProtocol.payloadByteCount),
                  value.bytes(for: 25, count: 32) == DiagnosticsCrypto.sha256(payload) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
    }

    private static func validateClock(_ value: DiagnosticsCBORValue, now: Date) throws {
        guard let issuedAt = value.unsigned(for: 12),
              let expiresAt = value.unsigned(for: 13) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let current = try unixSeconds(now)
        if issuedAt > current, issuedAt - current > DiagnosticsUploadProtocol.maximumClockSkew {
            throw DiagnosticsProtocolError.expired
        }
        if current > expiresAt, current - expiresAt > DiagnosticsUploadProtocol.maximumClockSkew {
            throw DiagnosticsProtocolError.expired
        }
    }

    private static func commonFieldsEqual(
        _ lhs: DiagnosticsCBORValue,
        _ rhs: DiagnosticsCBORValue
    ) -> Bool {
        [1, 2, 3, 5, 6, 7, 8, 9, 10, 11].allSatisfy {
            lhs.value(for: $0) == rhs.value(for: $0)
        }
    }

    private static func unixSeconds(_ date: Date) throws -> UInt64 {
        let seconds = date.timeIntervalSince1970.rounded(.down)
        guard seconds >= 0, seconds < Double(UInt64.max) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return UInt64(seconds)
    }
}

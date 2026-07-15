import CryptoKit
import Foundation

enum DiagnosticsUploadProtocol {
    static let path = "/api/v1/diagnostics/attestation"
    static let capability = "eu.vaultsync.diagnostics.correlated-roundtrip/1"
    static let maximumLifetime: UInt64 = 600
    static let maximumClockSkew: UInt64 = 120
    static let payloadByteCount = 256
    static let pollDelays: [UInt64] = [2, 4, 8, 16, 30, 60, 120, 120]

    enum MessageType: UInt64, Sendable {
        case operationRequest = 3
        case attestationQuery = 4
        case uploadAttestation = 5
    }

    struct Message: Equatable, Sendable {
        let type: MessageType
        let canonical: Data
        let value: DiagnosticsCBORValue
        let body: Data
        let digest: Data
    }

    struct Operation: Equatable, Sendable {
        let request: Message
        let query: Message
        let operationID: Data
        let installationBinding: Data
        let requestComponents: [String]
    }

    private static let domains: [MessageType: String] = [
        .operationRequest: "eu.vaultsync.roundtrip/v1/operation-request\0",
        .attestationQuery: "eu.vaultsync.roundtrip/v1/attestation-query\0",
        .uploadAttestation: "eu.vaultsync.roundtrip/v1/upload-attestation\0",
    ]

    private static let expectedLabels: [MessageType: [UInt64]] = [
        .operationRequest: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 255],
        .attestationQuery: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 30, 255],
        .uploadAttestation: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 16, 17, 18, 19, 30, 31, 255],
    ]

    static func makeOperation(
        record: DiagnosticsPairingRecord,
        appKey: Curve25519.Signing.PrivateKey,
        operationID: Data,
        requestNonce: Data,
        queryNonce: Data,
        payload: Data,
        now: Date
    ) throws -> Operation {
        guard record.state == .namespaceActive,
              record.namespaceAuthorizationEpoch > 0,
              let initialAppKeyID = record.namespaceInitialAppKeyID,
              initialAppKeyID.count == 32,
              appKey.publicKey.rawRepresentation == record.appPublicKey,
              operationID.count == 32, operationID.contains(where: { $0 != 0 }),
              requestNonce.count == 32, requestNonce.contains(where: { $0 != 0 }),
              queryNonce.count == 32, queryNonce.contains(where: { $0 != 0 }),
              payload.count == payloadByteCount else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let issuedAt = try unixSeconds(now)
        let expiresAt = try DiagnosticsPairingProtocol.checkedAdding(issuedAt, maximumLifetime)

        func common(_ type: MessageType) -> [DiagnosticsCBORField] {
            [
                DiagnosticsCBORField(label: 1, value: .text(capability)),
                DiagnosticsCBORField(label: 2, value: .unsigned(1)),
                DiagnosticsCBORField(label: 3, value: .unsigned(1)),
                DiagnosticsCBORField(label: 4, value: .unsigned(type.rawValue)),
                DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
                DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
                DiagnosticsCBORField(label: 7, value: .bytes(record.appKeyID)),
                DiagnosticsCBORField(label: 8, value: .bytes(record.helperKeyID)),
                DiagnosticsCBORField(label: 9, value: .unsigned(record.appEpoch)),
                DiagnosticsCBORField(label: 10, value: .unsigned(record.helperEpoch)),
                DiagnosticsCBORField(label: 11, value: .bytes(operationID)),
                DiagnosticsCBORField(label: 12, value: .unsigned(issuedAt)),
                DiagnosticsCBORField(label: 13, value: .unsigned(expiresAt)),
            ]
        }

        let payloadDigest = DiagnosticsCrypto.sha256(payload)
        let request = try sign(
            .map(common(.operationRequest) + [
                DiagnosticsCBORField(label: 14, value: .bytes(requestNonce)),
                DiagnosticsCBORField(label: 15, value: .bytes(payload)),
                DiagnosticsCBORField(label: 16, value: .bytes(payloadDigest)),
            ]),
            as: .operationRequest,
            with: appKey,
            record: record
        )
        let query = try sign(
            .map(common(.attestationQuery) + [
                DiagnosticsCBORField(label: 17, value: .bytes(request.digest)),
                DiagnosticsCBORField(label: 30, value: .bytes(queryNonce)),
            ]),
            as: .attestationQuery,
            with: appKey,
            record: record
        )
        try validateRequestAndQuery(request, query)

        let installation = DiagnosticsNamespaceProtocol.installationBinding(
            initialAppKeyID: initialAppKeyID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding
        )
        return Operation(
            request: request,
            query: query,
            operationID: operationID,
            installationBinding: installation,
            requestComponents: try DiagnosticsNamespaceProtocol.operationRequestComponents(
                installationBinding: installation,
                operationID: operationID
            )
        )
    }

    static func validateUploadAttestation(
        _ data: Data,
        operation: Operation,
        record: DiagnosticsPairingRecord,
        now: Date
    ) throws -> Message {
        try validateClock(operation.request, now: now)
        try validateClock(operation.query, now: now)
        let attestation = try decode(data, record: record)
        try validateClock(attestation, now: now)
        try validateRequestAndQuery(operation.request, operation.query)
        guard attestation.type == .uploadAttestation,
              commonFieldsEqual(operation.request, attestation),
              attestation.value.bytes(for: 16, count: 32) == operation.request.value.bytes(for: 16, count: 32),
              attestation.value.bytes(for: 17, count: 32) == operation.request.digest,
              attestation.value.bytes(for: 30, count: 32) == operation.query.value.bytes(for: 30, count: 32),
              attestation.value.bytes(for: 31, count: 32) == operation.query.digest,
              let attestationExpiry = attestation.value.unsigned(for: 13),
              let requestExpiry = operation.request.value.unsigned(for: 13),
              let queryExpiry = operation.query.value.unsigned(for: 13),
              attestationExpiry <= requestExpiry,
              attestationExpiry <= queryExpiry else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return attestation
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
        let publicBytes = type == .uploadAttestation ? record.helperPublicKey : record.appPublicKey
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

    static func verifyActiveNamespace(
        record: DiagnosticsPairingRecord,
        folderPath: String
    ) throws -> Data {
        guard record.state == .namespaceActive,
              record.namespaceAuthorizationEpoch > 0,
              let initialAppKeyID = record.namespaceInitialAppKeyID,
              let namespaceID = record.namespaceID,
              let rootDigest = record.namespaceRootDigest,
              let manifestDigest = record.namespaceManifestDigest,
              let authorizationDigest = record.namespaceAuthorizationDigest,
              let enablement = record.namespaceEnablement,
              let completedSnapshot = record.lastIncoming else {
            throw DiagnosticsProtocolError.unavailable
        }
        let installation = DiagnosticsNamespaceProtocol.installationBinding(
            initialAppKeyID: initialAppKeyID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding
        )
        let rootData = try DiagnosticsNamespaceFileReader.read(
            folderPath: folderPath,
            components: [DiagnosticsNamespaceProtocol.rootName, DiagnosticsNamespaceProtocol.rootManifestName]
        )
        guard DiagnosticsNamespaceProtocol.recordDigest(rootData) == rootDigest,
              let rootValue = try? DiagnosticsDeterministicCBOR.decode(rootData),
              rootValue.bytes(for: 5, count: 32) == record.homeserverBinding,
              rootValue.bytes(for: 6, count: 32) == record.folderBinding,
              rootValue.bytes(for: 7, count: 32) == namespaceID else {
            throw DiagnosticsProtocolError.conflict
        }
        let root: DiagnosticsNamespaceProtocol.RootManifest
        if record.namespaceAuthorizationEpoch == 1 {
            root = try DiagnosticsNamespaceProtocol.validateRootManifest(
                rootData,
                enablement: enablement,
                record: record
            )
        } else {
            root = DiagnosticsNamespaceProtocol.RootManifest(
                message: rootData,
                namespaceID: namespaceID,
                rootDigest: rootDigest,
                manifestDigest: manifestDigest
            )
        }
        let candidate = DiagnosticsNamespaceProtocol.AuthorizationCandidate(
            message: record.lastOutgoing,
            installationBinding: installation
        )
        let relative: String
        if record.namespaceAuthorizationEpoch == 1 {
            relative = try DiagnosticsNamespaceProtocol.authorizationRelativePath(
                installationBinding: installation
            )
        } else {
            relative = try DiagnosticsNamespaceProtocol.authorizationEpochRelativePath(
                installationBinding: installation,
                epoch: record.namespaceAuthorizationEpoch
            )
        }
        let completed = try DiagnosticsNamespaceFileReader.read(
            folderPath: folderPath,
            components: [DiagnosticsNamespaceProtocol.rootName] + relative.split(separator: "/").map(String.init)
        )
        guard completed == completedSnapshot else {
            throw DiagnosticsProtocolError.conflict
        }
        let digest: Data
        if record.namespaceAuthorizationEpoch == 1 {
            digest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorization(
                completed,
                candidate: candidate,
                record: record,
                root: root
            )
        } else {
            digest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorizationEpoch(
                completed,
                candidate: candidate,
                record: record,
                root: root
            )
        }
        guard digest == authorizationDigest else {
            throw DiagnosticsProtocolError.conflict
        }
        return installation
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

    private static func validateRequestAndQuery(_ request: Message, _ query: Message) throws {
        guard request.type == .operationRequest,
              query.type == .attestationQuery,
              commonFieldsEqual(request, query),
              query.value.bytes(for: 17, count: 32) == request.digest,
              let requestIssued = request.value.unsigned(for: 12),
              let requestExpiry = request.value.unsigned(for: 13),
              let queryIssued = query.value.unsigned(for: 12),
              let queryExpiry = query.value.unsigned(for: 13),
              queryIssued >= requestIssued,
              queryExpiry <= requestExpiry else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    private static func validateFields(
        _ value: DiagnosticsCBORValue,
        type: MessageType,
        record: DiagnosticsPairingRecord
    ) throws {
        guard value.text(for: 1) == capability,
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
              expiresAt - issuedAt <= maximumLifetime else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        switch type {
        case .operationRequest:
            guard value.bytes(for: 14, count: 32)?.contains(where: { $0 != 0 }) == true,
                  let payload = value.bytes(for: 15, count: payloadByteCount),
                  value.bytes(for: 16, count: 32) == DiagnosticsCrypto.sha256(payload) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .attestationQuery:
            guard value.bytes(for: 17, count: 32) != nil,
                  value.bytes(for: 30, count: 32)?.contains(where: { $0 != 0 }) == true else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .uploadAttestation:
            guard value.bytes(for: 16, count: 32) != nil,
                  value.bytes(for: 17, count: 32) != nil,
                  value.bytes(for: 18, count: 32)?.contains(where: { $0 != 0 }) == true,
                  let observedAt = value.unsigned(for: 19), observedAt > 0, observedAt <= issuedAt,
                  value.bytes(for: 30, count: 32)?.contains(where: { $0 != 0 }) == true,
                  value.bytes(for: 31, count: 32) != nil else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
    }

    private static func validateClock(_ message: Message, now: Date) throws {
        guard let issuedAt = message.value.unsigned(for: 12),
              let expiresAt = message.value.unsigned(for: 13) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let current = try unixSeconds(now)
        if issuedAt > current, issuedAt - current > maximumClockSkew {
            throw DiagnosticsProtocolError.expired
        }
        if current > expiresAt, current - expiresAt > maximumClockSkew {
            throw DiagnosticsProtocolError.expired
        }
    }

    private static func commonFieldsEqual(_ lhs: Message, _ rhs: Message) -> Bool {
        [1, 2, 3, 5, 6, 7, 8, 9, 10, 11].allSatisfy {
            lhs.value.value(for: $0) == rhs.value.value(for: $0)
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

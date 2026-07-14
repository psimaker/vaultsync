import CryptoKit
import Foundation

enum DiagnosticsPairingProtocol {
    static let capability = "eu.vaultsync.diagnostics.helper-pairing/1"
    static let path = "/api/v1/diagnostics/pairing"
    static let maximumClockSkew: UInt64 = 120
    static let maximumLifetime: UInt64 = 300

    enum MessageType: UInt64, Codable, Sendable {
        case qr = 0
        case appRequest = 1
        case helperAccept = 2
        case finalize = 3
        case finalizeAck = 4
        case receipt = 5
        case readyAck = 6
        case activate = 7
        case activeAck = 8
        case abort = 9
        case abortAck = 10
        case appKeyRotationRequest = 11
        case appKeyRotationNewProof = 12
        case appKeyRotationAccept = 13
        case helperKeyRotationPropose = 14
        case helperKeyRotationNewProof = 15
        case helperKeyRotationConfirm = 16
        case tlsPinRotationPropose = 17
        case tlsPinRotationConfirm = 18
        case revocationRequest = 19
        case revocationRecord = 20
        case lifecycleFinalize = 21
        case lifecycleActiveAck = 22
        case lifecycleAbort = 23
        case lifecycleAbortAck = 24
    }

    enum TransitionKind: UInt64, Codable, Sendable {
        case appKey = 1
        case helperKey = 2
        case tlsPin = 3
    }

    enum RevocationReason: UInt64, CaseIterable, Codable, Sendable {
        case userRequest = 1
        case lostApp = 2
        case folderRemoved = 3
        case suspectedCompromise = 4
    }

    struct Message: Equatable, Sendable {
        let type: MessageType
        let value: DiagnosticsCBORValue
        let canonical: Data

        var domain: String? { DiagnosticsPairingProtocol.domain(for: type) }

        func digest() throws -> Data {
            guard let domain else { throw DiagnosticsProtocolError.invalidMessage }
            return try DiagnosticsCrypto.signedMessageDigest(domain: domain, value: value)
        }
    }

    static func decodeQR(_ encoded: String, now: Date = Date()) throws -> Message {
        let message = try decode(DiagnosticsCrypto.base64URLDecode(encoded))
        guard message.type == .qr else { throw DiagnosticsProtocolError.invalidMessage }
        try validateClock(message, now: now, allowExpiredSkew: false)
        return message
    }

    static func decode(_ data: Data) throws -> Message {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        guard let rawType = value.unsigned(for: 4), let type = MessageType(rawValue: rawType) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let message = Message(type: type, value: value, canonical: data)
        try validateSchema(message)
        if type != .qr {
            try verifySignature(message)
        }
        return message
    }

    static func makeAppRequest(
        invitation: Message,
        appPrivateKey: Curve25519.Signing.PrivateKey,
        selectedDeviceID: String,
        selectedFolderID: String,
        appNonce: Data
    ) throws -> Message {
        let selectedDeviceDigest = try DiagnosticsSyncthingBinding.deviceDigest(selectedDeviceID)
        let selectedFolderDigest = try DiagnosticsSyncthingBinding.folderDigest(selectedFolderID)
        guard invitation.type == .qr,
              appNonce.count == 32, appNonce.contains(where: { $0 != 0 }),
              invitation.value.bytes(for: 13, count: 32) == selectedDeviceDigest,
              invitation.value.bytes(for: 14, count: 32) == selectedFolderDigest,
              let secret = invitation.value.bytes(for: 17, count: 32),
              let fields = invitation.value.fields else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var requestFields = fields.compactMap { field -> DiagnosticsCBORField? in
            guard field.label <= 16 else { return nil }
            if field.label == 4 {
                return DiagnosticsCBORField(label: 4, value: .unsigned(MessageType.appRequest.rawValue))
            }
            return field
        }
        let appPublic = appPrivateKey.publicKey.rawRepresentation
        requestFields.append(contentsOf: [
            DiagnosticsCBORField(label: 18, value: .bytes(appPublic)),
            DiagnosticsCBORField(label: 19, value: .bytes(DiagnosticsCrypto.keyID(publicKey: appPublic))),
            DiagnosticsCBORField(label: 20, value: .bytes(appNonce)),
            DiagnosticsCBORField(label: 23, value: .unsigned(1)),
            DiagnosticsCBORField(label: 24, value: .unsigned(invitation.value.unsigned(for: 24)!)),
        ])
        var unsigned = DiagnosticsCBORValue.map(requestFields)
        let hmacBody = try DiagnosticsDeterministicCBOR.encode(unsigned)
        let hmac = DiagnosticsCrypto.hmacSHA256(
            key: secret,
            domain: "eu.vaultsync.helper-pairing/v1/bootstrap-hmac\0",
            body: hmacBody
        )
        requestFields.append(DiagnosticsCBORField(label: 21, value: .bytes(hmac)))
        unsigned = .map(requestFields)
        return try sign(unsigned, as: .appRequest, with: appPrivateKey)
    }

    static func makeBootstrapTransition(
        prior: Message,
        type: MessageType,
        appPrivateKey: Curve25519.Signing.PrivateKey,
        now: Date,
        hardExpiry: UInt64
    ) throws -> Message {
        guard prior.type.rawValue >= MessageType.helperAccept.rawValue,
              prior.type.rawValue <= MessageType.abort.rawValue,
              [.finalize, .receipt, .activate, .abort].contains(type),
              let priorFields = prior.value.fields else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let issued = try unixSeconds(now)
        let expires = min(try checkedAdding(issued, maximumLifetime), hardExpiry)
        guard expires > issued else { throw DiagnosticsProtocolError.expired }
        let copied: Set<UInt64> = [1, 2, 3, 5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 22, 23, 24, 25]
        var fields = priorFields.filter { copied.contains($0.label) }
        fields.append(contentsOf: [
            DiagnosticsCBORField(label: 4, value: .unsigned(type.rawValue)),
            DiagnosticsCBORField(label: 15, value: .unsigned(issued)),
            DiagnosticsCBORField(label: 16, value: .unsigned(expires)),
            DiagnosticsCBORField(label: 26, value: .bytes(try prior.digest())),
        ])
        return try sign(.map(fields), as: type, with: appPrivateKey)
    }

    static func makeLifecycleBase(
        record: DiagnosticsPairingRecord,
        type: MessageType,
        nonce: Data,
        now: Date
    ) throws -> [DiagnosticsCBORField] {
        let issued = try unixSeconds(now)
        guard nonce.count == 32, nonce.contains(where: { $0 != 0 }) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return [
            DiagnosticsCBORField(label: 1, value: .text(capability)),
            DiagnosticsCBORField(label: 2, value: .unsigned(1)),
            DiagnosticsCBORField(label: 3, value: .unsigned(1)),
            DiagnosticsCBORField(label: 4, value: .unsigned(type.rawValue)),
            DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
            DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
            DiagnosticsCBORField(label: 7, value: .bytes(record.appPublicKey)),
            DiagnosticsCBORField(label: 8, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 11, value: .bytes(record.helperPublicKey)),
            DiagnosticsCBORField(label: 12, value: .bytes(record.helperKeyID)),
            DiagnosticsCBORField(label: 15, value: .bytes(record.tlsSPKIPin)),
            DiagnosticsCBORField(label: 17, value: .unsigned(record.appEpoch)),
            DiagnosticsCBORField(label: 19, value: .unsigned(record.helperEpoch)),
            DiagnosticsCBORField(label: 21, value: .unsigned(issued)),
            DiagnosticsCBORField(label: 22, value: .unsigned(try checkedAdding(issued, maximumLifetime))),
            DiagnosticsCBORField(label: 23, value: .bytes(nonce)),
            DiagnosticsCBORField(label: 26, value: .bytes(record.currentCredentialStateDigest)),
        ]
    }

    static func makeAppKeyRotationRequest(
        record: DiagnosticsPairingRecord,
        proposedKey: Curve25519.Signing.PrivateKey,
        currentKey: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> Message {
        let proposedPublic = proposedKey.publicKey.rawRepresentation
        var fields = try makeLifecycleBase(record: record, type: .appKeyRotationRequest, nonce: nonce, now: now)
        fields.append(contentsOf: [
            DiagnosticsCBORField(label: 9, value: .bytes(proposedPublic)),
            DiagnosticsCBORField(label: 10, value: .bytes(DiagnosticsCrypto.keyID(publicKey: proposedPublic))),
            DiagnosticsCBORField(label: 18, value: .unsigned(try checkedAdding(record.appEpoch, 1))),
        ])
        return try sign(.map(fields), as: .appKeyRotationRequest, with: currentKey)
    }

    static func makeRevocationRequest(
        record: DiagnosticsPairingRecord,
        reason: RevocationReason,
        currentKey: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> Message {
        var fields = try makeLifecycleBase(record: record, type: .revocationRequest, nonce: nonce, now: now)
        fields.append(contentsOf: [
            DiagnosticsCBORField(label: 18, value: .unsigned(try checkedAdding(record.appEpoch, 1))),
            DiagnosticsCBORField(label: 25, value: .unsigned(reason.rawValue)),
            DiagnosticsCBORField(label: 27, value: .unsigned(1)),
        ])
        return try sign(.map(fields), as: .revocationRequest, with: currentKey)
    }

    static func makeLifecycleContinuation(
        prior: Message,
        type: MessageType,
        transitionKind: TransitionKind?,
        transitionDigest: Data?,
        signer: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> Message {
        guard nonce.count == 32, nonce.contains(where: { $0 != 0 }),
              let priorFields = prior.value.fields else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let issued = try unixSeconds(now)
        let localExpiry = try checkedAdding(issued, maximumLifetime)
        guard let priorExpiry = prior.value.unsigned(for: 22) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let expires = min(localExpiry, priorExpiry)
        guard expires > issued else { throw DiagnosticsProtocolError.expired }

        var template = DiagnosticsCBORValue.map([
            DiagnosticsCBORField(label: 4, value: .unsigned(type.rawValue)),
        ])
        if type.rawValue >= MessageType.lifecycleFinalize.rawValue {
            guard let transitionKind else { throw DiagnosticsProtocolError.invalidMessage }
            template = .map([
                DiagnosticsCBORField(label: 4, value: .unsigned(type.rawValue)),
                DiagnosticsCBORField(label: 29, value: .unsigned(transitionKind.rawValue)),
            ])
        } else if type == .revocationRecord {
            guard let origin = prior.value.unsigned(for: 27) else { throw DiagnosticsProtocolError.invalidMessage }
            template = .map([
                DiagnosticsCBORField(label: 4, value: .unsigned(type.rawValue)),
                DiagnosticsCBORField(label: 27, value: .unsigned(origin)),
            ])
        }
        let expected = try expectedLabels(type: type, template: template).filter { $0 != 255 }
        var fields: [DiagnosticsCBORField] = []
        for label in expected {
            let value: DiagnosticsCBORValue
            switch label {
            case 4: value = .unsigned(type.rawValue)
            case 21: value = .unsigned(issued)
            case 22: value = .unsigned(expires)
            case 23: value = .bytes(nonce)
            case 24: value = .bytes(try prior.digest())
            case 28:
                guard let transitionDigest, transitionDigest.count == 32 else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                value = .bytes(transitionDigest)
            case 29:
                guard let transitionKind else { throw DiagnosticsProtocolError.invalidMessage }
                value = .unsigned(transitionKind.rawValue)
            default:
                guard let copied = priorFields.first(where: { $0.label == label })?.value else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                value = copied
            }
            fields.append(DiagnosticsCBORField(label: label, value: value))
        }
        return try sign(.map(fields), as: type, with: signer)
    }

    static func validateClock(_ message: Message, now: Date, allowExpiredSkew: Bool = true) throws {
        let issuedLabel: UInt64 = message.type.rawValue <= MessageType.abortAck.rawValue ? 15 : 21
        let expiresLabel: UInt64 = message.type.rawValue <= MessageType.abortAck.rawValue ? 16 : 22
        guard let issued = message.value.unsigned(for: issuedLabel),
              let expires = message.value.unsigned(for: expiresLabel),
              expires > issued,
              expires - issued <= maximumLifetime else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let current = try unixSeconds(now)
        guard issued <= (try checkedAdding(current, maximumClockSkew)) else {
            throw DiagnosticsProtocolError.expired
        }
        let allowance = allowExpiredSkew ? maximumClockSkew : 0
        guard current <= (try checkedAdding(expires, allowance)) else {
            throw DiagnosticsProtocolError.expired
        }
    }

    static func validateBootstrapResponse(
        _ response: Message,
        expectedType: MessageType,
        prior: Message,
        now: Date
    ) throws {
        guard response.type == expectedType else { throw DiagnosticsProtocolError.invalidMessage }
        try validateClock(response, now: now)
        guard let responseExpiry = response.value.unsigned(for: 16),
              let priorExpiry = prior.value.unsigned(for: 16),
              responseExpiry <= priorExpiry else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let exactLabels: [UInt64] = expectedType == .helperAccept
            ? [5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 23, 24]
            : [5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 22, 23, 24, 25]
        for label in exactLabels where response.value.value(for: label) != prior.value.value(for: label) {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let priorBindingLabel: UInt64 = expectedType == .helperAccept ? 22 : 26
        let expectedDigest = try prior.digest()
        guard response.value.bytes(for: priorBindingLabel, count: 32) == expectedDigest else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    static func validateLifecycleMessage(
        _ message: Message,
        expectedType: MessageType,
        record: DiagnosticsPairingRecord,
        prior: Message? = nil,
        now: Date
    ) throws {
        guard message.type == expectedType,
              message.value.bytes(for: 5, count: 32) == record.homeserverBinding,
              message.value.bytes(for: 6, count: 32) == record.folderBinding,
              message.value.bytes(for: 7, count: 32) == record.appPublicKey,
              message.value.bytes(for: 8, count: 32) == record.appKeyID,
              message.value.bytes(for: 11, count: 32) == record.helperPublicKey,
              message.value.bytes(for: 12, count: 32) == record.helperKeyID,
              message.value.bytes(for: 15, count: 32) == record.tlsSPKIPin,
              message.value.unsigned(for: 17) == record.appEpoch,
              message.value.unsigned(for: 19) == record.helperEpoch,
              message.value.bytes(for: 26, count: 32) == record.currentCredentialStateDigest else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        try validateClock(message, now: now)
        if let prior {
            guard message.value.bytes(for: 24, count: 32) == (try prior.digest()),
                  let messageExpiry = message.value.unsigned(for: 22),
                  let priorExpiry = prior.value.unsigned(for: 22),
                  messageExpiry <= priorExpiry else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            for label in [9, 10, 13, 14, 16, 18, 20, 25, 27, 28, 29] {
                let left = message.value.value(for: UInt64(label))
                let right = prior.value.value(for: UInt64(label))
                if left != nil || right != nil {
                    guard left == right else { throw DiagnosticsProtocolError.invalidMessage }
                }
            }
        }
    }

    private static func sign(
        _ value: DiagnosticsCBORValue,
        as type: MessageType,
        with key: Curve25519.Signing.PrivateKey
    ) throws -> Message {
        guard value.unsigned(for: 4) == type.rawValue,
              value.value(for: 255) == nil,
              let domain = domain(for: type) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value)
        var input = Data(domain.utf8)
        input.append(body)
        let signature = try key.signature(for: input)
        guard case .map(var fields) = value else { throw DiagnosticsProtocolError.invalidMessage }
        fields.append(DiagnosticsCBORField(label: 255, value: .bytes(signature)))
        return try decode(try DiagnosticsDeterministicCBOR.encode(.map(fields)))
    }

    private static func verifySignature(_ message: Message) throws {
        guard let domain = domain(for: message.type),
              let signerLabel = signerPublicKeyLabel(for: message),
              let publicBytes = message.value.bytes(for: signerLabel, count: 32),
              let signature = message.value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let publicKey = try Curve25519.Signing.PublicKey(rawRepresentation: publicBytes)
        let body = try DiagnosticsDeterministicCBOR.encode(message.value.removing(labels: [255]))
        var input = Data(domain.utf8)
        input.append(body)
        guard publicKey.isValidSignature(signature, for: input) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    private static func validateSchema(_ message: Message) throws {
        let labels = try expectedLabels(type: message.type, template: message.value)
        guard message.value.text(for: 1) == capability,
              message.value.unsigned(for: 2) == 1,
              message.value.unsigned(for: 3) == 1,
              message.value.unsigned(for: 4) == message.type.rawValue,
              let fields = message.value.fields,
              fields.map(\.label) == labels else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if message.type.rawValue <= MessageType.abortAck.rawValue {
            try validateBootstrapFields(message)
        } else {
            try validateLifecycleFields(message)
        }
    }

    private static func validateBootstrapFields(_ message: Message) throws {
        let uintLabels = Set<UInt64>([2, 3, 4, 15, 16, 24]
            + (message.type.rawValue <= MessageType.appRequest.rawValue ? [7] : [])
            + (message.type == .qr ? [] : [23]))
        guard let fields = message.value.fields else { throw DiagnosticsProtocolError.invalidMessage }
        for field in fields {
            if field.label == 1 { guard case .text = field.value else { throw DiagnosticsProtocolError.invalidMessage }; continue }
            if field.label == 6 { guard case .text = field.value else { throw DiagnosticsProtocolError.invalidMessage }; continue }
            if uintLabels.contains(field.label) { guard case .unsigned = field.value else { throw DiagnosticsProtocolError.invalidMessage }; continue }
            let expectedCount = field.label == 255 ? 64 : 32
            guard case .bytes(let bytes) = field.value, bytes.count == expectedCount,
                  expectedCount != 32 || bytes.contains(where: { $0 != 0 }) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        guard let issued = message.value.unsigned(for: 15),
              let expires = message.value.unsigned(for: 16),
              issued > 0, expires > issued, expires - issued <= maximumLifetime,
              let helperPublic = message.value.bytes(for: 9, count: 32),
              message.value.bytes(for: 10, count: 32) == DiagnosticsCrypto.keyID(publicKey: helperPublic),
              (message.value.unsigned(for: 24) ?? 0) > 0 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if message.type.rawValue <= MessageType.appRequest.rawValue {
            guard let host = message.value.text(for: 6), validEndpointHost(host),
                  let port = message.value.unsigned(for: 7), port > 0, port <= 65_535 else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        if message.type != .qr {
            guard message.value.unsigned(for: 23) == 1,
                  let appPublic = message.value.bytes(for: 18, count: 32),
                  message.value.bytes(for: 19, count: 32) == DiagnosticsCrypto.keyID(publicKey: appPublic) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        var requiredNonzeroByteLabels: [UInt64] = [5, 9, 10, 11, 12, 13, 14]
        if message.type.rawValue <= MessageType.appRequest.rawValue {
            requiredNonzeroByteLabels.append(8)
        }
        for label in requiredNonzeroByteLabels {
            guard message.value.bytes(for: label, count: 32)?.contains(where: { $0 != 0 }) == true else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
    }

    private static func validateLifecycleFields(_ message: Message) throws {
        guard let fields = message.value.fields else { throw DiagnosticsProtocolError.invalidMessage }
        var uintLabels: Set<UInt64> = [2, 3, 4, 17, 19, 21, 22]
        for label in [18, 20, 25, 27, 29] where message.value.value(for: UInt64(label)) != nil {
            uintLabels.insert(UInt64(label))
        }
        for field in fields {
            if field.label == 1 { guard case .text = field.value else { throw DiagnosticsProtocolError.invalidMessage }; continue }
            if uintLabels.contains(field.label) { guard case .unsigned = field.value else { throw DiagnosticsProtocolError.invalidMessage }; continue }
            let count = field.label == 255 ? 64 : 32
            guard case .bytes(let bytes) = field.value, bytes.count == count,
                  count != 32 || bytes.contains(where: { $0 != 0 }) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        guard let issued = message.value.unsigned(for: 21),
              let expires = message.value.unsigned(for: 22),
              issued > 0, expires > issued, expires - issued <= maximumLifetime,
              let appEpoch = message.value.unsigned(for: 17), appEpoch > 0,
              let helperEpoch = message.value.unsigned(for: 19), helperEpoch > 0 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if let proposed = message.value.unsigned(for: 18) {
            guard appEpoch < UInt64.max, proposed == appEpoch + 1 else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        if let proposed = message.value.unsigned(for: 20) {
            guard helperEpoch < UInt64.max, proposed == helperEpoch + 1 else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        if let reason = message.value.unsigned(for: 25), !(1...4).contains(reason) {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if let origin = message.value.unsigned(for: 27), !(1...2).contains(origin) {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if let kind = message.value.unsigned(for: 29), !(1...3).contains(kind) {
            throw DiagnosticsProtocolError.invalidMessage
        }
        for (publicLabel, idLabel) in [(7, 8), (9, 10), (11, 12), (13, 14)] {
            let publicKey = message.value.bytes(for: UInt64(publicLabel), count: 32)
            let keyID = message.value.bytes(for: UInt64(idLabel), count: 32)
            guard (publicKey == nil) == (keyID == nil),
                  publicKey == nil || keyID == DiagnosticsCrypto.keyID(publicKey: publicKey!) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
    }

    private static func expectedLabels(type: MessageType, template: DiagnosticsCBORValue) throws -> [UInt64] {
        switch type {
        case .qr:
            return Array(1...17) + [24]
        case .appRequest:
            return [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 21, 23, 24, 255]
        case .helperAccept:
            return [1, 2, 3, 4, 5, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 22, 23, 24, 25, 255]
        case .finalize, .finalizeAck, .receipt, .readyAck, .activate, .activeAck, .abort, .abortAck:
            return [1, 2, 3, 4, 5, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 22, 23, 24, 25, 26, 255]
        case .appKeyRotationRequest:
            return lifecycleLabels([9, 10, 18])
        case .appKeyRotationNewProof, .appKeyRotationAccept:
            return lifecycleLabels([9, 10, 18, 24])
        case .helperKeyRotationPropose:
            return lifecycleLabels([13, 14, 20])
        case .helperKeyRotationNewProof, .helperKeyRotationConfirm:
            return lifecycleLabels([13, 14, 20, 24])
        case .tlsPinRotationPropose:
            return lifecycleLabels([16])
        case .tlsPinRotationConfirm:
            return lifecycleLabels([16, 24])
        case .revocationRequest:
            return lifecycleLabels([18, 25, 27])
        case .revocationRecord:
            guard let origin = template.unsigned(for: 27) else { throw DiagnosticsProtocolError.invalidMessage }
            return lifecycleLabels(origin == 1 ? [18, 24, 25, 27] : [18, 25, 27])
        case .lifecycleFinalize, .lifecycleActiveAck, .lifecycleAbort, .lifecycleAbortAck:
            guard let rawKind = template.unsigned(for: 29), let kind = TransitionKind(rawValue: rawKind) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            switch kind {
            case .appKey: return lifecycleLabels([9, 10, 18, 24, 28, 29])
            case .helperKey: return lifecycleLabels([13, 14, 20, 24, 28, 29])
            case .tlsPin: return lifecycleLabels([16, 24, 28, 29])
            }
        }
    }

    private static func lifecycleLabels(_ additional: [UInt64]) -> [UInt64] {
        ([1, 2, 3, 4, 5, 6, 7, 8, 11, 12, 15, 17, 19, 21, 22, 23, 26, 255] + additional).sorted()
    }

    private static func signerPublicKeyLabel(for message: Message) -> UInt64? {
        switch message.type {
        case .appRequest, .finalize, .receipt, .activate, .abort: return 18
        case .helperAccept, .finalizeAck, .readyAck, .activeAck, .abortAck: return 9
        case .appKeyRotationRequest, .helperKeyRotationConfirm, .tlsPinRotationConfirm, .revocationRequest, .lifecycleAbort: return 7
        case .appKeyRotationNewProof: return 9
        case .appKeyRotationAccept, .helperKeyRotationPropose, .tlsPinRotationPropose, .revocationRecord, .lifecycleAbortAck: return 11
        case .helperKeyRotationNewProof: return 13
        case .lifecycleFinalize:
            return message.value.unsigned(for: 29) == TransitionKind.appKey.rawValue ? 9 : 7
        case .lifecycleActiveAck:
            return message.value.unsigned(for: 29) == TransitionKind.helperKey.rawValue ? 13 : 11
        case .qr: return nil
        }
    }

    private static func domain(for type: MessageType) -> String? {
        let suffix: String
        switch type {
        case .qr: return nil
        case .appRequest: suffix = "app-request"
        case .helperAccept: suffix = "helper-accept"
        case .finalize: suffix = "pairing-finalize"
        case .finalizeAck: suffix = "pairing-finalize-ack"
        case .receipt: suffix = "pairing-receipt"
        case .readyAck: suffix = "pairing-ready-ack"
        case .activate: suffix = "pairing-activate"
        case .activeAck: suffix = "pairing-active-ack"
        case .abort: suffix = "pairing-abort"
        case .abortAck: suffix = "pairing-abort-ack"
        case .appKeyRotationRequest: suffix = "app-key-rotation-request"
        case .appKeyRotationNewProof: suffix = "app-key-rotation-new-proof"
        case .appKeyRotationAccept: suffix = "app-key-rotation-accept"
        case .helperKeyRotationPropose: suffix = "helper-key-rotation-propose"
        case .helperKeyRotationNewProof: suffix = "helper-key-rotation-new-proof"
        case .helperKeyRotationConfirm: suffix = "helper-key-rotation-confirm"
        case .tlsPinRotationPropose: suffix = "tls-pin-rotation-propose"
        case .tlsPinRotationConfirm: suffix = "tls-pin-rotation-confirm"
        case .revocationRequest: suffix = "revocation-request"
        case .revocationRecord: suffix = "revocation-record"
        case .lifecycleFinalize: suffix = "lifecycle-finalize"
        case .lifecycleActiveAck: suffix = "lifecycle-active-ack"
        case .lifecycleAbort: suffix = "lifecycle-abort"
        case .lifecycleAbortAck: suffix = "lifecycle-abort-ack"
        }
        return "eu.vaultsync.helper-pairing/v1/\(suffix)\0"
    }

    static func validEndpointHost(_ host: String) -> Bool {
        guard !host.isEmpty, host.utf8.count <= 253, host == host.lowercased(),
              host.utf8.allSatisfy({ $0 >= 0x21 && $0 <= 0x7e }),
              !host.hasPrefix("."), !host.hasSuffix("."),
              !host.contains(":"),
              !host.utf8.allSatisfy({ ($0 >= 0x30 && $0 <= 0x39) || $0 == 0x2e }) else {
            // IPv6 and canonical IPv4 literals are validated by URLComponents
            // in the transport; this branch accepts only canonical DNS names.
            return DiagnosticsPinnedTransport.isCanonicalIPAddress(host)
        }
        return host.split(separator: ".", omittingEmptySubsequences: false).allSatisfy { label in
            !label.isEmpty && label.utf8.count <= 63 && !label.hasPrefix("-") && !label.hasSuffix("-") &&
                label.utf8.allSatisfy {
                    ($0 >= 0x61 && $0 <= 0x7a) || ($0 >= 0x30 && $0 <= 0x39) || $0 == 0x2d
                }
        }
    }

    private static func unixSeconds(_ date: Date) throws -> UInt64 {
        let seconds = date.timeIntervalSince1970.rounded(.down)
        guard seconds >= 0, seconds < Double(UInt64.max) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return UInt64(seconds)
    }

    static func checkedAdding(_ value: UInt64, _ increment: UInt64) throws -> UInt64 {
        let result = value.addingReportingOverflow(increment)
        guard !result.overflow else { throw DiagnosticsProtocolError.invalidMessage }
        return result.partialValue
    }
}

import CryptoKit
import Foundation

enum DiagnosticsCapabilityProtocol {
    static let capability = "eu.vaultsync.diagnostics.correlated-roundtrip/1"
    static let path = "/api/v1/diagnostics/capability"
    static let requiredFlags: UInt64 = 0x0f
    private static let queryDomain = "eu.vaultsync.roundtrip/v1/capability-query\0"
    private static let responseDomain = "eu.vaultsync.roundtrip/v1/capability-response\0"

    struct Query: Sendable {
        let message: Data
        let digest: Data
        let nonce: Data
    }

    static func makeQuery(
        record: DiagnosticsPairingRecord,
        appKey: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> Query {
        guard nonce.count == 32,
              appKey.publicKey.rawRepresentation == record.appPublicKey else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let issued = try unixSeconds(now)
        let body = DiagnosticsCBORValue.map([
            DiagnosticsCBORField(label: 1, value: .text(capability)),
            DiagnosticsCBORField(label: 2, value: .unsigned(1)),
            DiagnosticsCBORField(label: 3, value: .unsigned(1)),
            DiagnosticsCBORField(label: 4, value: .unsigned(1)),
            DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
            DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
            DiagnosticsCBORField(label: 7, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 8, value: .bytes(record.helperKeyID)),
            DiagnosticsCBORField(label: 9, value: .unsigned(record.appEpoch)),
            DiagnosticsCBORField(label: 10, value: .unsigned(record.helperEpoch)),
            DiagnosticsCBORField(label: 12, value: .unsigned(issued)),
            DiagnosticsCBORField(
                label: 13,
                value: .unsigned(try DiagnosticsPairingProtocol.checkedAdding(issued, 120))
            ),
            DiagnosticsCBORField(label: 30, value: .bytes(nonce)),
        ])
        try validate(body, type: 1, record: record, omittedSignature: true)
        let bodyBytes = try DiagnosticsDeterministicCBOR.encode(body)
        var signedInput = Data(queryDomain.utf8)
        signedInput.append(bodyBytes)
        let signature = try appKey.signature(for: signedInput)
        guard case .map(var fields) = body else { throw DiagnosticsProtocolError.invalidMessage }
        fields.append(DiagnosticsCBORField(label: 255, value: .bytes(signature)))
        let message = try DiagnosticsDeterministicCBOR.encode(.map(fields))
        return Query(
            message: message,
            digest: DiagnosticsCrypto.sha256(domain: queryDomain, body: bodyBytes),
            nonce: nonce
        )
    }

    @discardableResult
    static func validateResponse(
        _ data: Data,
        query: Query,
        record: DiagnosticsPairingRecord,
        now: Date
    ) throws -> UInt64 {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        try validate(value, type: 2, record: record, omittedSignature: false)
        guard value.bytes(for: 30, count: 32) == query.nonce,
              value.bytes(for: 31, count: 32) == query.digest,
              value.unsigned(for: 27) == requiredFlags,
              let signature = value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        var signedInput = Data(responseDomain.utf8)
        signedInput.append(body)
        let helperKey = try Curve25519.Signing.PublicKey(rawRepresentation: record.helperPublicKey)
        guard helperKey.isValidSignature(signature, for: signedInput) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let current = try unixSeconds(now)
        guard let issued = value.unsigned(for: 12), let expires = value.unsigned(for: 13),
              issued <= (try DiagnosticsPairingProtocol.checkedAdding(current, 120)),
              current <= (try DiagnosticsPairingProtocol.checkedAdding(expires, 120)) else {
            throw DiagnosticsProtocolError.expired
        }
        return expires
    }

    private static func validate(
        _ value: DiagnosticsCBORValue,
        type: UInt64,
        record: DiagnosticsPairingRecord,
        omittedSignature: Bool
    ) throws {
        let labels: [UInt64] = type == 1
            ? [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 30] + (omittedSignature ? [] : [255])
            : [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 27, 30, 31, 255]
        guard value.fields?.map(\.label) == labels.sorted(),
              value.text(for: 1) == capability,
              value.unsigned(for: 2) == 1,
              value.unsigned(for: 3) == 1,
              value.unsigned(for: 4) == type,
              value.bytes(for: 5, count: 32) == record.homeserverBinding,
              value.bytes(for: 6, count: 32) == record.folderBinding,
              value.bytes(for: 7, count: 32) == record.appKeyID,
              value.bytes(for: 8, count: 32) == record.helperKeyID,
              value.unsigned(for: 9) == record.appEpoch,
              value.unsigned(for: 10) == record.helperEpoch,
              value.bytes(for: 30, count: 32)?.contains(where: { $0 != 0 }) == true,
              let issued = value.unsigned(for: 12), issued > 0,
              let expires = value.unsigned(for: 13), expires > issued, expires - issued <= 120 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    private static func unixSeconds(_ date: Date) throws -> UInt64 {
        let value = date.timeIntervalSince1970.rounded(.down)
        guard value >= 0, value < Double(UInt64.max) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return UInt64(value)
    }
}

enum DiagnosticsNamespaceProtocol {
    static let capability = "eu.vaultsync.diagnostics.namespace/1"
    static let enablementPath = "/api/v1/diagnostics/namespace/enablement"
    static let authorizationPath = "/api/v1/diagnostics/namespace/authorization"
    static let rootName = "VaultSync Diagnostics"
    static let rootManifestName = "root-manifest.cbor"
    private static let recordDigestDomain = "eu.vaultsync.namespace/v1/record-digest\0"
    private static let installationBindingDomain = "eu.vaultsync.namespace/installation/v1\0"
    private static let enablementDomain = "eu.vaultsync.namespace/v1/enablement-request\0"
    private static let rootDomain = "eu.vaultsync.namespace/v1/root-manifest\0"
    private static let authorizationAppDomain = "eu.vaultsync.namespace/v1/authorization-initial-app\0"
    private static let authorizationHelperDomain = "eu.vaultsync.namespace/v1/authorization-initial-helper\0"
    private static let authorizationEpochAppDomain = "eu.vaultsync.namespace/v1/authorization-epoch-app\0"
    private static let authorizationEpochHelperDomain = "eu.vaultsync.namespace/v1/authorization-epoch-helper\0"
    private static let helperEpochPriorDomain = "eu.vaultsync.namespace/v1/helper-epoch-prior\0"
    private static let helperEpochCurrentDomain = "eu.vaultsync.namespace/v1/helper-epoch-current\0"
    private static let readme = """
    VaultSync Diagnostics

    EN: App-owned diagnostics infrastructure. It contains only opaque protocol
    data. It is visible in file browsers, synchronized peers, backups, versions,
    conflict copies, and deletion tombstones. Do not store notes here.

    DE: App-eigene Diagnose-Infrastruktur. Sie enthaelt nur undurchsichtige
    Protokolldaten. Sie ist in Dateibrowsern, auf synchronisierten Geraeten, in
    Backups, Versionen, Konfliktkopien und Loesch-Tombstones sichtbar. Keine
    Notizen hier speichern.

    ES: Infraestructura de diagnostico propiedad de la aplicacion. Solo contiene
    datos opacos del protocolo. Es visible en exploradores de archivos, pares
    sincronizados, copias de seguridad, versiones, copias en conflicto y registros
    de eliminacion. No guardes notas aqui.

    ZH-HANS: VaultSync 诊断基础设施，仅包含不透明的协议数据。它会显示在文件浏览器、
    同步设备、备份、版本、冲突副本和删除记录中。请勿在此存储笔记。

    """

    struct RootManifest: Equatable, Sendable {
        let message: Data
        let namespaceID: Data
        let rootDigest: Data
        let manifestDigest: Data
    }

    struct AuthorizationCandidate: Equatable, Sendable {
        let message: Data
        let installationBinding: Data
    }

    static func makeEnablement(
        record: DiagnosticsPairingRecord,
        appKey: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> Data {
        guard nonce.count == 32,
              appKey.publicKey.rawRepresentation == record.appPublicKey else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let issued = try unixSeconds(now)
        let body = DiagnosticsCBORValue.map([
            DiagnosticsCBORField(label: 1, value: .text(capability)),
            DiagnosticsCBORField(label: 2, value: .unsigned(1)),
            DiagnosticsCBORField(label: 3, value: .unsigned(1)),
            DiagnosticsCBORField(label: 4, value: .unsigned(1)),
            DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
            DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
            DiagnosticsCBORField(label: 9, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 10, value: .bytes(record.appPublicKey)),
            DiagnosticsCBORField(label: 11, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 12, value: .unsigned(record.appEpoch)),
            DiagnosticsCBORField(label: 13, value: .bytes(record.helperPublicKey)),
            DiagnosticsCBORField(label: 14, value: .bytes(record.helperKeyID)),
            DiagnosticsCBORField(label: 15, value: .unsigned(record.helperEpoch)),
            DiagnosticsCBORField(label: 19, value: .bytes(nonce)),
            DiagnosticsCBORField(label: 26, value: .unsigned(issued)),
            DiagnosticsCBORField(
                label: 27,
                value: .unsigned(try DiagnosticsPairingProtocol.checkedAdding(issued, 300))
            ),
        ])
        let bodyBytes = try DiagnosticsDeterministicCBOR.encode(body)
        var input = Data(enablementDomain.utf8)
        input.append(bodyBytes)
        let signature = try appKey.signature(for: input)
        guard case .map(var fields) = body else { throw DiagnosticsProtocolError.invalidMessage }
        fields.append(DiagnosticsCBORField(label: 253, value: .bytes(signature)))
        let encoded = try DiagnosticsDeterministicCBOR.encode(.map(fields))
        try validateEnablement(encoded, record: record)
        return encoded
    }

    static func validateRootManifest(
        _ data: Data,
        enablement: Data,
        record: DiagnosticsPairingRecord
    ) throws -> RootManifest {
        try validateEnablement(enablement, record: record)
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        let expected: [UInt64] = [1, 2, 3, 4, 5, 6, 7, 13, 14, 15, 19, 20, 28, 29, 255]
        guard value.fields?.map(\.label) == expected,
              value.text(for: 1) == capability,
              value.unsigned(for: 2) == 1,
              value.unsigned(for: 3) == 1,
              value.unsigned(for: 4) == 2,
              value.bytes(for: 5, count: 32) == record.homeserverBinding,
              value.bytes(for: 6, count: 32) == record.folderBinding,
              let namespaceID = value.bytes(for: 7, count: 32), namespaceID.contains(where: { $0 != 0 }),
              value.bytes(for: 13, count: 32) == record.helperPublicKey,
              value.bytes(for: 14, count: 32) == record.helperKeyID,
              value.unsigned(for: 15) == record.helperEpoch,
              let enablementValue = try? DiagnosticsDeterministicCBOR.decode(enablement),
              value.bytes(for: 19, count: 32) == enablementValue.bytes(for: 19, count: 32),
              value.bytes(for: 20, count: 32) == recordDigest(enablement),
              let enablementIssued = enablementValue.unsigned(for: 26),
              let enablementExpires = enablementValue.unsigned(for: 27),
              let createdAt = value.unsigned(for: 28),
              createdAt >= enablementIssued, createdAt <= enablementExpires,
              value.bytes(for: 29, count: 32) == DiagnosticsCrypto.sha256(Data(readme.utf8)),
              let signature = value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        var input = Data(rootDomain.utf8)
        input.append(body)
        let key = try Curve25519.Signing.PublicKey(rawRepresentation: record.helperPublicKey)
        guard key.isValidSignature(signature, for: input) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let digest = recordDigest(data)
        return RootManifest(message: data, namespaceID: namespaceID, rootDigest: digest, manifestDigest: digest)
    }

    static func validateHelperEpochManifest(
        _ data: Data,
        rootData: Data,
        priorManifestData: Data,
        record: DiagnosticsPairingRecord
    ) throws -> Data {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        let root = try DiagnosticsDeterministicCBOR.decode(rootData)
        let prior = try DiagnosticsDeterministicCBOR.decode(priorManifestData)
        guard let currentHelperEpoch = value.unsigned(for: 15),
              let priorHelperEpoch = value.unsigned(for: 18),
              priorHelperEpoch < UInt64.max else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let expected: [UInt64] = [1, 2, 3, 4, 5, 6, 7, 13, 14, 15, 16, 17, 18, 21, 22, 28, 29, 254, 255]
        guard value.fields?.map(\.label) == expected,
              value.text(for: 1) == capability,
              value.unsigned(for: 2) == 1,
              value.unsigned(for: 3) == 1,
              value.unsigned(for: 4) == 3,
              value.bytes(for: 5, count: 32) == root.bytes(for: 5, count: 32),
              value.bytes(for: 6, count: 32) == root.bytes(for: 6, count: 32),
              value.bytes(for: 7, count: 32) == root.bytes(for: 7, count: 32),
              value.bytes(for: 13, count: 32) == record.helperPublicKey,
              value.bytes(for: 14, count: 32) == record.helperKeyID,
              currentHelperEpoch == record.helperEpoch,
              value.bytes(for: 16, count: 32) == prior.bytes(for: 13, count: 32),
              value.bytes(for: 17, count: 32) == prior.bytes(for: 14, count: 32),
              priorHelperEpoch == prior.unsigned(for: 15),
              currentHelperEpoch == priorHelperEpoch + 1,
              value.bytes(for: 21, count: 32) == recordDigest(rootData),
              value.bytes(for: 22, count: 32) == recordDigest(priorManifestData),
              (value.unsigned(for: 28) ?? 0) > 0,
              value.bytes(for: 29, count: 32) == DiagnosticsCrypto.sha256(Data(readme.utf8)),
              let priorPublic = value.bytes(for: 16, count: 32),
              let priorSignature = value.bytes(for: 254, count: 64),
              let currentSignature = value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let priorBody = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [254, 255]))
        var priorInput = Data(helperEpochPriorDomain.utf8)
        priorInput.append(priorBody)
        let priorKey = try Curve25519.Signing.PublicKey(rawRepresentation: priorPublic)
        guard priorKey.isValidSignature(priorSignature, for: priorInput) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let currentBody = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        var currentInput = Data(helperEpochCurrentDomain.utf8)
        currentInput.append(currentBody)
        let currentKey = try Curve25519.Signing.PublicKey(rawRepresentation: record.helperPublicKey)
        guard currentKey.isValidSignature(currentSignature, for: currentInput) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return recordDigest(data)
    }

    static func makeInitialAuthorization(
        record: DiagnosticsPairingRecord,
        root: RootManifest,
        appKey: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> AuthorizationCandidate {
        guard nonce.count == 32, nonce.contains(where: { $0 != 0 }),
              appKey.publicKey.rawRepresentation == record.appPublicKey else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let installation = installationBinding(
            initialAppKeyID: record.appKeyID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding
        )
        let issued = try unixSeconds(now)
        let body = DiagnosticsCBORValue.map([
            DiagnosticsCBORField(label: 1, value: .text(capability)),
            DiagnosticsCBORField(label: 2, value: .unsigned(1)),
            DiagnosticsCBORField(label: 3, value: .unsigned(1)),
            DiagnosticsCBORField(label: 4, value: .unsigned(4)),
            DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
            DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
            DiagnosticsCBORField(label: 7, value: .bytes(root.namespaceID)),
            DiagnosticsCBORField(label: 8, value: .bytes(installation)),
            DiagnosticsCBORField(label: 9, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 10, value: .bytes(record.appPublicKey)),
            DiagnosticsCBORField(label: 11, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 12, value: .unsigned(record.appEpoch)),
            DiagnosticsCBORField(label: 13, value: .bytes(record.helperPublicKey)),
            DiagnosticsCBORField(label: 14, value: .bytes(record.helperKeyID)),
            DiagnosticsCBORField(label: 15, value: .unsigned(record.helperEpoch)),
            DiagnosticsCBORField(label: 21, value: .bytes(root.rootDigest)),
            DiagnosticsCBORField(label: 23, value: .bytes(root.manifestDigest)),
            DiagnosticsCBORField(label: 25, value: .bytes(record.currentCredentialStateDigest)),
            DiagnosticsCBORField(label: 26, value: .unsigned(issued)),
            DiagnosticsCBORField(
                label: 27,
                value: .unsigned(try DiagnosticsPairingProtocol.checkedAdding(issued, 300))
            ),
            DiagnosticsCBORField(label: 30, value: .bytes(nonce)),
            DiagnosticsCBORField(label: 31, value: .unsigned(1)),
        ])
        let bodyBytes = try DiagnosticsDeterministicCBOR.encode(body)
        var input = Data(authorizationAppDomain.utf8)
        input.append(bodyBytes)
        let signature = try appKey.signature(for: input)
        guard case .map(var fields) = body else { throw DiagnosticsProtocolError.invalidMessage }
        fields.append(DiagnosticsCBORField(label: 253, value: .bytes(signature)))
        let encoded = try DiagnosticsDeterministicCBOR.encode(.map(fields))
        return AuthorizationCandidate(message: encoded, installationBinding: installation)
    }

    static func makeAuthorizationEpoch(
        record: DiagnosticsPairingRecord,
        root: RootManifest,
        priorAuthorizationDigest: Data,
        appKey: Curve25519.Signing.PrivateKey,
        nonce: Data,
        now: Date
    ) throws -> AuthorizationCandidate {
        guard nonce.count == 32, nonce.contains(where: { $0 != 0 }),
              appKey.publicKey.rawRepresentation == record.appPublicKey,
              let initialAppKeyID = record.namespaceInitialAppKeyID,
              initialAppKeyID.count == 32,
              priorAuthorizationDigest.count == 32,
              record.namespaceAuthorizationEpoch > 0,
              record.namespaceAuthorizationEpoch < 9 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let installation = installationBinding(
            initialAppKeyID: initialAppKeyID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding
        )
        let issued = try unixSeconds(now)
        let epoch = try DiagnosticsPairingProtocol.checkedAdding(record.namespaceAuthorizationEpoch, 1)
        let body = DiagnosticsCBORValue.map([
            DiagnosticsCBORField(label: 1, value: .text(capability)),
            DiagnosticsCBORField(label: 2, value: .unsigned(1)),
            DiagnosticsCBORField(label: 3, value: .unsigned(1)),
            DiagnosticsCBORField(label: 4, value: .unsigned(5)),
            DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
            DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
            DiagnosticsCBORField(label: 7, value: .bytes(root.namespaceID)),
            DiagnosticsCBORField(label: 8, value: .bytes(installation)),
            DiagnosticsCBORField(label: 9, value: .bytes(initialAppKeyID)),
            DiagnosticsCBORField(label: 10, value: .bytes(record.appPublicKey)),
            DiagnosticsCBORField(label: 11, value: .bytes(record.appKeyID)),
            DiagnosticsCBORField(label: 12, value: .unsigned(record.appEpoch)),
            DiagnosticsCBORField(label: 13, value: .bytes(record.helperPublicKey)),
            DiagnosticsCBORField(label: 14, value: .bytes(record.helperKeyID)),
            DiagnosticsCBORField(label: 15, value: .unsigned(record.helperEpoch)),
            DiagnosticsCBORField(label: 21, value: .bytes(root.rootDigest)),
            DiagnosticsCBORField(label: 23, value: .bytes(root.manifestDigest)),
            DiagnosticsCBORField(label: 24, value: .bytes(priorAuthorizationDigest)),
            DiagnosticsCBORField(label: 25, value: .bytes(record.currentCredentialStateDigest)),
            DiagnosticsCBORField(label: 26, value: .unsigned(issued)),
            DiagnosticsCBORField(
                label: 27,
                value: .unsigned(try DiagnosticsPairingProtocol.checkedAdding(issued, 300))
            ),
            DiagnosticsCBORField(label: 30, value: .bytes(nonce)),
            DiagnosticsCBORField(label: 31, value: .unsigned(epoch)),
        ])
        let bodyBytes = try DiagnosticsDeterministicCBOR.encode(body)
        var input = Data(authorizationEpochAppDomain.utf8)
        input.append(bodyBytes)
        let signature = try appKey.signature(for: input)
        guard case .map(var fields) = body else { throw DiagnosticsProtocolError.invalidMessage }
        fields.append(DiagnosticsCBORField(label: 253, value: .bytes(signature)))
        return AuthorizationCandidate(
            message: try DiagnosticsDeterministicCBOR.encode(.map(fields)),
            installationBinding: installation
        )
    }

    static func validateCompletedAuthorization(
        _ data: Data,
        candidate: AuthorizationCandidate,
        record: DiagnosticsPairingRecord,
        root: RootManifest
    ) throws -> Data {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        let candidateValue = try DiagnosticsDeterministicCBOR.decode(candidate.message)
        let expected: [UInt64] = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21, 23, 25, 26, 27, 30, 31, 253, 255]
        guard value.fields?.map(\.label) == expected,
              value.removing(labels: [255]) == candidateValue,
              value.bytes(for: 7, count: 32) == root.namespaceID,
              value.bytes(for: 8, count: 32) == candidate.installationBinding,
              value.bytes(for: 13, count: 32) == record.helperPublicKey,
              let signature = value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        try validateAuthorizationCandidate(
            candidate.message,
            type: 4,
            record: record,
            root: root,
            priorAuthorizationDigest: nil
        )
        let helperBody = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        var helperInput = Data(authorizationHelperDomain.utf8)
        helperInput.append(helperBody)
        let helperKey = try Curve25519.Signing.PublicKey(rawRepresentation: record.helperPublicKey)
        guard helperKey.isValidSignature(signature, for: helperInput) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return recordDigest(data)
    }

    static func validateCompletedAuthorizationEpoch(
        _ data: Data,
        candidate: AuthorizationCandidate,
        record: DiagnosticsPairingRecord,
        root: RootManifest
    ) throws -> Data {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        let candidateValue = try DiagnosticsDeterministicCBOR.decode(candidate.message)
        let expected: [UInt64] = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21, 23, 24, 25, 26, 27, 30, 31, 253, 255]
        guard value.fields?.map(\.label) == expected,
              value.removing(labels: [255]) == candidateValue,
              value.unsigned(for: 4) == 5,
              value.bytes(for: 7, count: 32) == root.namespaceID,
              value.bytes(for: 8, count: 32) == candidate.installationBinding,
              value.bytes(for: 13, count: 32) == record.helperPublicKey,
              let signature = value.bytes(for: 255, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        try validateAuthorizationCandidate(
            candidate.message,
            type: 5,
            record: record,
            root: root,
            priorAuthorizationDigest: record.namespaceAuthorizationDigest
        )
        let helperBody = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        var helperInput = Data(authorizationEpochHelperDomain.utf8)
        helperInput.append(helperBody)
        let helperKey = try Curve25519.Signing.PublicKey(rawRepresentation: record.helperPublicKey)
        guard helperKey.isValidSignature(signature, for: helperInput) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return recordDigest(data)
    }

    static func authorizationRelativePath(installationBinding: Data) throws -> String {
        guard installationBinding.count == 32 else { throw DiagnosticsProtocolError.invalidMessage }
        return "installations/\(base32LowerNoPadding(installationBinding))/authorization.cbor"
    }

    static func authorizationEpochRelativePath(installationBinding: Data, epoch: UInt64) throws -> String {
        guard installationBinding.count == 32, epoch >= 2, epoch <= 9 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return "installations/\(base32LowerNoPadding(installationBinding))/authorization-epochs/\(epoch).authorization.cbor"
    }

    static func recordDigest(_ data: Data) -> Data {
        DiagnosticsCrypto.sha256(domain: recordDigestDomain, body: data)
    }

    private static func validateEnablement(_ data: Data, record: DiagnosticsPairingRecord) throws {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        let expected: [UInt64] = [1, 2, 3, 4, 5, 6, 9, 10, 11, 12, 13, 14, 15, 19, 26, 27, 253]
        guard value.fields?.map(\.label) == expected,
              value.text(for: 1) == capability,
              value.unsigned(for: 2) == 1,
              value.unsigned(for: 3) == 1,
              value.unsigned(for: 4) == 1,
              value.bytes(for: 5, count: 32) == record.homeserverBinding,
              value.bytes(for: 6, count: 32) == record.folderBinding,
              value.bytes(for: 9, count: 32) == record.appKeyID,
              value.bytes(for: 10, count: 32) == record.appPublicKey,
              value.bytes(for: 11, count: 32) == record.appKeyID,
              value.unsigned(for: 12) == record.appEpoch,
              value.bytes(for: 13, count: 32) == record.helperPublicKey,
              value.bytes(for: 14, count: 32) == record.helperKeyID,
              value.unsigned(for: 15) == record.helperEpoch,
              value.bytes(for: 19, count: 32)?.contains(where: { $0 != 0 }) == true,
              let issued = value.unsigned(for: 26), issued > 0,
              let expires = value.unsigned(for: 27),
              expires > issued, expires - issued <= 300,
              let signature = value.bytes(for: 253, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [253]))
        var input = Data(enablementDomain.utf8)
        input.append(body)
        let appKey = try Curve25519.Signing.PublicKey(rawRepresentation: record.appPublicKey)
        guard appKey.isValidSignature(signature, for: input) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    private static func validateAuthorizationCandidate(
        _ data: Data,
        type: UInt64,
        record: DiagnosticsPairingRecord,
        root: RootManifest,
        priorAuthorizationDigest: Data?
    ) throws {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        let expected: [UInt64] = type == 4
            ? [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21, 23, 25, 26, 27, 30, 31, 253]
            : [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21, 23, 24, 25, 26, 27, 30, 31, 253]
        let initialAppKeyID: Data
        let expectedEpoch: UInt64
        if type == 4 {
            initialAppKeyID = record.appKeyID
            expectedEpoch = 1
        } else {
            guard type == 5,
                  let storedInitial = record.namespaceInitialAppKeyID,
                  let priorAuthorizationDigest,
                  priorAuthorizationDigest.count == 32,
                  value.bytes(for: 24, count: 32) == priorAuthorizationDigest else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            initialAppKeyID = storedInitial
            expectedEpoch = try DiagnosticsPairingProtocol.checkedAdding(
                record.namespaceAuthorizationEpoch,
                1
            )
        }
        let installation = installationBinding(
            initialAppKeyID: initialAppKeyID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding
        )
        guard value.fields?.map(\.label) == expected,
              value.text(for: 1) == capability,
              value.unsigned(for: 2) == 1,
              value.unsigned(for: 3) == 1,
              value.unsigned(for: 4) == type,
              value.bytes(for: 5, count: 32) == record.homeserverBinding,
              value.bytes(for: 6, count: 32) == record.folderBinding,
              value.bytes(for: 7, count: 32) == root.namespaceID,
              value.bytes(for: 8, count: 32) == installation,
              value.bytes(for: 9, count: 32) == initialAppKeyID,
              value.bytes(for: 10, count: 32) == record.appPublicKey,
              value.bytes(for: 11, count: 32) == record.appKeyID,
              value.unsigned(for: 12) == record.appEpoch,
              value.bytes(for: 13, count: 32) == record.helperPublicKey,
              value.bytes(for: 14, count: 32) == record.helperKeyID,
              value.unsigned(for: 15) == record.helperEpoch,
              value.bytes(for: 21, count: 32) == root.rootDigest,
              value.bytes(for: 23, count: 32) == root.manifestDigest,
              value.bytes(for: 25, count: 32) == record.currentCredentialStateDigest,
              let issued = value.unsigned(for: 26), issued > 0,
              let expires = value.unsigned(for: 27),
              expires > issued, expires - issued <= 300,
              value.bytes(for: 30, count: 32)?.contains(where: { $0 != 0 }) == true,
              value.unsigned(for: 31) == expectedEpoch,
              let signature = value.bytes(for: 253, count: 64) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let body = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [253]))
        let domain = type == 4 ? authorizationAppDomain : authorizationEpochAppDomain
        var input = Data(domain.utf8)
        input.append(body)
        let key = try Curve25519.Signing.PublicKey(rawRepresentation: record.appPublicKey)
        guard key.isValidSignature(signature, for: input) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    static func installationBinding(
        initialAppKeyID: Data,
        homeserverBinding: Data,
        folderBinding: Data
    ) -> Data {
        var body = initialAppKeyID
        body.append(homeserverBinding)
        body.append(folderBinding)
        return DiagnosticsCrypto.sha256(domain: installationBindingDomain, body: body)
    }

    static func base32LowerNoPadding(_ data: Data) -> String {
        let alphabet = Array("abcdefghijklmnopqrstuvwxyz234567".utf8)
        var result: [UInt8] = []
        var buffer: UInt64 = 0
        var bits = 0
        for byte in data {
            buffer = (buffer << 8) | UInt64(byte)
            bits += 8
            while bits >= 5 {
                bits -= 5
                result.append(alphabet[Int((buffer >> UInt64(bits)) & 0x1f)])
                buffer &= bits == 0 ? 0 : (1 << UInt64(bits)) - 1
            }
        }
        if bits > 0 {
            result.append(alphabet[Int((buffer << UInt64(5 - bits)) & 0x1f)])
        }
        return String(decoding: result, as: UTF8.self)
    }

    static func operationRequestComponents(
        installationBinding: Data,
        operationID: Data
    ) throws -> [String] {
        guard installationBinding.count == 32,
              operationID.count == 32,
              installationBinding.contains(where: { $0 != 0 }),
              operationID.contains(where: { $0 != 0 }) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return [
            rootName,
            "installations",
            base32LowerNoPadding(installationBinding),
            "operations",
            base32LowerNoPadding(operationID) + ".request.cbor",
        ]
    }

    static func operationResponseComponents(
        installationBinding: Data,
        operationID: Data
    ) throws -> [String] {
        var components = try operationRequestComponents(
            installationBinding: installationBinding,
            operationID: operationID
        )
        components[components.count - 1] = base32LowerNoPadding(operationID) + ".response.cbor"
        return components
    }

    private static func unixSeconds(_ date: Date) throws -> UInt64 {
        let value = date.timeIntervalSince1970.rounded(.down)
        guard value >= 0, value < Double(UInt64.max) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return UInt64(value)
    }
}

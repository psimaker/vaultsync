import CryptoKit
import Foundation
import Security
import os

private let diagnosticsCredentialLogger = Logger(
    subsystem: "eu.vaultsync.app",
    category: "diagnostics-credentials"
)

protocol DiagnosticsKeychainAccess {
    func copyMatching(_ query: CFDictionary, result: inout AnyObject?) -> OSStatus
    func update(_ query: CFDictionary, attributes: CFDictionary) -> OSStatus
    func add(_ attributes: CFDictionary) -> OSStatus
    func delete(_ query: CFDictionary) -> OSStatus
}

struct SystemDiagnosticsKeychainAccess: DiagnosticsKeychainAccess {
    func copyMatching(_ query: CFDictionary, result: inout AnyObject?) -> OSStatus {
        SecItemCopyMatching(query, &result)
    }

    func update(_ query: CFDictionary, attributes: CFDictionary) -> OSStatus {
        SecItemUpdate(query, attributes)
    }

    func add(_ attributes: CFDictionary) -> OSStatus {
        SecItemAdd(attributes, nil)
    }

    func delete(_ query: CFDictionary) -> OSStatus {
        SecItemDelete(query)
    }
}

struct DiagnosticsPairingRecord: Codable, Equatable, Identifiable, Sendable {
    enum State: String, Codable, Hashable, Sendable {
        case requestPrepared
        case acceptanceReceived
        case finalizePrepared
        case finalizeAcknowledged
        case receiptPrepared
        case readyAcknowledged
        case activatePrepared
        case abortPrepared
        case active
        case namespaceEnablementPrepared
        case namespaceAwaitingOperator
        case namespaceAuthorizationPrepared
        case namespaceActive
        case namespaceAuthorizationRefreshRequired
        case namespaceAuthorizationRefreshPrepared
        case lifecyclePending
        case revocationPrepared
        case revoked
    }

    struct PendingLifecycle: Codable, Equatable, Sendable {
        let kind: DiagnosticsPairingProtocol.TransitionKind
        let transitionDigest: Data
        var latestMessage: Data
        var proposedAppSeed: Data?
        var proposedHelperPublicKey: Data?
        var proposedHelperEpoch: UInt64?
        var proposedTLSSPKIPin: Data?
    }

    struct LocalDeadline: Codable, Equatable, Sendable {
        let createdWallSeconds: UInt64
        let createdContinuousSeconds: TimeInterval
        let expiresContinuousSeconds: TimeInterval
    }

    let id: String
    let homeserverDeviceID: String
    let folderID: String
    let endpointHost: String
    let endpointPort: UInt16
    var tlsSPKIPin: Data
    var helperPublicKey: Data
    var helperKeyID: Data
    let homeserverBinding: Data
    let folderBinding: Data
    var appSeed: Data
    var appPublicKey: Data
    var appKeyID: Data
    var appEpoch: UInt64
    var helperEpoch: UInt64
    var currentCredentialStateDigest: Data
    var state: State
    var hardExpiry: UInt64
    var localDeadline: LocalDeadline?
    var lastOutgoing: Data
    var lastIncoming: Data?
    var transcriptFingerprint: String?
    var namespaceID: Data?
    var namespaceInitialAppKeyID: Data?
    var namespaceEnablement: Data?
    var namespaceRootDigest: Data?
    var namespaceManifestDigest: Data?
    var namespaceManifestEpoch: UInt64?
    var namespaceAuthorizationDigest: Data?
    var namespaceAuthorizationEpoch: UInt64
    var pendingLifecycle: PendingLifecycle?

    static func identifier(appKeyID: Data, folderBinding: Data) -> String {
        var body = appKeyID
        body.append(folderBinding)
        return DiagnosticsCrypto.base64URLEncode(
            DiagnosticsCrypto.sha256(domain: "eu.vaultsync.app/diagnostics-record/v1\0", body: body)
        )
    }
}

struct DiagnosticsInstallationCredential: Sendable {
    let privateKey: Curve25519.Signing.PrivateKey
    let markerDigest: Data
}

final class DiagnosticsCredentialStore: @unchecked Sendable {
    static let service = "eu.vaultsync.app.diagnostics.v1"
    private static let installationAccount = "installation-key-v1"
    private static let recordPrefix = "record-v1."
    private static let formatVersion = 1

    private struct InstallationEnvelope: Codable {
        let formatVersion: Int
        let seed: Data
        let markerDigest: Data
    }

    private let fileManager: FileManager
    private let applicationSupportURL: URL
    private let service: String
    private let keychain: any DiagnosticsKeychainAccess

    init(
        fileManager: FileManager = .default,
        applicationSupportURL: URL? = nil,
        service: String = DiagnosticsCredentialStore.service,
        keychain: any DiagnosticsKeychainAccess = SystemDiagnosticsKeychainAccess()
    ) {
        self.fileManager = fileManager
        self.applicationSupportURL = applicationSupportURL ?? fileManager.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        )[0]
        self.service = service
        self.keychain = keychain
    }

    /// Read-only inspection. It never creates an installation marker, key, or
    /// pairing record, which keeps an existing-user upgrade mutation-free.
    func inspection() throws -> (hasMarker: Bool, hasCredential: Bool, records: [DiagnosticsPairingRecord]) {
        let marker = try readMarker()
        let encoded = try read(account: Self.installationAccount)
        let records = try loadRecords()
        switch (marker, encoded) {
        case (nil, nil):
            guard records.isEmpty else { throw DiagnosticsProtocolError.recoveryRequired }
            return (false, false, [])
        case let (marker?, encoded?):
            _ = try decodeInstallationCredential(marker: marker, encoded: encoded)
            return (true, true, records)
        case (.some, nil), (nil, .some):
            throw DiagnosticsProtocolError.recoveryRequired
        }
    }

    /// Called only from the explicit pairing action.
    func installationCredential() throws -> DiagnosticsInstallationCredential {
        let marker = try readMarker()
        let encoded = try read(account: Self.installationAccount)
        switch (marker, encoded) {
        case (nil, nil):
            let newMarker = try DiagnosticsCrypto.randomBytes(count: 32)
            let seed = try DiagnosticsCrypto.randomBytes(count: 32)
            let digest = DiagnosticsCrypto.sha256(newMarker)
            let envelope = InstallationEnvelope(
                formatVersion: Self.formatVersion,
                seed: seed,
                markerDigest: digest
            )
            // The container marker is committed before the Keychain item. A
            // crash between them yields recoveryRequired on the next attempt;
            // it never silently adopts a surviving credential.
            try writeMarker(newMarker)
            do {
                try write(account: Self.installationAccount, data: try JSONEncoder().encode(envelope))
            } catch {
                try? removeMarker()
                throw error
            }
            return DiagnosticsInstallationCredential(
                privateKey: try Curve25519.Signing.PrivateKey(rawRepresentation: seed),
                markerDigest: digest
            )
        case let (marker?, encoded?):
            return try decodeInstallationCredential(marker: marker, encoded: encoded)
        case (.some, nil), (nil, .some):
            throw DiagnosticsProtocolError.recoveryRequired
        }
    }

    /// Durably selects the next installation-wide app key before any scoped
    /// authorization starts its D022 transition. Authorizations still advance
    /// independently, but every lagging record reuses this exact seed instead
    /// of forking the installation identity into per-folder keys.
    func advanceInstallationAppKey(
        expected: Curve25519.Signing.PrivateKey,
        proposed: Curve25519.Signing.PrivateKey
    ) throws {
        let marker = try readMarker()
        let encoded = try read(account: Self.installationAccount)
        guard let marker, let encoded else {
            throw DiagnosticsProtocolError.recoveryRequired
        }
        let current = try decodeInstallationCredential(marker: marker, encoded: encoded)
        let expectedSeed = expected.rawRepresentation
        let proposedSeed = proposed.rawRepresentation
        guard current.privateKey.rawRepresentation == expectedSeed,
              proposedSeed != expectedSeed,
              proposed.publicKey.rawRepresentation != expected.publicKey.rawRepresentation else {
            throw DiagnosticsProtocolError.conflict
        }
        let envelope = InstallationEnvelope(
            formatVersion: Self.formatVersion,
            seed: proposedSeed,
            markerDigest: current.markerDigest
        )
        try write(account: Self.installationAccount, data: try JSONEncoder().encode(envelope))
    }

    func loadRecords() throws -> [DiagnosticsPairingRecord] {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrSynchronizable as String: kCFBooleanFalse!,
            kSecReturnAttributes as String: true,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitAll,
        ]
        var result: AnyObject?
        let status = keychain.copyMatching(query as CFDictionary, result: &result)
        if status == errSecItemNotFound { return [] }
        try check(status)
        guard let entries = result as? [[String: Any]] else {
            throw DiagnosticsProtocolError.protectedDataUnavailable
        }
        var records: [DiagnosticsPairingRecord] = []
        for entry in entries {
            guard let account = entry[kSecAttrAccount as String] as? String,
                  account.hasPrefix(Self.recordPrefix),
                  let data = entry[kSecValueData as String] as? Data else { continue }
            do {
                guard data.count <= 256 * 1024 else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let record = try JSONDecoder().decode(DiagnosticsPairingRecord.self, from: data)
                try validate(record, account: account)
                records.append(record)
            } catch {
                diagnosticsCredentialLogger.error("Diagnostics credential record validation failed")
                throw DiagnosticsProtocolError.recoveryRequired
            }
        }
        return records.sorted { $0.id < $1.id }
    }

    private func decodeInstallationCredential(
        marker: Data,
        encoded: Data
    ) throws -> DiagnosticsInstallationCredential {
        do {
            guard marker.count == 32, encoded.count <= 4 * 1024 else {
                throw DiagnosticsProtocolError.recoveryRequired
            }
            let envelope = try JSONDecoder().decode(InstallationEnvelope.self, from: encoded)
            guard envelope.formatVersion == Self.formatVersion,
                  envelope.seed.count == 32,
                  envelope.markerDigest.count == 32,
                  envelope.markerDigest == DiagnosticsCrypto.sha256(marker) else {
                throw DiagnosticsProtocolError.recoveryRequired
            }
            return DiagnosticsInstallationCredential(
                privateKey: try Curve25519.Signing.PrivateKey(rawRepresentation: envelope.seed),
                markerDigest: envelope.markerDigest
            )
        } catch {
            throw DiagnosticsProtocolError.recoveryRequired
        }
    }

    private func validate(_ record: DiagnosticsPairingRecord, account: String) throws {
        guard account == Self.recordPrefix + record.id,
              let identifier = try? DiagnosticsCrypto.base64URLDecode(record.id),
              identifier.count == 32,
              record.homeserverDeviceID.utf8.count <= 63,
              (try? DiagnosticsSyncthingBinding.rawDeviceID(record.homeserverDeviceID)) != nil,
              record.folderID.utf8.count <= 255,
              (try? DiagnosticsSyncthingBinding.folderDigest(record.folderID)) != nil,
              DiagnosticsPairingProtocol.validEndpointHost(record.endpointHost),
              record.endpointPort > 0,
              validNonzero32(record.tlsSPKIPin),
              validNonzero32(record.helperPublicKey),
              record.helperKeyID == DiagnosticsCrypto.keyID(publicKey: record.helperPublicKey),
              validNonzero32(record.homeserverBinding),
              validNonzero32(record.folderBinding),
              record.appSeed.count == 32,
              validNonzero32(record.appPublicKey),
              record.appKeyID == DiagnosticsCrypto.keyID(publicKey: record.appPublicKey),
              record.appEpoch > 0,
              record.helperEpoch > 0,
              validNonzero32(record.currentCredentialStateDigest),
              record.hardExpiry > 0,
              validControlMessage(record.lastOutgoing),
              record.lastIncoming == nil || validControlMessage(record.lastIncoming!),
              record.namespaceAuthorizationEpoch <= 9 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let appKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
        guard appKey.publicKey.rawRepresentation == record.appPublicKey else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if let deadline = record.localDeadline {
            let maximumExpiry = deadline.createdContinuousSeconds +
                TimeInterval(DiagnosticsPairingProtocol.maximumLifetime)
            guard deadline.createdWallSeconds > 0,
                  deadline.createdContinuousSeconds.isFinite,
                  deadline.createdContinuousSeconds >= 0,
                  deadline.expiresContinuousSeconds.isFinite,
                  deadline.expiresContinuousSeconds > deadline.createdContinuousSeconds,
                  maximumExpiry.isFinite,
                  deadline.expiresContinuousSeconds <= maximumExpiry else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        let deadlineStates: Set<DiagnosticsPairingRecord.State> = [
            .requestPrepared, .acceptanceReceived, .finalizePrepared,
            .finalizeAcknowledged, .receiptPrepared, .readyAcknowledged,
            .activatePrepared, .abortPrepared, .namespaceEnablementPrepared,
            .namespaceAwaitingOperator, .namespaceAuthorizationPrepared,
            .namespaceAuthorizationRefreshPrepared, .lifecyclePending,
            .revocationPrepared,
        ]
        guard deadlineStates.contains(record.state) == (record.localDeadline != nil) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if let fingerprint = record.transcriptFingerprint {
            guard fingerprint.utf8.count == 12,
                  fingerprint.utf8.allSatisfy({
                      ($0 >= 0x30 && $0 <= 0x39) || ($0 >= 0x41 && $0 <= 0x46)
                  }) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        for value in [record.namespaceID, record.namespaceInitialAppKeyID,
                      record.namespaceRootDigest, record.namespaceManifestDigest,
                      record.namespaceAuthorizationDigest] {
            guard value == nil || validNonzero32(value!) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        guard record.namespaceEnablement == nil || validControlMessage(record.namespaceEnablement!) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        if record.namespaceAuthorizationEpoch > 0 {
            guard record.namespaceID != nil,
                  record.namespaceInitialAppKeyID != nil,
                  record.namespaceEnablement != nil,
                  record.namespaceRootDigest != nil,
                  record.namespaceManifestDigest != nil,
                  record.namespaceManifestEpoch != nil,
                  record.namespaceAuthorizationDigest != nil else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        try validateNamespaceState(record)
        if let pending = record.pendingLifecycle {
            let latest = try DiagnosticsPairingProtocol.decode(pending.latestMessage)
            guard record.state == .lifecyclePending,
                  validNonzero32(pending.transitionDigest),
                  pending.latestMessage == record.lastOutgoing ||
                    pending.latestMessage == record.lastIncoming,
                  latest.value.bytes(for: 5, count: 32) == record.homeserverBinding,
                  latest.value.bytes(for: 6, count: 32) == record.folderBinding,
                  latest.value.bytes(for: 7, count: 32) == record.appPublicKey,
                  latest.value.bytes(for: 8, count: 32) == record.appKeyID,
                  latest.value.bytes(for: 11, count: 32) == record.helperPublicKey,
                  latest.value.bytes(for: 12, count: 32) == record.helperKeyID,
                  latest.value.bytes(for: 15, count: 32) == record.tlsSPKIPin,
                  latest.value.unsigned(for: 17) == record.appEpoch,
                  latest.value.unsigned(for: 19) == record.helperEpoch,
                  latest.value.bytes(for: 26, count: 32) == record.currentCredentialStateDigest else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            switch pending.kind {
            case .appKey:
                guard let proposedSeed = pending.proposedAppSeed,
                      let proposedKey = try? Curve25519.Signing.PrivateKey(
                        rawRepresentation: proposedSeed
                      ),
                      latest.value.bytes(for: 9, count: 32) == proposedKey.publicKey.rawRepresentation,
                      latest.value.bytes(for: 10, count: 32) == DiagnosticsCrypto.keyID(
                        publicKey: proposedKey.publicKey.rawRepresentation
                      ),
                      record.appEpoch < UInt64.max,
                      latest.value.unsigned(for: 18) == record.appEpoch + 1,
                      [.appKeyRotationRequest, .appKeyRotationNewProof,
                       .appKeyRotationAccept, .lifecycleFinalize,
                       .lifecycleActiveAck, .lifecycleAbort].contains(latest.type),
                      pending.proposedHelperPublicKey == nil,
                      pending.proposedHelperEpoch == nil,
                      pending.proposedTLSSPKIPin == nil else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
            case .helperKey:
                guard let proposedPublic = pending.proposedHelperPublicKey,
                      validNonzero32(proposedPublic),
                      record.helperEpoch < UInt64.max,
                      pending.proposedHelperEpoch == record.helperEpoch + 1,
                      latest.value.bytes(for: 13, count: 32) == proposedPublic,
                      latest.value.bytes(for: 14, count: 32) == DiagnosticsCrypto.keyID(
                        publicKey: proposedPublic
                      ),
                      latest.value.unsigned(for: 20) == pending.proposedHelperEpoch,
                      [.helperKeyRotationConfirm, .lifecycleFinalize,
                       .lifecycleActiveAck, .lifecycleAbort].contains(latest.type),
                      pending.proposedAppSeed == nil,
                      pending.proposedTLSSPKIPin == nil else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
            case .tlsPin:
                guard let proposedPin = pending.proposedTLSSPKIPin,
                      validNonzero32(proposedPin),
                      latest.value.bytes(for: 16, count: 32) == proposedPin,
                      [.tlsPinRotationConfirm, .lifecycleFinalize,
                       .lifecycleActiveAck, .lifecycleAbort].contains(latest.type),
                      pending.proposedAppSeed == nil,
                      pending.proposedHelperPublicKey == nil,
                      pending.proposedHelperEpoch == nil else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
            }
            if [.lifecycleFinalize, .lifecycleActiveAck, .lifecycleAbort].contains(latest.type) {
                guard latest.value.unsigned(for: 29) == pending.kind.rawValue,
                      latest.value.bytes(for: 28, count: 32) == pending.transitionDigest else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
            }
        } else if record.state == .lifecyclePending {
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    private func validateNamespaceState(_ record: DiagnosticsPairingRecord) throws {
        let namespaceValues = [
            record.namespaceID,
            record.namespaceInitialAppKeyID,
            record.namespaceEnablement,
            record.namespaceRootDigest,
            record.namespaceManifestDigest,
            record.namespaceAuthorizationDigest,
        ]
        let hasAnyNamespaceValue = namespaceValues.contains(where: { $0 != nil }) ||
            record.namespaceManifestEpoch != nil
        let hasCompleteAuthorization = namespaceValues.allSatisfy { $0 != nil } &&
            record.namespaceManifestEpoch != nil &&
            record.namespaceAuthorizationEpoch > 0

        if let manifestEpoch = record.namespaceManifestEpoch {
            guard manifestEpoch > 0, manifestEpoch <= record.helperEpoch else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }

        switch record.state {
        case .requestPrepared, .acceptanceReceived, .finalizePrepared,
             .finalizeAcknowledged, .receiptPrepared, .readyAcknowledged,
             .activatePrepared, .abortPrepared, .active:
            guard !hasAnyNamespaceValue, record.namespaceAuthorizationEpoch == 0 else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .namespaceEnablementPrepared, .namespaceAwaitingOperator:
            guard record.namespaceEnablement != nil,
                  record.namespaceID == nil,
                  record.namespaceInitialAppKeyID == nil,
                  record.namespaceRootDigest == nil,
                  record.namespaceManifestDigest == nil,
                  record.namespaceManifestEpoch == nil,
                  record.namespaceAuthorizationDigest == nil,
                  record.namespaceAuthorizationEpoch == 0 else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .namespaceAuthorizationPrepared:
            guard record.namespaceEnablement != nil,
                  record.namespaceID != nil,
                  record.namespaceInitialAppKeyID == nil,
                  record.namespaceRootDigest != nil,
                  record.namespaceManifestDigest != nil,
                  record.namespaceManifestEpoch != nil,
                  record.namespaceAuthorizationDigest == nil,
                  record.namespaceAuthorizationEpoch == 0 else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .namespaceActive, .namespaceAuthorizationRefreshRequired,
             .namespaceAuthorizationRefreshPrepared:
            guard hasCompleteAuthorization else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        case .lifecyclePending, .revocationPrepared, .revoked:
            guard (!hasAnyNamespaceValue && record.namespaceAuthorizationEpoch == 0) ||
                    hasCompleteAuthorization else {
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
    }

    private func validNonzero32(_ value: Data) -> Bool {
        value.count == 32 && value.contains(where: { $0 != 0 })
    }

    private func validControlMessage(_ value: Data) -> Bool {
        !value.isEmpty && value.count <= DiagnosticsDeterministicCBOR.maximumMessageBytes &&
            (try? DiagnosticsDeterministicCBOR.decode(value)) != nil
    }

    func save(_ record: DiagnosticsPairingRecord) throws {
        let account = Self.recordPrefix + record.id
        try validate(record, account: account)
        try write(
            account: account,
            data: try JSONEncoder().encode(record)
        )
    }

    func delete(_ record: DiagnosticsPairingRecord) throws {
        try delete(account: Self.recordPrefix + record.id)
    }

    /// Explicit lost-container/lost-key recovery. It removes only this app's
    /// local diagnostics credentials; the UI requires a new QR pairing and
    /// tells the operator to revoke the old helper authorization separately.
    func resetForExplicitRepair() throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrSynchronizable as String: kCFBooleanFalse!,
        ]
        let status = keychain.delete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound {
            try check(status)
        }
        try removeMarker()
    }

    private func read(account: String) throws -> Data? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrSynchronizable as String: kCFBooleanFalse!,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var result: AnyObject?
        let status = keychain.copyMatching(query as CFDictionary, result: &result)
        if status == errSecItemNotFound { return nil }
        try check(status)
        guard let data = result as? Data else {
            throw DiagnosticsProtocolError.protectedDataUnavailable
        }
        return data
    }

    private func write(account: String, data: Data) throws {
        let identity: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrSynchronizable as String: kCFBooleanFalse!,
        ]
        let update: [String: Any] = [kSecValueData as String: data]
        let updateStatus = keychain.update(identity as CFDictionary, attributes: update as CFDictionary)
        if updateStatus == errSecSuccess { return }
        if updateStatus != errSecItemNotFound { try check(updateStatus) }
        var insertion = identity
        insertion[kSecValueData as String] = data
        insertion[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        try check(keychain.add(insertion as CFDictionary))
    }

    private func delete(account: String) throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrSynchronizable as String: kCFBooleanFalse!,
        ]
        let status = keychain.delete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound {
            try check(status)
        }
    }

    private func check(_ status: OSStatus) throws {
        guard status == errSecSuccess else {
            diagnosticsCredentialLogger.error("Diagnostics Keychain operation failed with status category \(status)")
            switch status {
            case errSecInteractionNotAllowed, errSecNotAvailable, errSecAuthFailed:
                throw DiagnosticsProtocolError.protectedDataUnavailable
            default:
                throw DiagnosticsProtocolError.unavailable
            }
        }
    }

    private var markerURL: URL {
        applicationSupportURL
            .appendingPathComponent("ControlledDiagnostics", isDirectory: true)
            .appendingPathComponent("installation-marker-v1", isDirectory: false)
    }

    private func readMarker() throws -> Data? {
        do {
            let data = try Data(contentsOf: markerURL, options: [.uncached])
            guard data.count == 32 else { throw DiagnosticsProtocolError.recoveryRequired }
            return data
        } catch let error as CocoaError where error.code == .fileReadNoSuchFile {
            return nil
        } catch let error as DiagnosticsProtocolError {
            throw error
        } catch {
            throw DiagnosticsProtocolError.protectedDataUnavailable
        }
    }

    private func writeMarker(_ marker: Data) throws {
        guard marker.count == 32 else { throw DiagnosticsProtocolError.invalidMessage }
        let directory = markerURL.deletingLastPathComponent()
        do {
            try fileManager.createDirectory(
                at: directory,
                withIntermediateDirectories: true,
                attributes: [.protectionKey: FileProtectionType.complete]
            )
            try marker.write(to: markerURL, options: [.atomic, .completeFileProtection])
        } catch {
            throw DiagnosticsProtocolError.protectedDataUnavailable
        }
    }

    private func removeMarker() throws {
        do {
            try fileManager.removeItem(at: markerURL)
        } catch let error as CocoaError where error.code == .fileNoSuchFile {
            return
        } catch {
            throw DiagnosticsProtocolError.protectedDataUnavailable
        }
    }
}

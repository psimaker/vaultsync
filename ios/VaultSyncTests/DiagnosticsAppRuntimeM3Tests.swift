import CryptoKit
import Foundation
import Security
import Testing
@testable import VaultSync

@Suite("Controlled diagnostics app runtime (M3)", .serialized)
struct DiagnosticsAppRuntimeM3Tests {
    @Test("Production pairing decoder accepts every Decision 022 golden and rejects tampering")
    func productionPairingGoldens() throws {
        let fixture = try loadDiagnosticsHexFixture(named: "diagnostics-pairing-m3")
        let names = [
            "00_qr", "01_app_request", "02_helper_accept", "03_finalize", "04_finalize_ack",
            "05_receipt", "06_ready_ack", "07_activate", "08_active_ack", "09_abort", "10_abort_ack",
            "11_app_key_rotation_request", "12_app_key_rotation_new_proof", "13_app_key_rotation_accept",
            "14_helper_key_rotation_propose", "15_helper_key_rotation_new_proof", "16_helper_key_rotation_confirm",
            "17_tls_pin_rotation_propose", "18_tls_pin_rotation_confirm", "19_revocation_request",
            "20_revocation_record", "21_lifecycle_finalize", "22_lifecycle_active_ack",
            "23_lifecycle_abort", "24_lifecycle_abort_ack",
        ]
        #expect(fixture.keys.sorted() == names)

        for (rawType, name) in names.enumerated() {
            let encoded = try Data(m1Hex: #require(fixture[name]))
            let message: DiagnosticsPairingProtocol.Message
            do {
                message = try DiagnosticsPairingProtocol.decode(encoded)
            } catch {
                Issue.record("Production decoder rejected \(name): \(error)")
                continue
            }
            #expect(message.type.rawValue == UInt64(rawType))
            #expect(message.canonical == encoded)
            #expect(try DiagnosticsDeterministicCBOR.encode(message.value) == encoded)

            if rawType > 0 {
                var tampered = encoded
                tampered[tampered.index(before: tampered.endIndex)] ^= 0x01
                expectDiagnosticsError(.invalidMessage) {
                    _ = try DiagnosticsPairingProtocol.decode(tampered)
                }
            }
        }

        let qrData = try Data(m1Hex: #require(fixture["00_qr"]))
        let qrMessage = try DiagnosticsPairingProtocol.decode(qrData)
        let issuedAt = try #require(qrMessage.value.unsigned(for: 15))
        let qr = DiagnosticsCrypto.base64URLEncode(qrData)
        #expect(
            try DiagnosticsPairingProtocol.decodeQR(
                qr,
                now: Date(timeIntervalSince1970: TimeInterval(issuedAt))
            ).canonical == qrData
        )

        for invalid in ["a201000101", "a202000100", "1817", "5f4100ff", "bf0100ff", "f4"] {
            expectDiagnosticsError(.invalidMessage) {
                _ = try DiagnosticsDeterministicCBOR.decode(Data(m1Hex: invalid))
            }
        }
        expectDiagnosticsError(.invalidMessage) {
            _ = try DiagnosticsCrypto.base64URLDecode(
                String(repeating: "A", count: DiagnosticsDeterministicCBOR.maximumMessageBytes * 2)
            )
        }
    }

    @Test("Extreme external epochs and timestamps fail closed without arithmetic traps")
    func arithmeticBoundaries() throws {
        let fixture = try loadDiagnosticsHexFixture(named: "diagnostics-pairing-m3")
        let rotation = try DiagnosticsDeterministicCBOR.decode(
            Data(m1Hex: #require(fixture["11_app_key_rotation_request"]))
        )
        guard case .map(var fields) = rotation else {
            Issue.record("Expected lifecycle map")
            return
        }
        fields = fields.map { field in
            switch field.label {
            case 17, 18:
                return DiagnosticsCBORField(label: field.label, value: .unsigned(UInt64.max))
            default:
                return field
            }
        }
        let extremeEpoch = try DiagnosticsDeterministicCBOR.encode(.map(fields))
        expectDiagnosticsError(.invalidMessage) {
            _ = try DiagnosticsPairingProtocol.decode(extremeEpoch)
        }

        let appKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(repeating: 0x31, count: 32)
        )
        let record = makeRuntimeRecord(
            appSeed: appKey.rawRepresentation,
            helperPublic: Curve25519.Signing.PrivateKey().publicKey.rawRepresentation,
            homeserver: Data(repeating: 0x11, count: 32),
            folder: Data(repeating: 0x12, count: 32)
        )
        expectDiagnosticsError(.invalidMessage) {
            _ = try DiagnosticsCapabilityProtocol.makeQuery(
                record: record,
                appKey: appKey,
                nonce: Data(repeating: 0x44, count: 32),
                now: Date(timeIntervalSince1970: Double(UInt64.max))
            )
        }
    }

    @Test("Production capability query is byte-exact and response validation is mutually authenticated")
    func capabilityWireContract() throws {
        let fixture = try M1ContractFixtureLoader.load()
        let vector = fixture.vectors.contractQuery
        let appKey = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(m1Hex: fixture.vectors.rfc8032.seedHex)
        )
        let helperPublic = Data(repeating: vector.helperPublicKeyByte, count: 32)
        let record = makeRuntimeRecord(
            appSeed: appKey.rawRepresentation,
            helperPublic: helperPublic,
            homeserver: Data(repeating: vector.homeserverByte, count: 32),
            folder: Data(repeating: vector.folderByte, count: 32)
        )
        let query = try DiagnosticsCapabilityProtocol.makeQuery(
            record: record,
            appKey: appKey,
            nonce: Data(repeating: vector.queryNonceByte, count: 32),
            now: Date(timeIntervalSince1970: TimeInterval(vector.issuedAt))
        )
        let value = try DiagnosticsDeterministicCBOR.decode(query.message)
        let body = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
        #expect(body.m1Hex == vector.expectedCanonicalBodyHex)
        #expect(query.digest.m1Hex == vector.expectedDigestHex)

        var signedInput = Data("eu.vaultsync.roundtrip/v1/capability-query\0".utf8)
        signedInput.append(body)
        let expectedSignature = try Data(m1Hex: vector.expectedSignatureHex)
        #expect(appKey.publicKey.isValidSignature(expectedSignature, for: signedInput))
        #expect(appKey.publicKey.isValidSignature(try #require(value.bytes(for: 255, count: 64)), for: signedInput))

        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x44, count: 32))
        let responseRecord = makeRuntimeRecord(
            appSeed: appKey.rawRepresentation,
            helperPublic: helperKey.publicKey.rawRepresentation,
            homeserver: record.homeserverBinding,
            folder: record.folderBinding
        )
        let responseQuery = try DiagnosticsCapabilityProtocol.makeQuery(
            record: responseRecord,
            appKey: appKey,
            nonce: Data(repeating: 0x45, count: 32),
            now: Date(timeIntervalSince1970: 1_700_000_100)
        )
        let response = try makeCapabilityResponse(
            query: responseQuery,
            record: responseRecord,
            helperKey: helperKey,
            flags: DiagnosticsCapabilityProtocol.requiredFlags,
            issuedAt: 1_700_000_100
        )
        try DiagnosticsCapabilityProtocol.validateResponse(
            response,
            query: responseQuery,
            record: responseRecord,
            now: Date(timeIntervalSince1970: 1_700_000_100)
        )

        let incomplete = try makeCapabilityResponse(
            query: responseQuery,
            record: responseRecord,
            helperKey: helperKey,
            flags: DiagnosticsCapabilityProtocol.requiredFlags - 1,
            issuedAt: 1_700_000_100
        )
        expectDiagnosticsError(.invalidMessage) {
            try DiagnosticsCapabilityProtocol.validateResponse(
                incomplete,
                query: responseQuery,
                record: responseRecord,
                now: Date(timeIntervalSince1970: 1_700_000_100)
            )
        }
    }

    @Test("Production namespace records reproduce and validate the Decision 023 golden chain")
    @MainActor
    func namespaceWireContract() async throws {
        let fixture = try loadDiagnosticsHexFixture(named: "diagnostics-namespace-m4")
        let enablement = try Data(m1Hex: #require(fixture["01_enablement"]))
        let rootData = try Data(m1Hex: #require(fixture["02_root_manifest"]))
        let helperEpochData = try Data(m1Hex: #require(fixture["03_helper_epoch"]))
        let initialAuthorization = try Data(m1Hex: #require(fixture["04_initial_authorization"]))
        let authorizationEpoch = try Data(m1Hex: #require(fixture["05_authorization_epoch"]))

        for encoded in [enablement, rootData, helperEpochData, initialAuthorization, authorizationEpoch] {
            #expect(try DiagnosticsDeterministicCBOR.encode(DiagnosticsDeterministicCBOR.decode(encoded)) == encoded)
        }

        let enablementValue = try DiagnosticsDeterministicCBOR.decode(enablement)
        var initialRecord = makeRuntimeRecord(
            appSeed: Data(repeating: 0x31, count: 32),
            helperPublic: try #require(enablementValue.bytes(for: 13, count: 32)),
            homeserver: try #require(enablementValue.bytes(for: 5, count: 32)),
            folder: try #require(enablementValue.bytes(for: 6, count: 32)),
            appEpoch: try #require(enablementValue.unsigned(for: 12)),
            helperEpoch: try #require(enablementValue.unsigned(for: 15))
        )
        initialRecord.currentCredentialStateDigest = Data(repeating: 0x25, count: 32)
        let reproducedEnablement = try DiagnosticsNamespaceProtocol.makeEnablement(
            record: initialRecord,
            appKey: Curve25519.Signing.PrivateKey(rawRepresentation: initialRecord.appSeed),
            nonce: try #require(enablementValue.bytes(for: 19, count: 32)),
            now: Date(timeIntervalSince1970: TimeInterval(try #require(enablementValue.unsigned(for: 26))))
        )
        let reproducedEnablementBody = try removingSignatures(reproducedEnablement, labels: [253])
        let expectedEnablementBody = try removingSignatures(enablement, labels: [253])
        #expect(reproducedEnablementBody == expectedEnablementBody)

        let root = try DiagnosticsNamespaceProtocol.validateRootManifest(
            rootData,
            enablement: enablement,
            record: initialRecord
        )
        let helperEpochValue = try DiagnosticsDeterministicCBOR.decode(helperEpochData)
        var currentRecord = initialRecord
        currentRecord.helperPublicKey = try #require(helperEpochValue.bytes(for: 13, count: 32))
        currentRecord.helperKeyID = try #require(helperEpochValue.bytes(for: 14, count: 32))
        currentRecord.helperEpoch = try #require(helperEpochValue.unsigned(for: 15))
        let manifestDigest = try DiagnosticsNamespaceProtocol.validateHelperEpochManifest(
            helperEpochData,
            rootData: rootData,
            priorManifestData: rootData,
            record: currentRecord
        )
        let currentRoot = DiagnosticsNamespaceProtocol.RootManifest(
            message: root.message,
            namespaceID: root.namespaceID,
            rootDigest: root.rootDigest,
            manifestDigest: manifestDigest
        )

        let initialValue = try DiagnosticsDeterministicCBOR.decode(initialAuthorization)
        currentRecord.currentCredentialStateDigest = try #require(initialValue.bytes(for: 25, count: 32))
        let initialCandidate = try DiagnosticsNamespaceProtocol.makeInitialAuthorization(
            record: currentRecord,
            root: currentRoot,
            appKey: Curve25519.Signing.PrivateKey(rawRepresentation: currentRecord.appSeed),
            nonce: try #require(initialValue.bytes(for: 30, count: 32)),
            now: Date(timeIntervalSince1970: TimeInterval(try #require(initialValue.unsigned(for: 26))))
        )
        let expectedInitialCandidate = try removingSignatures(initialAuthorization, labels: [255])
        let generatedInitialBody = try removingSignatures(initialCandidate.message, labels: [253])
        let expectedInitialBody = try removingSignatures(initialAuthorization, labels: [253, 255])
        #expect(generatedInitialBody == expectedInitialBody)
        let fixtureInitialCandidate = DiagnosticsNamespaceProtocol.AuthorizationCandidate(
            message: expectedInitialCandidate,
            installationBinding: initialCandidate.installationBinding
        )
        let initialDigest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorization(
            initialAuthorization,
            candidate: fixtureInitialCandidate,
            record: currentRecord,
            root: currentRoot
        )

        let epochValue = try DiagnosticsDeterministicCBOR.decode(authorizationEpoch)
        currentRecord.appSeed = Data(repeating: 0x32, count: 32)
        currentRecord.appPublicKey = try #require(epochValue.bytes(for: 10, count: 32))
        currentRecord.appKeyID = try #require(epochValue.bytes(for: 11, count: 32))
        currentRecord.appEpoch = try #require(epochValue.unsigned(for: 12))
        currentRecord.currentCredentialStateDigest = try #require(epochValue.bytes(for: 25, count: 32))
        guard let initialAppKeyID = epochValue.bytes(for: 9, count: 32) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        currentRecord.namespaceInitialAppKeyID = initialAppKeyID
        currentRecord.namespaceAuthorizationDigest = initialDigest
        currentRecord.namespaceAuthorizationEpoch = 1
        let epochCandidate = try DiagnosticsNamespaceProtocol.makeAuthorizationEpoch(
            record: currentRecord,
            root: currentRoot,
            priorAuthorizationDigest: initialDigest,
            appKey: Curve25519.Signing.PrivateKey(rawRepresentation: currentRecord.appSeed),
            nonce: try #require(epochValue.bytes(for: 30, count: 32)),
            now: Date(timeIntervalSince1970: TimeInterval(try #require(epochValue.unsigned(for: 26))))
        )
        let expectedEpochCandidate = try removingSignatures(authorizationEpoch, labels: [255])
        let generatedEpochBody = try removingSignatures(epochCandidate.message, labels: [253])
        let expectedEpochBody = try removingSignatures(authorizationEpoch, labels: [253, 255])
        #expect(generatedEpochBody == expectedEpochBody)
        let fixtureEpochCandidate = DiagnosticsNamespaceProtocol.AuthorizationCandidate(
            message: expectedEpochCandidate,
            installationBinding: epochCandidate.installationBinding
        )
        let epochDigest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorizationEpoch(
            authorizationEpoch,
            candidate: fixtureEpochCandidate,
            record: currentRecord,
            root: currentRoot
        )

        let expectedPath = M1ContractCrypto.base32LowerNoPadding(initialCandidate.installationBinding)
        #expect(
            try DiagnosticsNamespaceProtocol.authorizationRelativePath(
                installationBinding: initialCandidate.installationBinding
            ) == "installations/\(expectedPath)/authorization.cbor"
        )
        #expect(
            try DiagnosticsNamespaceProtocol.authorizationEpochRelativePath(
                installationBinding: initialCandidate.installationBinding,
                epoch: 2
            ) == "installations/\(expectedPath)/authorization-epochs/2.authorization.cbor"
        )

        // A later app-key rotation does not require or append another helper
        // manifest when the stored helper-manifest epoch is already current.
        currentRecord.namespaceID = currentRoot.namespaceID
        currentRecord.namespaceEnablement = enablement
        currentRecord.namespaceRootDigest = currentRoot.rootDigest
        currentRecord.namespaceManifestDigest = currentRoot.manifestDigest
        currentRecord.namespaceManifestEpoch = currentRecord.helperEpoch
        currentRecord.namespaceAuthorizationDigest = epochDigest
        currentRecord.namespaceAuthorizationEpoch = 2
        currentRecord.state = .namespaceActive
        currentRecord.lastOutgoing = epochCandidate.message
        currentRecord.lastIncoming = authorizationEpoch

        let identifier = UUID().uuidString.lowercased()
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-manifest-epoch-\(identifier)", isDirectory: true)
        let folder = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-manifest-folder-\(identifier)", isDirectory: true)
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)",
            keychain: InMemoryDiagnosticsKeychain()
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
            try? FileManager.default.removeItem(at: folder)
        }
        _ = try store.installationCredential()
        try store.save(currentRecord)
        let namespace = folder.appendingPathComponent(
            DiagnosticsNamespaceProtocol.rootName,
            isDirectory: true
        )
        try FileManager.default.createDirectory(at: namespace, withIntermediateDirectories: true)
        try rootData.write(
            to: namespace.appendingPathComponent(DiagnosticsNamespaceProtocol.rootManifestName),
            options: .atomic
        )
        let helper = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(repeating: 0x42, count: 32)
        )
        #expect(helper.publicKey.rawRepresentation == currentRecord.helperPublicKey)
        let transport = LifecycleTransport(
            currentHelper: helper,
            proposedHelper: nil,
            capabilityHelper: helper,
            now: Date(timeIntervalSince1970: 1_700_000_000)
        )
        let controller = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { Date(timeIntervalSince1970: 1_700_000_000) }
        )
        controller.refresh()
        await controller.startAppKeyRotation(recordID: currentRecord.id)
        #expect(controller.records.first?.state == .namespaceAuthorizationRefreshRequired)
        await controller.continueNamespaceAuthorizationRefresh(
            recordID: currentRecord.id,
            currentFolderPath: folder.path
        )
        #expect(controller.lastError == nil)
        #expect(controller.records.first?.state == .namespaceAuthorizationRefreshPrepared)
        #expect(controller.records.first?.namespaceManifestEpoch == currentRecord.helperEpoch)
        #expect(!FileManager.default.fileExists(
            atPath: namespace.appendingPathComponent("manifest-epochs", isDirectory: true).path
        ))
    }

    @Test("Pairing advances only after fingerprint confirmation and survives app restart")
    @MainActor
    func explicitPairingStateMachine() async throws {
        let identifier = UUID().uuidString.lowercased()
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-pairing-\(identifier)", isDirectory: true)
        let keychain = InMemoryDiagnosticsKeychain()
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)",
            keychain: keychain
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
        }

        let now = Date(timeIntervalSince1970: 1_700_000_000)
        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x41, count: 32))
        let transport = BootstrapPairingTransport(helperKey: helperKey, issuedAt: 1_700_000_000)
        let controller = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { now }
        )
        controller.refresh()
        #expect(controller.records.isEmpty)
        #expect(!controller.hasInstallationMarker)
        #expect(!controller.hasInstallationCredential)

        let deviceID = "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2"
        let folderID = "folder-alpha"
        let invitation = try makePairingInvitation(
            helperKey: helperKey,
            deviceID: deviceID,
            folderID: folderID,
            issuedAt: 1_700_000_000
        )
        await controller.beginPairing(
            qr: DiagnosticsCrypto.base64URLEncode(invitation),
            homeserverDeviceID: deviceID,
            folderID: folderID
        )
        #expect(controller.lastError == nil)
        let pending = try #require(controller.records.first)
        #expect(pending.state == .acceptanceReceived)
        #expect(pending.namespaceID == nil)
        #expect(pending.namespaceEnablement == nil)
        #expect(pending.namespaceAuthorizationEpoch == 0)
        guard case .fingerprint(let recordID, let fingerprint) = controller.notice else {
            Issue.record("Pairing became active without an explicit transcript comparison")
            return
        }
        #expect(recordID == pending.id)
        #expect(fingerprint.count == 12)
        let initialTypes = await transport.observedTypes()
        #expect(initialTypes == [.appRequest])

        await controller.confirmFingerprintAndActivate(recordID: pending.id)
        #expect(controller.lastError == nil)
        #expect(controller.records.first?.state == .active)
        #expect(controller.records.first?.namespaceID == nil)
        let activeTypes = await transport.observedTypes()
        #expect(activeTypes == [.appRequest, .finalize, .receipt, .activate])

        let restarted = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { now }
        )
        restarted.refresh()
        #expect(restarted.records.count == 1)
        #expect(restarted.records.first?.state == .active)
        #expect(restarted.records.first?.transcriptFingerprint == fingerprint)
        #expect(restarted.hasInstallationMarker)
        #expect(restarted.hasInstallationCredential)
    }

    @Test("Pre-activation cancellation is signed, retryable, and removes only pending local state")
    @MainActor
    func explicitPairingCancellation() async throws {
        let identifier = UUID().uuidString.lowercased()
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-cancel-\(identifier)", isDirectory: true)
        let keychain = InMemoryDiagnosticsKeychain()
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)",
            keychain: keychain
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
        }
        let now = Date(timeIntervalSince1970: 1_700_000_000)
        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x41, count: 32))
        let transport = BootstrapPairingTransport(helperKey: helperKey, issuedAt: 1_700_000_000)
        let controller = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { now }
        )
        let deviceID = "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2"
        let invitation = try makePairingInvitation(
            helperKey: helperKey,
            deviceID: deviceID,
            folderID: "folder-alpha",
            issuedAt: 1_700_000_000
        )
        await controller.beginPairing(
            qr: DiagnosticsCrypto.base64URLEncode(invitation),
            homeserverDeviceID: deviceID,
            folderID: "folder-alpha"
        )
        let recordID = try #require(controller.records.first?.id)
        await controller.cancelPendingPairing(recordID: recordID)
        #expect(controller.lastError == nil)
        #expect(controller.records.isEmpty)
        let types = await transport.observedTypes()
        #expect(types == [.appRequest, .abort])
        let inspection = try store.inspection()
        #expect(inspection.hasMarker)
        #expect(inspection.hasCredential)
        #expect(inspection.records.isEmpty)
    }

    @Test("Namespace activation requires separate operator creation and synchronized countersignature")
    @MainActor
    func explicitNamespaceStateMachine() async throws {
        let identifier = UUID().uuidString.lowercased()
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-namespace-store-\(identifier)", isDirectory: true)
        let folder = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-namespace-folder-\(identifier)", isDirectory: true)
        let keychain = InMemoryDiagnosticsKeychain()
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)",
            keychain: keychain
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
            try? FileManager.default.removeItem(at: folder)
        }
        _ = try store.installationCredential()
        let appSeed = Data(repeating: 0x31, count: 32)
        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x41, count: 32))
        let record = makeRuntimeRecord(
            appSeed: appSeed,
            helperPublic: helperKey.publicKey.rawRepresentation,
            homeserver: Data(repeating: 0x05, count: 32),
            folder: Data(repeating: 0x06, count: 32)
        )
        try store.save(record)
        let now = Date(timeIntervalSince1970: 1_700_000_000)
        let transport = NamespaceControlTransport(record: record, helperKey: helperKey)
        let controller = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in transport },
            now: { now }
        )
        controller.refresh()
        await controller.checkCapability(recordID: record.id)
        #expect(controller.capabilityStates[record.id] == .available)

        await controller.requestNamespaceEnablement(recordID: record.id)
        #expect(controller.lastError == nil)
        #expect(controller.records.first?.state == .namespaceAwaitingOperator)
        #expect(controller.records.first?.namespaceID == nil)
        await controller.startAppKeyRotation(recordID: record.id)
        #expect(controller.lastError == .unavailable)
        #expect(controller.records.first?.state == .namespaceAwaitingOperator)
        #expect(controller.records.first?.pendingLifecycle == nil)
        let capturedEnablement = await transport.latestEnablement()
        let enablement = try #require(capturedEnablement)

        // This filesystem mutation represents the distinct, explicit helper
        // operator step. The app controller itself never creates this path.
        let namespace = folder.appendingPathComponent(DiagnosticsNamespaceProtocol.rootName, isDirectory: true)
        try FileManager.default.createDirectory(at: namespace, withIntermediateDirectories: true)
        let m4Fixture = try loadDiagnosticsHexFixture(named: "diagnostics-namespace-m4")
        let goldenRoot = try DiagnosticsDeterministicCBOR.decode(
            Data(m1Hex: #require(m4Fixture["02_root_manifest"]))
        )
        let readmeDigest = try #require(goldenRoot.bytes(for: 29, count: 32))
        let rootData = try makeNamespaceRoot(
            enablement: enablement,
            record: record,
            helperKey: helperKey,
            readmeDigest: readmeDigest,
            createdAt: 1_700_000_020
        )
        let lateRoot = try makeNamespaceRoot(
            enablement: enablement,
            record: record,
            helperKey: helperKey,
            readmeDigest: readmeDigest,
            createdAt: 1_700_000_301
        )
        expectDiagnosticsError(.invalidMessage) {
            _ = try DiagnosticsNamespaceProtocol.validateRootManifest(
                lateRoot,
                enablement: enablement,
                record: record
            )
        }
        try rootData.write(
            to: namespace.appendingPathComponent(DiagnosticsNamespaceProtocol.rootManifestName),
            options: .atomic
        )

        await controller.continueNamespace(recordID: record.id, currentFolderPath: folder.path)
        #expect(controller.lastError == nil)
        #expect(controller.records.first?.state == .namespaceAuthorizationPrepared)
        let capturedAuthorization = await transport.latestAuthorization()
        let candidate = try #require(capturedAuthorization)
        let completed = try countersignInitialAuthorization(candidate, helperKey: helperKey)
        let candidateValue = try DiagnosticsDeterministicCBOR.decode(candidate)
        let installation = try #require(candidateValue.bytes(for: 8, count: 32))
        let relative = try DiagnosticsNamespaceProtocol.authorizationRelativePath(
            installationBinding: installation
        )
        let authorizationURL = namespace.appendingPathComponent(relative)
        try FileManager.default.createDirectory(
            at: authorizationURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try completed.write(to: authorizationURL, options: .atomic)

        await controller.continueNamespace(recordID: record.id, currentFolderPath: folder.path)
        #expect(controller.lastError == nil)
        let active = try #require(controller.records.first)
        #expect(active.state == .namespaceActive)
        #expect(active.namespaceAuthorizationEpoch == 1)
        #expect(active.namespaceAuthorizationDigest == DiagnosticsNamespaceProtocol.recordDigest(completed))
        let paths = await transport.observedPaths()
        #expect(paths == [
            DiagnosticsCapabilityProtocol.path,
            DiagnosticsNamespaceProtocol.enablementPath,
            DiagnosticsNamespaceProtocol.authorizationPath,
            DiagnosticsNamespaceProtocol.authorizationPath,
        ])
        #expect(!FileManager.default.fileExists(
            atPath: namespace.appendingPathComponent("operations", isDirectory: true).path
        ))
    }

    @Test("App-key rotation confirms proposed capability before revocation")
    @MainActor
    func appKeyRotationAndRevocation() async throws {
        let context = try LifecycleTestContext.make(label: "app-key")
        defer { context.cleanup() }
        let initialAppPublic = context.record.appPublicKey
        let helperKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x41, count: 32))
        let transport = LifecycleTransport(
            currentHelper: helperKey,
            proposedHelper: nil,
            capabilityHelper: helperKey,
            now: Date(timeIntervalSince1970: 1_700_000_000)
        )
        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { Date(timeIntervalSince1970: 1_700_000_000) }
        )
        controller.refresh()
        await controller.startAppKeyRotation(recordID: context.record.id)
        #expect(controller.lastError == nil)
        let rotated = try #require(controller.records.first)
        #expect(rotated.state == .active)
        #expect(rotated.appEpoch == 2)
        #expect(rotated.appPublicKey != initialAppPublic)
        #expect(rotated.helperPublicKey == helperKey.publicKey.rawRepresentation)
        #expect(rotated.pendingLifecycle == nil)

        await controller.revoke(recordID: rotated.id, reason: .userRequest)
        #expect(controller.lastError == nil)
        let revoked = try #require(controller.records.first)
        #expect(revoked.state == .revoked)
        #expect(revoked.appEpoch == 3)
        let types = await transport.observedTypes()
        #expect(types == [
            .appKeyRotationRequest,
            .appKeyRotationNewProof,
            .lifecycleFinalize,
            .revocationRequest,
        ])
    }

    @Test("App-key rotation reuses one staged installation key across folders")
    @MainActor
    func appKeyRotationSharesInstallationKey() async throws {
        let context = try LifecycleTestContext.make(label: "app-key-multi-folder")
        defer { context.cleanup() }
        let second = makeRuntimeRecord(
            appSeed: context.record.appSeed,
            helperPublic: context.record.helperPublicKey,
            homeserver: context.record.homeserverBinding,
            folder: Data(repeating: 0x13, count: 32),
            folderID: "folder-beta"
        )
        try context.store.save(second)

        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in
                FailingDiagnosticsTransport(error: .unavailable)
            },
            now: { Date(timeIntervalSince1970: 1_700_000_000) }
        )
        controller.refresh()
        await controller.startAppKeyRotation(recordID: context.record.id)
        #expect(controller.lastError == .unavailable)
        let firstPending = try #require(
            controller.records.first(where: { $0.id == context.record.id })?.pendingLifecycle
        )
        let stagedSeed = try #require(firstPending.proposedAppSeed)
        #expect(stagedSeed != context.record.appSeed)
        #expect(try context.store.installationCredential().privateKey.rawRepresentation == stagedSeed)

        await controller.startAppKeyRotation(recordID: second.id)
        #expect(controller.lastError == .unavailable)
        let secondPending = try #require(
            controller.records.first(where: { $0.id == second.id })?.pendingLifecycle
        )
        #expect(secondPending.proposedAppSeed == stagedSeed)
    }

    @Test("Helper-key rotation requires old/new proof and proposed-helper capability")
    @MainActor
    func helperKeyRotation() async throws {
        let fixture = try loadDiagnosticsHexFixture(named: "diagnostics-pairing-m3")
        let proposal = try Data(m1Hex: #require(fixture["14_helper_key_rotation_propose"]))
        let proof = try Data(m1Hex: #require(fixture["15_helper_key_rotation_new_proof"]))
        let proposalValue = try DiagnosticsDeterministicCBOR.decode(proposal)
        let record = try lifecycleFixtureRecord(value: proposalValue)
        let context = try LifecycleTestContext.make(label: "helper-key", record: record)
        defer { context.cleanup() }
        let currentHelper = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x41, count: 32))
        let proposedHelper = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x42, count: 32))
        let transport = LifecycleTransport(
            currentHelper: currentHelper,
            proposedHelper: proposedHelper,
            capabilityHelper: proposedHelper,
            now: Date(timeIntervalSince1970: 1_700_000_000)
        )
        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { Date(timeIntervalSince1970: 1_700_000_000) }
        )
        controller.refresh()
        await controller.startHelperKeyRotation(
            recordID: record.id,
            proposal: DiagnosticsCrypto.base64URLEncode(proposal),
            proof: DiagnosticsCrypto.base64URLEncode(proof)
        )
        #expect(controller.lastError == nil)
        let rotated = try #require(controller.records.first)
        #expect(rotated.state == .active)
        #expect(rotated.helperEpoch == 2)
        #expect(rotated.helperPublicKey == proposedHelper.publicKey.rawRepresentation)
        #expect(rotated.pendingLifecycle == nil)
        let types = await transport.observedTypes()
        #expect(types == [.helperKeyRotationConfirm, .lifecycleFinalize])
    }

    @Test("TLS-pin rotation switches only after proposed-pin capability")
    @MainActor
    func tlsPinRotation() async throws {
        let fixture = try loadDiagnosticsHexFixture(named: "diagnostics-pairing-m3")
        let proposal = try Data(m1Hex: #require(fixture["17_tls_pin_rotation_propose"]))
        let proposalValue = try DiagnosticsDeterministicCBOR.decode(proposal)
        let record = try lifecycleFixtureRecord(value: proposalValue)
        let context = try LifecycleTestContext.make(label: "tls-pin", record: record)
        defer { context.cleanup() }
        let helper = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(repeating: 0x41, count: 32))
        let transport = LifecycleTransport(
            currentHelper: helper,
            proposedHelper: nil,
            capabilityHelper: helper,
            now: Date(timeIntervalSince1970: 1_700_000_000)
        )
        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { Date(timeIntervalSince1970: 1_700_000_000) }
        )
        controller.refresh()
        await controller.startTLSPinRotation(
            recordID: record.id,
            proposal: DiagnosticsCrypto.base64URLEncode(proposal)
        )
        #expect(controller.lastError == nil)
        let rotated = try #require(controller.records.first)
        #expect(rotated.state == .active)
        #expect(rotated.tlsSPKIPin == Data(repeating: 0x16, count: 32))
        #expect(rotated.pendingLifecycle == nil)
        let types = await transport.observedTypes()
        #expect(types == [.tlsPinRotationConfirm, .lifecycleFinalize])
    }

    @Test("Pre-commit credential rotation abort preserves the current authority")
    @MainActor
    func lifecycleAbort() async throws {
        let context = try LifecycleTestContext.make(label: "lifecycle-abort")
        defer { context.cleanup() }
        let helper = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(repeating: 0x41, count: 32)
        )
        let transport = AbortLifecycleTransport(
            currentHelper: helper,
            now: Date(timeIntervalSince1970: 1_700_000_000)
        )
        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { Date(timeIntervalSince1970: 1_700_000_000) }
        )
        controller.refresh()
        await controller.startAppKeyRotation(recordID: context.record.id)
        #expect(controller.lastError == .unavailable)
        #expect(controller.records.first?.state == .lifecyclePending)
        #expect(controller.records.first?.pendingLifecycle != nil)

        await controller.abortLifecycle(recordID: context.record.id)
        #expect(controller.lastError == nil)
        let restored = try #require(controller.records.first)
        #expect(restored.state == .active)
        #expect(restored.pendingLifecycle == nil)
        #expect(restored.appEpoch == context.record.appEpoch)
        #expect(restored.appPublicKey == context.record.appPublicKey)
        #expect(controller.capabilityStates[restored.id] == .unavailable)
        let types = await transport.observedTypes()
        #expect(types == [
            .appKeyRotationRequest,
            .appKeyRotationNewProof,
            .appKeyRotationNewProof,
            .lifecycleAbort,
        ])
    }

    @Test("Persisted monotonic deadline survives restart and defeats wall-clock rollback")
    @MainActor
    func persistedMonotonicDeadline() async throws {
        let context = try LifecycleTestContext.make(label: "monotonic-deadline")
        defer { context.cleanup() }
        let helper = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(repeating: 0x41, count: 32)
        )
        let transport = AbortLifecycleTransport(
            currentHelper: helper,
            now: Date(timeIntervalSince1970: 1_700_000_000)
        )
        let clock = LockedDiagnosticsClock(
            Date(timeIntervalSince1970: 1_700_000_000),
            continuous: 1_000
        )
        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { clock.value() },
            continuousNow: { clock.continuousValue() }
        )
        controller.refresh()
        await controller.startAppKeyRotation(recordID: context.record.id)
        #expect(controller.lastError == .unavailable)
        #expect(controller.records.first?.localDeadline != nil)

        clock.shiftWall(by: -100)
        clock.advanceContinuous(by: 301)
        let restarted = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { clock.value() },
            continuousNow: { clock.continuousValue() }
        )
        restarted.refresh()
        let pending = try #require(restarted.records.first)
        #expect(restarted.canDiscardExpiredLifecycle(pending))
        restarted.discardExpiredLifecycle(recordID: pending.id)
        #expect(restarted.lastError == nil)
        #expect(restarted.records.first?.state == .active)
        #expect(restarted.records.first?.localDeadline == nil)
    }

    @Test("Expired capability evidence cannot authorize namespace enablement")
    @MainActor
    func capabilityExpiry() async throws {
        let context = try LifecycleTestContext.make(label: "capability-expiry")
        defer { context.cleanup() }
        let helper = try Curve25519.Signing.PrivateKey(
            rawRepresentation: Data(repeating: 0x41, count: 32)
        )
        let transport = NamespaceControlTransport(record: context.record, helperKey: helper)
        let clock = LockedDiagnosticsClock(
            Date(timeIntervalSince1970: 1_700_000_000)
        )
        let controller = DiagnosticsPairingController(
            credentialStore: context.store,
            transportFactory: { _, _, _ in transport },
            now: { clock.value() },
            continuousNow: { clock.continuousValue() }
        )
        controller.refresh()
        await controller.checkCapability(recordID: context.record.id)
        #expect(controller.capabilityStates[context.record.id] == .available)

        clock.advance(by: 120)
        await controller.requestNamespaceEnablement(recordID: context.record.id)
        #expect(controller.lastError == .unavailable)
        #expect(controller.capabilityStates[context.record.id] == .unavailable)
        #expect(controller.records.first?.state == .active)
        let paths = await transport.observedPaths()
        #expect(paths == [DiagnosticsCapabilityProtocol.path])
    }

    @Test("Old or invalid helper capability remains honestly unavailable or unsupported")
    @MainActor
    func capabilityFailureStates() async throws {
        let identifier = UUID().uuidString.lowercased()
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-capability-\(identifier)", isDirectory: true)
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)",
            keychain: InMemoryDiagnosticsKeychain()
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
        }
        let credential = try store.installationCredential()
        let record = makeRuntimeRecord(
            appSeed: credential.privateKey.rawRepresentation,
            helperPublic: Curve25519.Signing.PrivateKey().publicKey.rawRepresentation,
            homeserver: Data(repeating: 0x11, count: 32),
            folder: Data(repeating: 0x12, count: 32)
        )
        try store.save(record)
        let now = Date(timeIntervalSince1970: 1_700_000_000)

        let unavailable = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in FailingDiagnosticsTransport(error: .unavailable) },
            now: { now }
        )
        unavailable.refresh()
        await unavailable.checkCapability(recordID: record.id)
        #expect(unavailable.capabilityStates[record.id] == .unavailable)
        #expect(unavailable.lastError == .unavailable)
        #expect(unavailable.records.first?.state == .active)

        let unsupported = DiagnosticsPairingController(
            credentialStore: store,
            transportFactory: { _, _, _ in FailingDiagnosticsTransport(error: .invalidMessage) },
            now: { now }
        )
        unsupported.refresh()
        await unsupported.checkCapability(recordID: record.id)
        #expect(unsupported.capabilityStates[record.id] == .unsupported)
        #expect(unsupported.lastError == .invalidMessage)
        #expect(unsupported.records.first?.state == .active)
    }

    @Test("Existing-user inspection is non-mutating and explicit credentials are device-only")
    @MainActor
    func keychainAndExistingUserBoundary() throws {
        let identifier = UUID().uuidString.lowercased()
        let service = "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)"
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-keychain-\(identifier)", isDirectory: true)
        let keychain = InMemoryDiagnosticsKeychain()
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: service,
            keychain: keychain
        )
        defer {
            try? store.resetForExplicitRepair()
            try? FileManager.default.removeItem(at: support)
        }
        try? store.resetForExplicitRepair()

        let initial = try store.inspection()
        #expect(!initial.hasMarker)
        #expect(!initial.hasCredential)
        #expect(initial.records.isEmpty)
        #expect(!FileManager.default.fileExists(atPath: support.path))
        #expect(keychain.attributes(account: "installation-key-v1") == nil)

        let orphanRecord = makeRuntimeRecord(
            appSeed: Data(repeating: 0x31, count: 32),
            helperPublic: Curve25519.Signing.PrivateKey().publicKey.rawRepresentation,
            homeserver: Data(repeating: 0x11, count: 32),
            folder: Data(repeating: 0x12, count: 32)
        )
        try store.save(orphanRecord)
        expectDiagnosticsError(.recoveryRequired) {
            _ = try store.inspection()
        }
        try store.delete(orphanRecord)

        let first = try store.installationCredential()
        let second = try store.installationCredential()
        #expect(first.privateKey.rawRepresentation == second.privateKey.rawRepresentation)
        #expect(first.markerDigest == second.markerDigest)
        let attributes = try #require(keychain.attributes(account: "installation-key-v1"))
        #expect(
            attributes[kSecAttrAccessible as String] as? String
                == kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String
        )
        #expect((attributes[kSecAttrSynchronizable as String] as? Bool) != true)
        #expect(attributes[kSecAttrAccessGroup as String] == nil)

        let storedRecord = makeRuntimeRecord(
            appSeed: first.privateKey.rawRepresentation,
            helperPublic: Curve25519.Signing.PrivateKey().publicKey.rawRepresentation,
            homeserver: Data(repeating: 0x11, count: 32),
            folder: Data(repeating: 0x12, count: 32)
        )
        try store.save(storedRecord)
        let populated = try store.inspection()
        #expect(populated.hasMarker)
        #expect(populated.hasCredential)
        #expect(populated.records.map(\.id) == [storedRecord.id])

        let controller = DiagnosticsPairingController(credentialStore: store)
        controller.refresh()
        #expect(controller.records.map(\.id) == [storedRecord.id])
        let lostCredentialQuery: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "installation-key-v1",
            kSecAttrSynchronizable as String: kCFBooleanFalse!,
        ]
        #expect(keychain.delete(lostCredentialQuery as CFDictionary) == errSecSuccess)
        controller.refresh()
        #expect(controller.records.isEmpty)
        #expect(!controller.hasInstallationMarker)
        #expect(!controller.hasInstallationCredential)
        #expect(controller.notice == .recoveryRequired)
        #expect(controller.lastError == .recoveryRequired)

        try store.resetForExplicitRepair()
        let reset = try store.inspection()
        #expect(!reset.hasMarker)
        #expect(!reset.hasCredential)
        #expect(reset.records.isEmpty)
    }

    @Test("Syncthing bindings and endpoint literals fail closed")
    func targetBindingValidation() throws {
        let deviceID = "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2"
        #expect(try DiagnosticsSyncthingBinding.rawDeviceID(deviceID).count == 32)
        expectDiagnosticsError(.unsupported) {
            _ = try DiagnosticsSyncthingBinding.rawDeviceID(String(deviceID.dropLast()) + "3")
        }
        expectDiagnosticsError(.unsupported) {
            _ = try DiagnosticsSyncthingBinding.rawDeviceID(deviceID.lowercased())
        }
        expectDiagnosticsError(.unsupported) {
            _ = try DiagnosticsSyncthingBinding.rawDeviceID(deviceID.replacingOccurrences(of: "-", with: ""))
        }
        #expect(DiagnosticsPinnedTransport.isCanonicalIPAddress("127.0.0.1"))
        #expect(!DiagnosticsPinnedTransport.isCanonicalIPAddress("127.000.000.001"))
        #expect(DiagnosticsPinnedTransport.isCanonicalIPAddress("2001:db8::1"))
        #expect(!DiagnosticsPinnedTransport.isCanonicalIPAddress("2001:0db8::1"))
    }

    @Test("Pinned transport accepts only the four fixed M3 control paths")
    func fixedTransportPaths() async throws {
        let transport = try DiagnosticsPinnedTransport(
            host: "127.0.0.1",
            port: 8443,
            pin: Data(repeating: 0x11, count: 32)
        )
        for path in [
            "/api/v1/diagnostics/../pairing",
            "/api/v1/diagnostics/pairing?record=1",
            "/api/v1/diagnostics/artifact",
            "/api/v1/diagnostics/namespace/authorization/extra",
        ] {
            do {
                _ = try await transport.post(path: path, body: Data([0xa0]), responseBody: true)
                Issue.record("Unexpectedly accepted non-M3 diagnostics path")
            } catch let error as DiagnosticsProtocolError {
                #expect(error == .invalidMessage)
            } catch {
                Issue.record("Unexpected error: \(error)")
            }
        }
    }

    @Test("Namespace reads are descriptor-confined through the authorization-epoch depth")
    func namespaceFileConfinement() throws {
        let root = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-reader-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let epochDirectory = root
            .appendingPathComponent(DiagnosticsNamespaceProtocol.rootName, isDirectory: true)
            .appendingPathComponent("installations", isDirectory: true)
            .appendingPathComponent("fixture-installation", isDirectory: true)
            .appendingPathComponent("authorization-epochs", isDirectory: true)
        try FileManager.default.createDirectory(at: epochDirectory, withIntermediateDirectories: true)
        let epochFile = epochDirectory.appendingPathComponent("2.authorization.cbor")
        let expected = Data([0xa1, 0x01, 0x01])
        try expected.write(to: epochFile, options: .atomic)
        #expect(
            try DiagnosticsNamespaceFileReader.read(
                folderPath: root.path,
                components: [
                    DiagnosticsNamespaceProtocol.rootName,
                    "installations",
                    "fixture-installation",
                    "authorization-epochs",
                    "2.authorization.cbor",
                ]
            ) == expected
        )

        expectDiagnosticsError(.unsupported) {
            _ = try DiagnosticsNamespaceFileReader.read(
                folderPath: root.path,
                components: [DiagnosticsNamespaceProtocol.rootName, "..", "outside"]
            )
        }
        let namespace = root.appendingPathComponent(DiagnosticsNamespaceProtocol.rootName, isDirectory: true)
        let symlink = namespace.appendingPathComponent("linked.cbor")
        try FileManager.default.createSymbolicLink(at: symlink, withDestinationURL: epochFile)
        expectDiagnosticsError(.unavailable) {
            _ = try DiagnosticsNamespaceFileReader.read(
                folderPath: root.path,
                components: [DiagnosticsNamespaceProtocol.rootName, "linked.cbor"]
            )
        }

        let hardlink = epochDirectory.appendingPathComponent("hardlink.cbor")
        try FileManager.default.linkItem(at: epochFile, to: hardlink)
        expectDiagnosticsError(.conflict) {
            _ = try DiagnosticsNamespaceFileReader.read(
                folderPath: root.path,
                components: [
                    DiagnosticsNamespaceProtocol.rootName,
                    "installations",
                    "fixture-installation",
                    "authorization-epochs",
                    "2.authorization.cbor",
                ]
            )
        }

        let oversized = namespace.appendingPathComponent("oversized.cbor")
        try Data(repeating: 0, count: DiagnosticsDeterministicCBOR.maximumMessageBytes + 1)
            .write(to: oversized, options: .atomic)
        expectDiagnosticsError(.conflict) {
            _ = try DiagnosticsNamespaceFileReader.read(
                folderPath: root.path,
                components: [DiagnosticsNamespaceProtocol.rootName, "oversized.cbor"]
            )
        }
    }
}

private final class DiagnosticsAppRuntimeFixtureToken {}

private final class LockedDiagnosticsClock: @unchecked Sendable {
    private let lock = NSLock()
    private var date: Date
    private var continuous: TimeInterval

    init(_ date: Date, continuous: TimeInterval = 1_000) {
        self.date = date
        self.continuous = continuous
    }

    func value() -> Date {
        lock.lock()
        defer { lock.unlock() }
        return date
    }

    func advance(by interval: TimeInterval) {
        lock.lock()
        date = date.addingTimeInterval(interval)
        continuous += interval
        lock.unlock()
    }

    func continuousValue() -> TimeInterval {
        lock.lock()
        defer { lock.unlock() }
        return continuous
    }

    func advanceContinuous(by interval: TimeInterval) {
        lock.lock()
        continuous += interval
        lock.unlock()
    }

    func shiftWall(by interval: TimeInterval) {
        lock.lock()
        date = date.addingTimeInterval(interval)
        lock.unlock()
    }
}

private func loadDiagnosticsHexFixture(
    named name: String,
    filePath: StaticString = #filePath
) throws -> [String: String] {
    let bundle = Bundle(for: DiagnosticsAppRuntimeFixtureToken.self)
    let bundled = bundle.url(forResource: name, withExtension: "json")
    let fallback = URL(fileURLWithPath: "\(filePath)")
        .deletingLastPathComponent()
        .appendingPathComponent("Fixtures", isDirectory: true)
        .appendingPathComponent("\(name).json")
    return try JSONDecoder().decode([String: String].self, from: Data(contentsOf: bundled ?? fallback))
}

private func makeRuntimeRecord(
    appSeed: Data,
    helperPublic: Data,
    homeserver: Data,
    folder: Data,
    folderID: String = "folder-alpha",
    appEpoch: UInt64 = 1,
    helperEpoch: UInt64 = 1
) -> DiagnosticsPairingRecord {
    let appKey = try! Curve25519.Signing.PrivateKey(rawRepresentation: appSeed)
    let appPublic = appKey.publicKey.rawRepresentation
    let appID = DiagnosticsCrypto.keyID(publicKey: appPublic)
    let helperID = DiagnosticsCrypto.keyID(publicKey: helperPublic)
    return DiagnosticsPairingRecord(
        id: DiagnosticsPairingRecord.identifier(appKeyID: appID, folderBinding: folder),
        homeserverDeviceID: "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2",
        folderID: folderID,
        endpointHost: "127.0.0.1",
        endpointPort: 443,
        tlsSPKIPin: Data(repeating: 0x55, count: 32),
        helperPublicKey: helperPublic,
        helperKeyID: helperID,
        homeserverBinding: homeserver,
        folderBinding: folder,
        appSeed: appSeed,
        appPublicKey: appPublic,
        appKeyID: appID,
        appEpoch: appEpoch,
        helperEpoch: helperEpoch,
        currentCredentialStateDigest: Data(repeating: 0x25, count: 32),
        state: .active,
        hardExpiry: 1_800_000_000,
        localDeadline: nil,
        lastOutgoing: Data([0xa0]),
        lastIncoming: nil,
        transcriptFingerprint: nil,
        namespaceID: nil,
        namespaceInitialAppKeyID: nil,
        namespaceEnablement: nil,
        namespaceRootDigest: nil,
        namespaceManifestDigest: nil,
        namespaceManifestEpoch: nil,
        namespaceAuthorizationDigest: nil,
        namespaceAuthorizationEpoch: 0,
        pendingLifecycle: nil
    )
}

private func makeCapabilityResponse(
    query: DiagnosticsCapabilityProtocol.Query,
    record: DiagnosticsPairingRecord,
    helperKey: Curve25519.Signing.PrivateKey,
    flags: UInt64,
    issuedAt: UInt64
) throws -> Data {
    let body = DiagnosticsCBORValue.map([
        DiagnosticsCBORField(label: 1, value: .text(DiagnosticsCapabilityProtocol.capability)),
        DiagnosticsCBORField(label: 2, value: .unsigned(1)),
        DiagnosticsCBORField(label: 3, value: .unsigned(1)),
        DiagnosticsCBORField(label: 4, value: .unsigned(2)),
        DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
        DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
        DiagnosticsCBORField(label: 7, value: .bytes(record.appKeyID)),
        DiagnosticsCBORField(label: 8, value: .bytes(record.helperKeyID)),
        DiagnosticsCBORField(label: 9, value: .unsigned(record.appEpoch)),
        DiagnosticsCBORField(label: 10, value: .unsigned(record.helperEpoch)),
        DiagnosticsCBORField(label: 12, value: .unsigned(issuedAt)),
        DiagnosticsCBORField(label: 13, value: .unsigned(issuedAt + 120)),
        DiagnosticsCBORField(label: 27, value: .unsigned(flags)),
        DiagnosticsCBORField(label: 30, value: .bytes(query.nonce)),
        DiagnosticsCBORField(label: 31, value: .bytes(query.digest)),
    ])
    let encodedBody = try DiagnosticsDeterministicCBOR.encode(body)
    var input = Data("eu.vaultsync.roundtrip/v1/capability-response\0".utf8)
    input.append(encodedBody)
    let signature = try helperKey.signature(for: input)
    guard case .map(var fields) = body else { throw DiagnosticsProtocolError.invalidMessage }
    fields.append(DiagnosticsCBORField(label: 255, value: .bytes(signature)))
    return try DiagnosticsDeterministicCBOR.encode(.map(fields))
}

private func makeCapabilityResponseForQuery(
    _ queryData: Data,
    helperKey: Curve25519.Signing.PrivateKey
) throws -> Data {
    let query = try DiagnosticsDeterministicCBOR.decode(queryData)
    let copiedLabels: Set<UInt64> = [1, 2, 3, 5, 6, 7, 8, 9, 10, 12, 13, 30]
    guard let queryFields = query.fields,
          query.bytes(for: 30, count: 32) != nil else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    let queryBody = try DiagnosticsDeterministicCBOR.encode(query.removing(labels: [255]))
    let queryDigest = DiagnosticsCrypto.sha256(
        domain: "eu.vaultsync.roundtrip/v1/capability-query\0",
        body: queryBody
    )
    var fields = queryFields.filter { copiedLabels.contains($0.label) }
    fields.append(DiagnosticsCBORField(label: 4, value: .unsigned(2)))
    fields.append(DiagnosticsCBORField(
        label: 27,
        value: .unsigned(DiagnosticsCapabilityProtocol.requiredFlags)
    ))
    fields.append(DiagnosticsCBORField(label: 31, value: .bytes(queryDigest)))
    let body = DiagnosticsCBORValue.map(fields)
    let encodedBody = try DiagnosticsDeterministicCBOR.encode(body)
    var input = Data("eu.vaultsync.roundtrip/v1/capability-response\0".utf8)
    input.append(encodedBody)
    let signature = try helperKey.signature(for: input)
    guard case .map(var signedFields) = body else { throw DiagnosticsProtocolError.invalidMessage }
    signedFields.append(DiagnosticsCBORField(label: 255, value: .bytes(signature)))
    return try DiagnosticsDeterministicCBOR.encode(.map(signedFields))
}

private struct LifecycleTestContext {
    let store: DiagnosticsCredentialStore
    let support: URL
    let record: DiagnosticsPairingRecord

    static func make(
        label: String,
        record suppliedRecord: DiagnosticsPairingRecord? = nil
    ) throws -> LifecycleTestContext {
        let identifier = "\(label)-\(UUID().uuidString.lowercased())"
        let support = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("vaultsync-m3-lifecycle-\(identifier)", isDirectory: true)
        let store = DiagnosticsCredentialStore(
            applicationSupportURL: support,
            service: "eu.vaultsync.app.diagnostics.v1.tests.\(identifier)",
            keychain: InMemoryDiagnosticsKeychain()
        )
        let installation = try store.installationCredential()
        let record: DiagnosticsPairingRecord
        if let suppliedRecord {
            record = suppliedRecord
        } else {
            let helper = try Curve25519.Signing.PrivateKey(
                rawRepresentation: Data(repeating: 0x41, count: 32)
            )
            record = makeRuntimeRecord(
                appSeed: installation.privateKey.rawRepresentation,
                helperPublic: helper.publicKey.rawRepresentation,
                homeserver: Data(repeating: 0x11, count: 32),
                folder: Data(repeating: 0x12, count: 32)
            )
        }
        try store.save(record)
        return LifecycleTestContext(store: store, support: support, record: record)
    }

    func cleanup() {
        try? store.resetForExplicitRepair()
        try? FileManager.default.removeItem(at: support)
    }
}

private func lifecycleFixtureRecord(value: DiagnosticsCBORValue) throws -> DiagnosticsPairingRecord {
    let appSeed = Data(repeating: 0x31, count: 32)
    let helperPublic = try requiredDiagnosticsBytes(value, label: 11)
    var record = makeRuntimeRecord(
        appSeed: appSeed,
        helperPublic: helperPublic,
        homeserver: try requiredDiagnosticsBytes(value, label: 5),
        folder: try requiredDiagnosticsBytes(value, label: 6),
        appEpoch: try requiredDiagnosticsUnsigned(value, label: 17),
        helperEpoch: try requiredDiagnosticsUnsigned(value, label: 19)
    )
    guard record.appPublicKey == value.bytes(for: 7, count: 32),
          record.appKeyID == value.bytes(for: 8, count: 32),
          record.helperKeyID == value.bytes(for: 12, count: 32) else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    record.tlsSPKIPin = try requiredDiagnosticsBytes(value, label: 15)
    record.currentCredentialStateDigest = try requiredDiagnosticsBytes(value, label: 26)
    record.lastOutgoing = try DiagnosticsDeterministicCBOR.encode(value)
    return record
}

private func requiredDiagnosticsBytes(
    _ value: DiagnosticsCBORValue,
    label: UInt64
) throws -> Data {
    guard let data = value.bytes(for: label, count: 32) else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    return data
}

private func requiredDiagnosticsUnsigned(
    _ value: DiagnosticsCBORValue,
    label: UInt64
) throws -> UInt64 {
    guard let number = value.unsigned(for: label) else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    return number
}

private func makePairingInvitation(
    helperKey: Curve25519.Signing.PrivateKey,
    deviceID: String,
    folderID: String,
    issuedAt: UInt64
) throws -> Data {
    let helperPublic = helperKey.publicKey.rawRepresentation
    let value = DiagnosticsCBORValue.map([
        DiagnosticsCBORField(label: 1, value: .text(DiagnosticsPairingProtocol.capability)),
        DiagnosticsCBORField(label: 2, value: .unsigned(1)),
        DiagnosticsCBORField(label: 3, value: .unsigned(1)),
        DiagnosticsCBORField(label: 4, value: .unsigned(DiagnosticsPairingProtocol.MessageType.qr.rawValue)),
        DiagnosticsCBORField(label: 5, value: .bytes(Data(repeating: 0x05, count: 32))),
        DiagnosticsCBORField(label: 6, value: .text("helper.test")),
        DiagnosticsCBORField(label: 7, value: .unsigned(8443)),
        DiagnosticsCBORField(label: 8, value: .bytes(Data(repeating: 0x08, count: 32))),
        DiagnosticsCBORField(label: 9, value: .bytes(helperPublic)),
        DiagnosticsCBORField(label: 10, value: .bytes(DiagnosticsCrypto.keyID(publicKey: helperPublic))),
        DiagnosticsCBORField(label: 11, value: .bytes(Data(repeating: 0x11, count: 32))),
        DiagnosticsCBORField(label: 12, value: .bytes(Data(repeating: 0x12, count: 32))),
        DiagnosticsCBORField(label: 13, value: .bytes(try DiagnosticsSyncthingBinding.deviceDigest(deviceID))),
        DiagnosticsCBORField(label: 14, value: .bytes(try DiagnosticsSyncthingBinding.folderDigest(folderID))),
        DiagnosticsCBORField(label: 15, value: .unsigned(issuedAt)),
        DiagnosticsCBORField(label: 16, value: .unsigned(issuedAt + DiagnosticsPairingProtocol.maximumLifetime)),
        DiagnosticsCBORField(label: 17, value: .bytes(Data(repeating: 0x17, count: 32))),
        DiagnosticsCBORField(label: 24, value: .unsigned(1)),
    ])
    let encoded = try DiagnosticsDeterministicCBOR.encode(value)
    _ = try DiagnosticsPairingProtocol.decode(encoded)
    return encoded
}

private actor BootstrapPairingTransport: DiagnosticsTransporting {
    private let helperKey: Curve25519.Signing.PrivateKey
    private let issuedAt: UInt64
    private var receivedTypes: [DiagnosticsPairingProtocol.MessageType] = []

    init(helperKey: Curve25519.Signing.PrivateKey, issuedAt: UInt64) {
        self.helperKey = helperKey
        self.issuedAt = issuedAt
    }

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        guard path == DiagnosticsPairingProtocol.path, responseBody else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let prior = try DiagnosticsPairingProtocol.decode(body)
        receivedTypes.append(prior.type)
        let responseType: DiagnosticsPairingProtocol.MessageType
        switch prior.type {
        case .appRequest: responseType = .helperAccept
        case .finalize: responseType = .finalizeAck
        case .receipt: responseType = .readyAck
        case .activate: responseType = .activeAck
        case .abort: responseType = .abortAck
        default: throw DiagnosticsProtocolError.invalidMessage
        }
        return try makeHelperBootstrapResponse(
            prior: prior,
            responseType: responseType,
            helperKey: helperKey,
            issuedAt: issuedAt
        )
    }

    func observedTypes() -> [DiagnosticsPairingProtocol.MessageType] {
        receivedTypes
    }
}

private struct FailingDiagnosticsTransport: DiagnosticsTransporting {
    let error: DiagnosticsProtocolError

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        throw error
    }
}

private actor NamespaceControlTransport: DiagnosticsTransporting {
    private let record: DiagnosticsPairingRecord
    private let helperKey: Curve25519.Signing.PrivateKey
    private var paths: [String] = []
    private var enablement: Data?
    private var authorization: Data?

    init(record: DiagnosticsPairingRecord, helperKey: Curve25519.Signing.PrivateKey) {
        self.record = record
        self.helperKey = helperKey
    }

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        paths.append(path)
        switch path {
        case DiagnosticsCapabilityProtocol.path:
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            let value = try DiagnosticsDeterministicCBOR.decode(body)
            let unsigned = try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255]))
            guard let nonce = value.bytes(for: 30, count: 32),
                  let issuedAt = value.unsigned(for: 12) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let query = DiagnosticsCapabilityProtocol.Query(
                message: body,
                digest: DiagnosticsCrypto.sha256(
                    domain: "eu.vaultsync.roundtrip/v1/capability-query\0",
                    body: unsigned
                ),
                nonce: nonce
            )
            return try makeCapabilityResponse(
                query: query,
                record: record,
                helperKey: helperKey,
                flags: DiagnosticsCapabilityProtocol.requiredFlags,
                issuedAt: issuedAt
            )
        case DiagnosticsNamespaceProtocol.enablementPath:
            guard !responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            enablement = body
            return nil
        case DiagnosticsNamespaceProtocol.authorizationPath:
            guard !responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            authorization = body
            return nil
        default:
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    func latestEnablement() -> Data? { enablement }
    func latestAuthorization() -> Data? { authorization }
    func observedPaths() -> [String] { paths }
}

private actor LifecycleTransport: DiagnosticsTransporting {
    private let currentHelper: Curve25519.Signing.PrivateKey
    private let proposedHelper: Curve25519.Signing.PrivateKey?
    private let capabilityHelper: Curve25519.Signing.PrivateKey
    private let now: Date
    private var types: [DiagnosticsPairingProtocol.MessageType] = []

    init(
        currentHelper: Curve25519.Signing.PrivateKey,
        proposedHelper: Curve25519.Signing.PrivateKey?,
        capabilityHelper: Curve25519.Signing.PrivateKey,
        now: Date
    ) {
        self.currentHelper = currentHelper
        self.proposedHelper = proposedHelper
        self.capabilityHelper = capabilityHelper
        self.now = now
    }

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        if path == DiagnosticsCapabilityProtocol.path {
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return try makeCapabilityResponseForQuery(body, helperKey: capabilityHelper)
        }
        if path == DiagnosticsNamespaceProtocol.authorizationPath {
            guard !responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return nil
        }
        guard path == DiagnosticsPairingProtocol.path else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let message = try DiagnosticsPairingProtocol.decode(body)
        types.append(message.type)
        switch message.type {
        case .appKeyRotationRequest, .helperKeyRotationConfirm, .tlsPinRotationConfirm:
            guard !responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return nil
        case .appKeyRotationNewProof:
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: message,
                type: .appKeyRotationAccept,
                transitionKind: nil,
                transitionDigest: nil,
                signer: currentHelper,
                nonce: Data(repeating: 0x71, count: 32),
                now: now
            ).canonical
        case .lifecycleFinalize:
            guard responseBody,
                  let rawKind = message.value.unsigned(for: 29),
                  let kind = DiagnosticsPairingProtocol.TransitionKind(rawValue: rawKind),
                  let digest = message.value.bytes(for: 28, count: 32) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let signer: Curve25519.Signing.PrivateKey
            if kind == .helperKey {
                guard let proposedHelper else { throw DiagnosticsProtocolError.invalidMessage }
                signer = proposedHelper
            } else {
                signer = currentHelper
            }
            return try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: message,
                type: .lifecycleActiveAck,
                transitionKind: kind,
                transitionDigest: digest,
                signer: signer,
                nonce: Data(repeating: 0x72, count: 32),
                now: now
            ).canonical
        case .revocationRequest:
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: message,
                type: .revocationRecord,
                transitionKind: nil,
                transitionDigest: nil,
                signer: currentHelper,
                nonce: Data(repeating: 0x73, count: 32),
                now: now
            ).canonical
        default:
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    func observedTypes() -> [DiagnosticsPairingProtocol.MessageType] { types }
}

private actor AbortLifecycleTransport: DiagnosticsTransporting {
    private let currentHelper: Curve25519.Signing.PrivateKey
    private let now: Date
    private var failProofOnce = true
    private var types: [DiagnosticsPairingProtocol.MessageType] = []

    init(currentHelper: Curve25519.Signing.PrivateKey, now: Date) {
        self.currentHelper = currentHelper
        self.now = now
    }

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        guard path == DiagnosticsPairingProtocol.path else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let message = try DiagnosticsPairingProtocol.decode(body)
        types.append(message.type)
        switch message.type {
        case .appKeyRotationRequest:
            guard !responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            return nil
        case .appKeyRotationNewProof:
            guard responseBody else { throw DiagnosticsProtocolError.invalidMessage }
            if failProofOnce {
                failProofOnce = false
                throw DiagnosticsProtocolError.unavailable
            }
            return try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: message,
                type: .appKeyRotationAccept,
                transitionKind: nil,
                transitionDigest: nil,
                signer: currentHelper,
                nonce: Data(repeating: 0x74, count: 32),
                now: now
            ).canonical
        case .lifecycleAbort:
            guard responseBody,
                  let rawKind = message.value.unsigned(for: 29),
                  let kind = DiagnosticsPairingProtocol.TransitionKind(rawValue: rawKind),
                  let digest = message.value.bytes(for: 28, count: 32) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            return try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: message,
                type: .lifecycleAbortAck,
                transitionKind: kind,
                transitionDigest: digest,
                signer: currentHelper,
                nonce: Data(repeating: 0x75, count: 32),
                now: now
            ).canonical
        default:
            throw DiagnosticsProtocolError.invalidMessage
        }
    }

    func observedTypes() -> [DiagnosticsPairingProtocol.MessageType] { types }
}

private func makeHelperBootstrapResponse(
    prior: DiagnosticsPairingProtocol.Message,
    responseType: DiagnosticsPairingProtocol.MessageType,
    helperKey: Curve25519.Signing.PrivateKey,
    issuedAt: UInt64
) throws -> Data {
    guard let priorFields = prior.value.fields else { throw DiagnosticsProtocolError.invalidMessage }
    let copiedLabels: Set<UInt64>
    if responseType == .helperAccept {
        copiedLabels = [1, 2, 3, 5, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 23, 24]
    } else {
        copiedLabels = [1, 2, 3, 5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 22, 23, 24, 25]
    }
    var fields = priorFields.filter { copiedLabels.contains($0.label) }
    fields.append(DiagnosticsCBORField(label: 4, value: .unsigned(responseType.rawValue)))
    if responseType == .helperAccept {
        fields.append(DiagnosticsCBORField(label: 22, value: .bytes(try prior.digest())))
        fields.append(DiagnosticsCBORField(label: 25, value: .bytes(Data(repeating: 0x25, count: 32))))
    } else {
        fields.append(DiagnosticsCBORField(label: 15, value: .unsigned(issuedAt)))
        fields.append(DiagnosticsCBORField(
            label: 16,
            value: .unsigned(issuedAt + DiagnosticsPairingProtocol.maximumLifetime)
        ))
        fields.append(DiagnosticsCBORField(label: 26, value: .bytes(try prior.digest())))
    }
    let unsigned = DiagnosticsCBORValue.map(fields)
    let body = try DiagnosticsDeterministicCBOR.encode(unsigned)
    let domain = try pairingDomainForTest(responseType)
    var input = Data(domain.utf8)
    input.append(body)
    let signature = try helperKey.signature(for: input)
    guard case .map(var signedFields) = unsigned else { throw DiagnosticsProtocolError.invalidMessage }
    signedFields.append(DiagnosticsCBORField(label: 255, value: .bytes(signature)))
    let encoded = try DiagnosticsDeterministicCBOR.encode(.map(signedFields))
    _ = try DiagnosticsPairingProtocol.decode(encoded)
    return encoded
}

private func pairingDomainForTest(_ type: DiagnosticsPairingProtocol.MessageType) throws -> String {
    let suffix: String
    switch type {
    case .helperAccept: suffix = "helper-accept"
    case .finalizeAck: suffix = "pairing-finalize-ack"
    case .readyAck: suffix = "pairing-ready-ack"
    case .activeAck: suffix = "pairing-active-ack"
    case .abortAck: suffix = "pairing-abort-ack"
    default: throw DiagnosticsProtocolError.invalidMessage
    }
    return "eu.vaultsync.helper-pairing/v1/\(suffix)\0"
}

private func makeNamespaceRoot(
    enablement: Data,
    record: DiagnosticsPairingRecord,
    helperKey: Curve25519.Signing.PrivateKey,
    readmeDigest: Data,
    createdAt: UInt64
) throws -> Data {
    let enablementValue = try DiagnosticsDeterministicCBOR.decode(enablement)
    guard let nonce = enablementValue.bytes(for: 19, count: 32) else {
        throw DiagnosticsProtocolError.invalidMessage
    }
    let body = DiagnosticsCBORValue.map([
        DiagnosticsCBORField(label: 1, value: .text(DiagnosticsNamespaceProtocol.capability)),
        DiagnosticsCBORField(label: 2, value: .unsigned(1)),
        DiagnosticsCBORField(label: 3, value: .unsigned(1)),
        DiagnosticsCBORField(label: 4, value: .unsigned(2)),
        DiagnosticsCBORField(label: 5, value: .bytes(record.homeserverBinding)),
        DiagnosticsCBORField(label: 6, value: .bytes(record.folderBinding)),
        DiagnosticsCBORField(label: 7, value: .bytes(Data(repeating: 0x07, count: 32))),
        DiagnosticsCBORField(label: 13, value: .bytes(record.helperPublicKey)),
        DiagnosticsCBORField(label: 14, value: .bytes(record.helperKeyID)),
        DiagnosticsCBORField(label: 15, value: .unsigned(record.helperEpoch)),
        DiagnosticsCBORField(label: 19, value: .bytes(nonce)),
        DiagnosticsCBORField(label: 20, value: .bytes(DiagnosticsNamespaceProtocol.recordDigest(enablement))),
        DiagnosticsCBORField(label: 28, value: .unsigned(createdAt)),
        DiagnosticsCBORField(label: 29, value: .bytes(readmeDigest)),
    ])
    return try signNamespaceMessage(
        body,
        signatureLabel: 255,
        domain: "eu.vaultsync.namespace/v1/root-manifest\0",
        key: helperKey
    )
}

private func countersignInitialAuthorization(
    _ candidate: Data,
    helperKey: Curve25519.Signing.PrivateKey
) throws -> Data {
    let value = try DiagnosticsDeterministicCBOR.decode(candidate)
    return try signNamespaceMessage(
        value,
        signatureLabel: 255,
        domain: "eu.vaultsync.namespace/v1/authorization-initial-helper\0",
        key: helperKey
    )
}

private func signNamespaceMessage(
    _ value: DiagnosticsCBORValue,
    signatureLabel: UInt64,
    domain: String,
    key: Curve25519.Signing.PrivateKey
) throws -> Data {
    let body = try DiagnosticsDeterministicCBOR.encode(value)
    var input = Data(domain.utf8)
    input.append(body)
    let signature = try key.signature(for: input)
    guard case .map(var fields) = value else { throw DiagnosticsProtocolError.invalidMessage }
    fields.append(DiagnosticsCBORField(label: signatureLabel, value: .bytes(signature)))
    return try DiagnosticsDeterministicCBOR.encode(.map(fields))
}

private func removingSignatures(_ encoded: Data, labels: Set<UInt64>) throws -> Data {
    let value = try DiagnosticsDeterministicCBOR.decode(encoded)
    return try DiagnosticsDeterministicCBOR.encode(value.removing(labels: labels))
}

private final class InMemoryDiagnosticsKeychain: DiagnosticsKeychainAccess, @unchecked Sendable {
    private var items: [String: [String: Any]] = [:]

    func attributes(account: String) -> [String: Any]? {
        items[account]
    }

    func copyMatching(_ query: CFDictionary, result: inout AnyObject?) -> OSStatus {
        let values = dictionary(query)
        if let account = values[kSecAttrAccount as String] as? String {
            guard let item = items[account] else { return errSecItemNotFound }
            if values[kSecReturnAttributes as String] as? Bool == true {
                result = item as NSDictionary
            } else {
                result = item[kSecValueData as String] as AnyObject?
            }
            return errSecSuccess
        }
        guard !items.isEmpty else { return errSecItemNotFound }
        result = Array(items.values) as NSArray
        return errSecSuccess
    }

    func update(_ query: CFDictionary, attributes: CFDictionary) -> OSStatus {
        let identity = dictionary(query)
        guard let account = identity[kSecAttrAccount as String] as? String,
              var item = items[account] else { return errSecItemNotFound }
        for (key, value) in dictionary(attributes) {
            item[key] = value
        }
        items[account] = item
        return errSecSuccess
    }

    func add(_ attributes: CFDictionary) -> OSStatus {
        let item = dictionary(attributes)
        guard let account = item[kSecAttrAccount as String] as? String else { return errSecParam }
        guard items[account] == nil else { return errSecDuplicateItem }
        items[account] = item
        return errSecSuccess
    }

    func delete(_ query: CFDictionary) -> OSStatus {
        let values = dictionary(query)
        if let account = values[kSecAttrAccount as String] as? String {
            return items.removeValue(forKey: account) == nil ? errSecItemNotFound : errSecSuccess
        }
        guard !items.isEmpty else { return errSecItemNotFound }
        items.removeAll()
        return errSecSuccess
    }

    private func dictionary(_ value: CFDictionary) -> [String: Any] {
        value as NSDictionary as? [String: Any] ?? [:]
    }
}

private func expectDiagnosticsError(
    _ expected: DiagnosticsProtocolError,
    _ operation: () throws -> Void
) {
    do {
        try operation()
        Issue.record("Expected diagnostics error \(expected)")
    } catch let error as DiagnosticsProtocolError {
        #expect(error == expected)
    } catch {
        Issue.record("Unexpected error: \(error)")
    }
}

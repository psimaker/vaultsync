import CryptoKit
import Darwin
import Foundation
import Observation

@MainActor
@Observable
final class DiagnosticsPairingController {
    enum CapabilityState: String, Sendable {
        case notChecked
        case checking
        case available
        case unavailable
        case unsupported
    }

    enum Notice: Equatable, Sendable {
        case none
        case fingerprint(recordID: String, value: String)
        case pairingActive(recordID: String)
        case operatorNamespaceAction(recordID: String)
        case namespaceAuthorizationPending(recordID: String)
        case namespaceActive(recordID: String)
        case recoveryRequired
    }

    enum UploadPhase: String, Equatable, Sendable {
        case preflighting
        case checking
        case uploadObserved
        case cancelled
        case timedOut
        case interrupted
        case conflict
        case rateLimited
        case unsupported
        case unavailable
    }

    struct UploadEvidence: Equatable, Sendable {
        var uploadObserved = false
        let downloadObserved = false
        let roundtripConfirmed = false
    }

    struct UploadStatus: Equatable, Sendable {
        var phase: UploadPhase
        var evidence = UploadEvidence()
        var completedPolls = 0
    }

    private struct UploadTuple: Hashable, Sendable {
        let recordID: String
        let homeserverDeviceID: String
        let folderID: String
        let homeserverBinding: Data
        let folderBinding: Data
        let appKeyID: Data
        let helperKeyID: Data
        let appEpoch: UInt64
        let helperEpoch: UInt64
        let namespaceID: Data?
        let namespaceAuthorizationDigest: Data?
        let namespaceAuthorizationEpoch: UInt64
    }

    typealias TransportFactory = @Sendable (String, UInt16, Data) throws -> any DiagnosticsTransporting
    typealias UploadPreflightProvider = @MainActor (
        _ installationComponent: String,
        _ operationComponent: String,
        _ requireEmptySlot: Bool
    ) -> DiagnosticsUploadPreflight
    typealias UploadRescan = @MainActor () -> Bool

    private let credentialStore: DiagnosticsCredentialStore
    private let transportFactory: TransportFactory
    private let now: @Sendable () -> Date
    private let continuousNow: @Sendable () -> TimeInterval
    private let uploadRandomBytes: @Sendable (Int) throws -> Data
    private let uploadFileWriter: @Sendable (String, [String], Data) throws -> Void
    private let uploadSleep: @Sendable (UInt64) async throws -> Void
    private var capabilityValidUntil: [String: TimeInterval] = [:]
    private var uploadTasks: [String: Task<Void, Never>] = [:]
    private var uploadRunIDs: [String: UUID] = [:]
    private var activeUploadTuples: [UploadTuple: UUID] = [:]
    private var uploadStartsByRecord: [String: [TimeInterval]] = [:]
    private var uploadRequestsByRecord: [String: [TimeInterval]] = [:]
    private var uploadRequests: [TimeInterval] = []

    private(set) var records: [DiagnosticsPairingRecord] = []
    private(set) var capabilityStates: [String: CapabilityState] = [:]
    private(set) var uploadStatuses: [String: UploadStatus] = [:]
    private(set) var notice: Notice = .none
    private(set) var lastError: DiagnosticsProtocolError?
    private(set) var isBusy = false
    private(set) var hasInstallationMarker = false
    private(set) var hasInstallationCredential = false

    init(
        credentialStore: DiagnosticsCredentialStore = DiagnosticsCredentialStore(),
        transportFactory: @escaping TransportFactory = { host, port, pin in
            try DiagnosticsPinnedTransport(host: host, port: port, pin: pin)
        },
        now: @escaping @Sendable () -> Date = Date.init,
        continuousNow: @escaping @Sendable () -> TimeInterval = DiagnosticsContinuousClock.seconds,
        uploadRandomBytes: @escaping @Sendable (Int) throws -> Data = DiagnosticsCrypto.randomBytes,
        uploadFileWriter: @escaping @Sendable (String, [String], Data) throws -> Void = {
            try DiagnosticsUploadFileStore.createImmutable(folderPath: $0, components: $1, data: $2)
        },
        uploadSleep: @escaping @Sendable (UInt64) async throws -> Void = {
            try await ContinuousClock().sleep(for: .seconds(Int64($0)))
        }
    ) {
        self.credentialStore = credentialStore
        self.transportFactory = transportFactory
        self.now = now
        self.continuousNow = continuousNow
        self.uploadRandomBytes = uploadRandomBytes
        self.uploadFileWriter = uploadFileWriter
        self.uploadSleep = uploadSleep
    }

    func refresh() {
        uploadTasks.values.forEach { $0.cancel() }
        uploadTasks = [:]
        uploadRunIDs = [:]
        activeUploadTuples = [:]
        uploadStatuses = [:]
        do {
            let inspection = try credentialStore.inspection()
            hasInstallationMarker = inspection.hasMarker
            hasInstallationCredential = inspection.hasCredential
            records = inspection.records
            capabilityStates = [:]
            capabilityValidUntil = [:]
            if inspection.hasMarker != inspection.hasCredential {
                notice = .recoveryRequired
            }
            lastError = nil
        } catch let error as DiagnosticsProtocolError {
            records = []
            capabilityStates = [:]
            capabilityValidUntil = [:]
            hasInstallationMarker = false
            hasInstallationCredential = false
            lastError = error
            if error == .recoveryRequired { notice = .recoveryRequired }
        } catch {
            records = []
            capabilityStates = [:]
            capabilityValidUntil = [:]
            hasInstallationMarker = false
            hasInstallationCredential = false
            lastError = .unavailable
        }
    }

    func beginPairing(qr: String, homeserverDeviceID: String, folderID: String) async {
        await perform {
            let invitation = try DiagnosticsPairingProtocol.decodeQR(qr, now: now())
            let credential = try credentialStore.installationCredential()
            let appKey = credential.privateKey
            let request = try DiagnosticsPairingProtocol.makeAppRequest(
                invitation: invitation,
                appPrivateKey: appKey,
                selectedDeviceID: homeserverDeviceID,
                selectedFolderID: folderID,
                appNonce: try DiagnosticsCrypto.randomBytes(count: 32)
            )
            guard let endpointHost = invitation.value.text(for: 6),
                  let endpointPort = invitation.value.unsigned(for: 7), endpointPort <= UInt16.max,
                  let tlsPin = invitation.value.bytes(for: 8, count: 32),
                  let helperPublic = invitation.value.bytes(for: 9, count: 32),
                  let helperKeyID = invitation.value.bytes(for: 10, count: 32),
                  let homeserverBinding = invitation.value.bytes(for: 11, count: 32),
                  let folderBinding = invitation.value.bytes(for: 12, count: 32),
                  let hardExpiry = invitation.value.unsigned(for: 16) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let appPublic = appKey.publicKey.rawRepresentation
            let appKeyID = DiagnosticsCrypto.keyID(publicKey: appPublic)
            let identifier = DiagnosticsPairingRecord.identifier(appKeyID: appKeyID, folderBinding: folderBinding)
            guard !records.contains(where: {
                $0.homeserverDeviceID == homeserverDeviceID && $0.folderID == folderID && $0.state != .revoked
            }) else {
                throw DiagnosticsProtocolError.conflict
            }
            var record = DiagnosticsPairingRecord(
                id: identifier,
                homeserverDeviceID: homeserverDeviceID,
                folderID: folderID,
                endpointHost: endpointHost,
                endpointPort: UInt16(endpointPort),
                tlsSPKIPin: tlsPin,
                helperPublicKey: helperPublic,
                helperKeyID: helperKeyID,
                homeserverBinding: homeserverBinding,
                folderBinding: folderBinding,
                appSeed: appKey.rawRepresentation,
                appPublicKey: appPublic,
                appKeyID: appKeyID,
                appEpoch: 1,
                helperEpoch: invitation.value.unsigned(for: 24)!,
                currentCredentialStateDigest: try request.digest(),
                state: .requestPrepared,
                hardExpiry: hardExpiry,
                localDeadline: try makeLocalDeadline(expiresAt: hardExpiry),
                lastOutgoing: request.canonical,
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
            try persist(record)
            try await sendPreparedBootstrap(&record)
        }
    }

    func retryPreparedPairing(recordID: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            try requireLocalDeadline(record)
            switch record.state {
            case .requestPrepared, .finalizePrepared, .receiptPrepared, .activatePrepared:
                try await sendPreparedBootstrap(&record)
            default:
                throw DiagnosticsProtocolError.conflict
            }
        }
    }

    func confirmFingerprintAndActivate(recordID: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            try requireLocalDeadline(record)
            guard record.state == .acceptanceReceived ||
                    record.state == .finalizeAcknowledged ||
                    record.state == .readyAcknowledged else {
                throw DiagnosticsProtocolError.conflict
            }
            while record.state != .active {
                switch record.state {
                case .acceptanceReceived:
                    try prepareBootstrap(&record, type: .finalize, preparedState: .finalizePrepared)
                case .finalizeAcknowledged:
                    try prepareBootstrap(&record, type: .receipt, preparedState: .receiptPrepared)
                case .readyAcknowledged:
                    try prepareBootstrap(&record, type: .activate, preparedState: .activatePrepared)
                default:
                    throw DiagnosticsProtocolError.conflict
                }
                try await sendPreparedBootstrap(&record)
            }
            notice = .pairingActive(recordID: record.id)
        }
    }

    func cancelPendingPairing(recordID: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            try requireLocalDeadline(record)
            switch record.state {
            case .requestPrepared, .finalizePrepared, .receiptPrepared:
                try await sendPreparedBootstrap(&record)
            case .acceptanceReceived, .finalizeAcknowledged, .readyAcknowledged:
                break
            case .abortPrepared:
                try await sendPreparedAbort(&record)
                return
            default:
                // Once type 7 may have reached the helper, only authenticated
                // revocation can safely retire the authorization.
                throw DiagnosticsProtocolError.conflict
            }
            guard let incoming = record.lastIncoming else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let prior = try DiagnosticsPairingProtocol.decode(incoming)
            let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let abort = try DiagnosticsPairingProtocol.makeBootstrapTransition(
                prior: prior,
                type: .abort,
                appPrivateKey: key,
                now: now(),
                hardExpiry: record.hardExpiry
            )
            record.lastOutgoing = abort.canonical
            record.state = .abortPrepared
            try persist(record)
            try await sendPreparedAbort(&record)
        }
    }

    func discardExpiredPendingPairing(recordID: String) {
        do {
            let record = try requiredRecord(recordID)
            guard canDiscardExpiredPairing(record) else {
                throw DiagnosticsProtocolError.conflict
            }
            try credentialStore.delete(record)
            records.removeAll { $0.id == record.id }
            capabilityStates.removeValue(forKey: record.id)
            capabilityValidUntil.removeValue(forKey: record.id)
            notice = .none
            lastError = nil
        } catch let error as DiagnosticsProtocolError {
            lastError = error
        } catch {
            lastError = .unavailable
        }
    }

    func canDiscardExpiredPairing(_ record: DiagnosticsPairingRecord) -> Bool {
        let states: Set<DiagnosticsPairingRecord.State> = [
            .requestPrepared, .acceptanceReceived, .finalizePrepared,
            .finalizeAcknowledged, .receiptPrepared, .readyAcknowledged,
            .abortPrepared,
        ]
        guard states.contains(record.state),
              record.hardExpiry <= UInt64.max - DiagnosticsPairingProtocol.maximumClockSkew else {
            return false
        }
        if let deadline = record.localDeadline, localDeadlineExpired(deadline) {
            return true
        }
        let seconds = now().timeIntervalSince1970.rounded(.down)
        guard seconds >= 0, seconds < Double(UInt64.max) else { return false }
        return UInt64(seconds) > record.hardExpiry + DiagnosticsPairingProtocol.maximumClockSkew
    }

    func checkCapability(recordID: String) async {
        guard !isBusy else { return }
        capabilityValidUntil.removeValue(forKey: recordID)
        capabilityStates[recordID] = .checking
        await perform {
            let record = try requiredActiveRecord(recordID)
            let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let query = try DiagnosticsCapabilityProtocol.makeQuery(
                record: record,
                appKey: key,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            let transport = try makeTransport(record)
            do {
                guard let response = try await transport.post(
                    path: DiagnosticsCapabilityProtocol.path,
                    body: query.message,
                    responseBody: true
                ) else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let expires = try DiagnosticsCapabilityProtocol.validateResponse(
                    response,
                    query: query,
                    record: record,
                    now: now()
                )
                capabilityValidUntil[recordID] = try makeContinuousDeadline(
                    expiresAt: expires,
                    maximumLifetime: 120
                )
                capabilityStates[recordID] = .available
            } catch let error as DiagnosticsProtocolError {
                capabilityValidUntil.removeValue(forKey: recordID)
                switch error {
                case .invalidMessage, .unsupported, .conflict:
                    capabilityStates[recordID] = .unsupported
                default:
                    capabilityStates[recordID] = .unavailable
                }
                throw error
            }
        }
        if capabilityStates[recordID] == .checking {
            capabilityStates[recordID] = .unavailable
        }
    }

    func beginForegroundUpload(
        recordID: String,
        preflight: @escaping UploadPreflightProvider,
        rescan: @escaping UploadRescan
    ) {
        guard uploadTasks[recordID] == nil else { return }
        lastError = nil
        uploadStatuses[recordID] = UploadStatus(phase: .preflighting)
        let runID = UUID()
        uploadRunIDs[recordID] = runID
        let task = Task { [weak self] in
            guard let self else { return }
            await self.runForegroundUpload(
                recordID: recordID,
                runID: runID,
                preflight: preflight,
                rescan: rescan
            )
        }
        uploadTasks[recordID] = task
    }

    func cancelForegroundUpload(recordID: String) {
        guard let task = uploadTasks[recordID] else { return }
        task.cancel()
        if let status = uploadStatuses[recordID],
           [.preflighting, .checking].contains(status.phase) {
            uploadStatuses[recordID] = UploadStatus(
                phase: .cancelled,
                evidence: status.evidence,
                completedPolls: status.completedPolls
            )
        }
    }

    func cancelAllForegroundUploads() {
        for recordID in uploadTasks.keys {
            cancelForegroundUpload(recordID: recordID)
        }
    }

    private func runForegroundUpload(
        recordID: String,
        runID: UUID,
        preflight: @escaping UploadPreflightProvider,
        rescan: @escaping UploadRescan
    ) async {
        var tupleKey: UploadTuple?
        var artifactCreated = false
        defer {
            if let tupleKey, activeUploadTuples[tupleKey] == runID {
                activeUploadTuples.removeValue(forKey: tupleKey)
            }
            if uploadRunIDs[recordID] == runID {
                uploadTasks.removeValue(forKey: recordID)
                uploadRunIDs.removeValue(forKey: recordID)
            }
        }
        do {
            try requireCurrentUploadRun(recordID: recordID, runID: runID)
            let record = try requiredRecord(recordID)
            guard record.state == .namespaceActive else {
                throw DiagnosticsProtocolError.unsupported
            }
            try requireCurrentCapability(recordID)
            guard let initialAppKeyID = record.namespaceInitialAppKeyID else {
                throw DiagnosticsProtocolError.unavailable
            }
            let expectedInstallation = DiagnosticsNamespaceProtocol.installationBinding(
                initialAppKeyID: initialAppKeyID,
                homeserverBinding: record.homeserverBinding,
                folderBinding: record.folderBinding
            )
            let installationComponent = DiagnosticsNamespaceProtocol.base32LowerNoPadding(
                expectedInstallation
            )
            let generalPreflight = preflight(
                installationComponent,
                String(repeating: "a", count: 52),
                false
            )
            try generalPreflight.validate(record: record, requireEmptySlot: false)
            let installation = try DiagnosticsUploadProtocol.verifyActiveNamespace(
                record: record,
                folderPath: generalPreflight.folderPath
            )
            guard installation == expectedInstallation else {
                throw DiagnosticsProtocolError.conflict
            }

            let operationID = try uploadRandomBytes(32)
            let requestNonce = try uploadRandomBytes(32)
            let queryNonce = try uploadRandomBytes(32)
            let payload = try uploadRandomBytes(DiagnosticsUploadProtocol.payloadByteCount)
            let appKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let operation = try DiagnosticsUploadProtocol.makeOperation(
                record: record,
                appKey: appKey,
                operationID: operationID,
                requestNonce: requestNonce,
                queryNonce: queryNonce,
                payload: payload,
                now: now()
            )
            guard operation.installationBinding == installation else {
                throw DiagnosticsProtocolError.conflict
            }
            let operationComponent = DiagnosticsNamespaceProtocol.base32LowerNoPadding(operationID)
            let exactPreflight = preflight(installationComponent, operationComponent, true)
            try exactPreflight.validate(record: record, requireEmptySlot: true)
            guard exactPreflight.sameRuntimeBoundary(as: generalPreflight) else {
                throw DiagnosticsProtocolError.unavailable
            }

            let key = uploadTupleKey(record)
            try beginUploadLease(tupleKey: key, recordID: recordID, runID: runID)
            tupleKey = key
            uploadStatuses[recordID] = UploadStatus(phase: .checking)
            let start = continuousNow()
            guard start.isFinite, start >= 0 else {
                throw DiagnosticsProtocolError.unavailable
            }
            let deadline = start + TimeInterval(DiagnosticsUploadProtocol.maximumLifetime)
            guard deadline.isFinite else { throw DiagnosticsProtocolError.unavailable }

            try uploadFileWriter(
                exactPreflight.folderPath,
                operation.requestComponents,
                operation.request.canonical
            )
            artifactCreated = true
            guard rescan() else { throw DiagnosticsProtocolError.unavailable }

            for (index, delay) in DiagnosticsUploadProtocol.pollDelays.enumerated() {
                try requireCurrentUploadRun(recordID: recordID, runID: runID)
                try await uploadSleep(delay)
                try requireCurrentUploadRun(recordID: recordID, runID: runID)
                let currentContinuous = continuousNow()
                guard currentContinuous.isFinite,
                      currentContinuous >= start else {
                    throw DiagnosticsProtocolError.unavailable
                }
                guard currentContinuous < deadline else {
                    throw DiagnosticsProtocolError.expired
                }
                let currentRecord = try requiredRecord(recordID)
                guard uploadBindingUnchanged(record, currentRecord) else {
                    throw DiagnosticsProtocolError.unavailable
                }
                let currentPreflight = preflight(installationComponent, operationComponent, false)
                try currentPreflight.validate(record: currentRecord, requireEmptySlot: false)
                guard currentPreflight.sameRuntimeBoundary(as: exactPreflight) else {
                    throw DiagnosticsProtocolError.unavailable
                }
                _ = try DiagnosticsUploadProtocol.verifyActiveNamespace(
                    record: currentRecord,
                    folderPath: currentPreflight.folderPath
                )
                let persistedRequest = try DiagnosticsNamespaceFileReader.read(
                    folderPath: currentPreflight.folderPath,
                    components: operation.requestComponents
                )
                guard persistedRequest == operation.request.canonical else {
                    throw DiagnosticsProtocolError.conflict
                }
                try consumeUploadRequest(recordID: recordID)
                let transport = try makeTransport(currentRecord)
                let response = try await transport.post(
                    path: DiagnosticsUploadProtocol.path,
                    body: operation.query.canonical,
                    responseBody: true
                )
                try requireCurrentUploadRun(recordID: recordID, runID: runID)
                var status = uploadStatuses[recordID] ?? UploadStatus(phase: .checking)
                status.completedPolls = index + 1
                uploadStatuses[recordID] = status
                guard let response else { continue }

                let finalRecord = try requiredRecord(recordID)
                guard uploadBindingUnchanged(record, finalRecord) else {
                    throw DiagnosticsProtocolError.unavailable
                }
                let finalPreflight = preflight(installationComponent, operationComponent, false)
                try finalPreflight.validate(record: finalRecord, requireEmptySlot: false)
                guard finalPreflight.sameRuntimeBoundary(as: exactPreflight) else {
                    throw DiagnosticsProtocolError.unavailable
                }
                let finalInstallation = try DiagnosticsUploadProtocol.verifyActiveNamespace(
                    record: finalRecord,
                    folderPath: finalPreflight.folderPath
                )
                guard finalInstallation == operation.installationBinding else {
                    throw DiagnosticsProtocolError.conflict
                }
                let finalRequest = try DiagnosticsNamespaceFileReader.read(
                    folderPath: finalPreflight.folderPath,
                    components: operation.requestComponents
                )
                guard finalRequest == operation.request.canonical else {
                    throw DiagnosticsProtocolError.conflict
                }
                _ = try DiagnosticsUploadProtocol.validateUploadAttestation(
                    response,
                    operation: operation,
                    record: finalRecord,
                    now: now()
                )
                uploadStatuses[recordID] = UploadStatus(
                    phase: .uploadObserved,
                    evidence: UploadEvidence(uploadObserved: true),
                    completedPolls: index + 1
                )
                return
            }
            throw DiagnosticsProtocolError.expired
        } catch is CancellationError {
            if uploadRunIDs[recordID] == runID,
               let status = uploadStatuses[recordID],
               [.preflighting, .checking].contains(status.phase) {
                uploadStatuses[recordID] = UploadStatus(
                    phase: .cancelled,
                    evidence: status.evidence,
                    completedPolls: status.completedPolls
                )
            }
        } catch let error as DiagnosticsProtocolError {
            guard uploadRunIDs[recordID] == runID else { return }
            lastError = error
            finishUploadFailure(recordID: recordID, error: error, artifactCreated: artifactCreated)
        } catch {
            guard uploadRunIDs[recordID] == runID else { return }
            lastError = .unavailable
            finishUploadFailure(recordID: recordID, error: .unavailable, artifactCreated: artifactCreated)
        }
    }

    private func requireCurrentUploadRun(recordID: String, runID: UUID) throws {
        try Task.checkCancellation()
        guard uploadRunIDs[recordID] == runID else {
            throw CancellationError()
        }
    }

    private func requireCurrentCapability(_ recordID: String) throws {
        let current = continuousNow()
        guard capabilityStates[recordID] == .available,
              let expiry = capabilityValidUntil[recordID],
              current.isFinite,
              current >= 0,
              current < expiry else {
            invalidateCapability(recordID)
            throw DiagnosticsProtocolError.unavailable
        }
    }

    private func beginUploadLease(
        tupleKey: UploadTuple,
        recordID: String,
        runID: UUID
    ) throws {
        let current = continuousNow()
        guard current.isFinite, current >= 0,
              activeUploadTuples[tupleKey] == nil,
              activeUploadTuples.count < 2 else {
            throw DiagnosticsProtocolError.rateLimited
        }
        let hour = pruneUploadWindow(uploadStartsByRecord[recordID] ?? [], now: current, duration: 3_600)
        let day = pruneUploadWindow(uploadStartsByRecord[recordID] ?? [], now: current, duration: 86_400)
        guard hour.count < 3, day.count < 12 else {
            uploadStartsByRecord[recordID] = day
            throw DiagnosticsProtocolError.rateLimited
        }
        uploadStartsByRecord[recordID] = day + [current]
        activeUploadTuples[tupleKey] = runID
    }

    private func consumeUploadRequest(recordID: String) throws {
        let current = continuousNow()
        guard current.isFinite, current >= 0 else {
            throw DiagnosticsProtocolError.unavailable
        }
        var byRecord = pruneUploadWindow(
            uploadRequestsByRecord[recordID] ?? [],
            now: current,
            duration: 60
        )
        uploadRequests = pruneUploadWindow(uploadRequests, now: current, duration: 60)
        guard byRecord.count < 30, uploadRequests.count < 120 else {
            throw DiagnosticsProtocolError.rateLimited
        }
        byRecord.append(current)
        uploadRequests.append(current)
        uploadRequestsByRecord[recordID] = byRecord
    }

    private func pruneUploadWindow(
        _ values: [TimeInterval],
        now: TimeInterval,
        duration: TimeInterval
    ) -> [TimeInterval] {
        values.filter { $0 > now - duration && $0 <= now }
    }

    private func uploadTupleKey(_ record: DiagnosticsPairingRecord) -> UploadTuple {
        UploadTuple(
            recordID: record.id,
            homeserverDeviceID: record.homeserverDeviceID,
            folderID: record.folderID,
            homeserverBinding: record.homeserverBinding,
            folderBinding: record.folderBinding,
            appKeyID: record.appKeyID,
            helperKeyID: record.helperKeyID,
            appEpoch: record.appEpoch,
            helperEpoch: record.helperEpoch,
            namespaceID: record.namespaceID,
            namespaceAuthorizationDigest: record.namespaceAuthorizationDigest,
            namespaceAuthorizationEpoch: record.namespaceAuthorizationEpoch
        )
    }

    private func uploadBindingUnchanged(
        _ initial: DiagnosticsPairingRecord,
        _ current: DiagnosticsPairingRecord
    ) -> Bool {
        current.state == .namespaceActive && current == initial
    }

    private func finishUploadFailure(
        recordID: String,
        error: DiagnosticsProtocolError,
        artifactCreated: Bool
    ) {
        let existing = uploadStatuses[recordID] ?? UploadStatus(phase: .unavailable)
        let phase: UploadPhase
        switch error {
        case .expired:
            phase = .timedOut
        case .rateLimited:
            phase = .rateLimited
        case .unsupported:
            phase = .unsupported
        case .conflict, .invalidMessage:
            phase = .conflict
        case .unavailable, .protectedDataUnavailable, .recoveryRequired:
            phase = artifactCreated ? .interrupted : .unavailable
        }
        uploadStatuses[recordID] = UploadStatus(
            phase: phase,
            evidence: existing.evidence,
            completedPolls: existing.completedPolls
        )
    }

    func requestNamespaceEnablement(recordID: String) async {
        await perform {
            var record = try requiredActiveRecord(recordID)
            guard capabilityStates[recordID] == .available,
                  record.namespaceAuthorizationEpoch == 0 else {
                throw DiagnosticsProtocolError.unsupported
            }
            let currentContinuous = continuousNow()
            guard let capabilityExpiry = capabilityValidUntil[recordID],
                  currentContinuous.isFinite,
                  currentContinuous >= 0,
                  currentContinuous < capabilityExpiry else {
                invalidateCapability(recordID)
                throw DiagnosticsProtocolError.unavailable
            }
            let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let enablement = try DiagnosticsNamespaceProtocol.makeEnablement(
                record: record,
                appKey: key,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            record.namespaceEnablement = enablement
            record.localDeadline = try makeLocalDeadline(
                expiresAt: try messageExpiry(enablement, label: 27)
            )
            record.lastOutgoing = enablement
            record.lastIncoming = nil
            record.state = .namespaceEnablementPrepared
            try persist(record)
            let transport = try makeTransport(record)
            _ = try await transport.post(
                path: DiagnosticsNamespaceProtocol.enablementPath,
                body: enablement,
                responseBody: false
            )
            record.state = .namespaceAwaitingOperator
            try persist(record)
            notice = .operatorNamespaceAction(recordID: record.id)
        }
    }

    /// This advances exactly one explicit namespace step. It never polls and
    /// never creates or adopts a directory on the app side.
    func continueNamespace(recordID: String, currentFolderPath: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            switch record.state {
            case .namespaceEnablementPrepared:
                try requireLocalDeadline(record)
                let transport = try makeTransport(record)
                _ = try await transport.post(
                    path: DiagnosticsNamespaceProtocol.enablementPath,
                    body: record.lastOutgoing,
                    responseBody: false
                )
                record.state = .namespaceAwaitingOperator
                try persist(record)
                notice = .operatorNamespaceAction(recordID: record.id)
            case .namespaceAwaitingOperator:
                guard let enablement = record.namespaceEnablement else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let rootData = try DiagnosticsNamespaceFileReader.read(
                    folderPath: currentFolderPath,
                    components: [DiagnosticsNamespaceProtocol.rootName, DiagnosticsNamespaceProtocol.rootManifestName]
                )
                let root = try DiagnosticsNamespaceProtocol.validateRootManifest(
                    rootData,
                    enablement: enablement,
                    record: record
                )
                let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
                let candidate = try DiagnosticsNamespaceProtocol.makeInitialAuthorization(
                    record: record,
                    root: root,
                    appKey: key,
                    nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                    now: now()
                )
                record.namespaceID = root.namespaceID
                record.namespaceRootDigest = root.rootDigest
                record.namespaceManifestDigest = root.manifestDigest
                record.namespaceManifestEpoch = record.helperEpoch
                record.localDeadline = try makeLocalDeadline(
                    expiresAt: try messageExpiry(candidate.message, label: 27)
                )
                record.lastIncoming = root.message
                record.lastOutgoing = candidate.message
                record.state = .namespaceAuthorizationPrepared
                try persist(record)
                let transport = try makeTransport(record)
                _ = try await transport.post(
                    path: DiagnosticsNamespaceProtocol.authorizationPath,
                    body: candidate.message,
                    responseBody: false
                )
                notice = .namespaceAuthorizationPending(recordID: record.id)
            case .namespaceAuthorizationPrepared:
                if let deadline = record.localDeadline, !localDeadlineExpired(deadline) {
                    let transport = try makeTransport(record)
                    _ = try await transport.post(
                        path: DiagnosticsNamespaceProtocol.authorizationPath,
                        body: record.lastOutgoing,
                        responseBody: false
                    )
                }
                guard let rootData = record.lastIncoming,
                      let enablement = record.namespaceEnablement else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let root = try DiagnosticsNamespaceProtocol.validateRootManifest(
                    rootData,
                    enablement: enablement,
                    record: record
                )
                let candidate = DiagnosticsNamespaceProtocol.AuthorizationCandidate(
                    message: record.lastOutgoing,
                    installationBinding: try installationBinding(from: record.lastOutgoing)
                )
                let relative = try DiagnosticsNamespaceProtocol.authorizationRelativePath(
                    installationBinding: candidate.installationBinding
                )
                let completed = try DiagnosticsNamespaceFileReader.read(
                    folderPath: currentFolderPath,
                    components: [DiagnosticsNamespaceProtocol.rootName] + relative.split(separator: "/").map(String.init)
                )
                let digest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorization(
                    completed,
                    candidate: candidate,
                    record: record,
                    root: root
                )
                record.namespaceAuthorizationDigest = digest
                record.namespaceInitialAppKeyID = record.appKeyID
                record.namespaceAuthorizationEpoch = 1
                record.state = .namespaceActive
                record.localDeadline = nil
                record.lastIncoming = completed
                try persist(record)
                notice = .namespaceActive(recordID: record.id)
            default:
                throw DiagnosticsProtocolError.conflict
            }
        }
    }

    func startAppKeyRotation(recordID: String) async {
        await perform {
            var record = try requiredActiveRecord(recordID)
            guard record.pendingLifecycle == nil else { throw DiagnosticsProtocolError.conflict }
            let currentKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let proposedKey = try proposedInstallationAppKey(for: record)
            let request = try DiagnosticsPairingProtocol.makeAppKeyRotationRequest(
                record: record,
                proposedKey: proposedKey,
                currentKey: currentKey,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            record.pendingLifecycle = DiagnosticsPairingRecord.PendingLifecycle(
                kind: .appKey,
                transitionDigest: try request.digest(),
                latestMessage: request.canonical,
                proposedAppSeed: proposedKey.rawRepresentation,
                proposedHelperPublicKey: nil,
                proposedHelperEpoch: nil,
                proposedTLSSPKIPin: nil
            )
            record.lastOutgoing = request.canonical
            record.lastIncoming = nil
            record.state = .lifecyclePending
            record.localDeadline = try makeLocalDeadline(
                expiresAt: try lifecycleExpiry(request)
            )
            try persist(record)
            invalidateCapability(record.id)
            try await advanceLifecycle(&record)
        }
    }

    func startHelperKeyRotation(recordID: String, proposal: String, proof: String) async {
        await perform {
            var record = try requiredActiveRecord(recordID)
            guard record.pendingLifecycle == nil else { throw DiagnosticsProtocolError.conflict }
            let proposalMessage = try DiagnosticsPairingProtocol.decode(
                DiagnosticsCrypto.base64URLDecode(proposal)
            )
            try DiagnosticsPairingProtocol.validateLifecycleMessage(
                proposalMessage,
                expectedType: .helperKeyRotationPropose,
                record: record,
                now: now()
            )
            let proofMessage = try DiagnosticsPairingProtocol.decode(
                DiagnosticsCrypto.base64URLDecode(proof)
            )
            try DiagnosticsPairingProtocol.validateLifecycleMessage(
                proofMessage,
                expectedType: .helperKeyRotationNewProof,
                record: record,
                prior: proposalMessage,
                now: now()
            )
            guard let proposedPublic = proofMessage.value.bytes(for: 13, count: 32),
                  let proposedEpoch = proofMessage.value.unsigned(for: 20) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let currentKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let confirmation = try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: proofMessage,
                type: .helperKeyRotationConfirm,
                transitionKind: nil,
                transitionDigest: nil,
                signer: currentKey,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            record.pendingLifecycle = DiagnosticsPairingRecord.PendingLifecycle(
                kind: .helperKey,
                transitionDigest: try proposalMessage.digest(),
                latestMessage: confirmation.canonical,
                proposedAppSeed: nil,
                proposedHelperPublicKey: proposedPublic,
                proposedHelperEpoch: proposedEpoch,
                proposedTLSSPKIPin: nil
            )
            record.lastOutgoing = confirmation.canonical
            record.lastIncoming = nil
            record.state = .lifecyclePending
            record.localDeadline = try makeLocalDeadline(
                expiresAt: try lifecycleExpiry(confirmation)
            )
            try persist(record)
            invalidateCapability(record.id)
            try await advanceLifecycle(&record)
        }
    }

    func startTLSPinRotation(recordID: String, proposal: String) async {
        await perform {
            var record = try requiredActiveRecord(recordID)
            guard record.pendingLifecycle == nil else { throw DiagnosticsProtocolError.conflict }
            let proposalMessage = try DiagnosticsPairingProtocol.decode(
                DiagnosticsCrypto.base64URLDecode(proposal)
            )
            try DiagnosticsPairingProtocol.validateLifecycleMessage(
                proposalMessage,
                expectedType: .tlsPinRotationPropose,
                record: record,
                now: now()
            )
            guard let proposedPin = proposalMessage.value.bytes(for: 16, count: 32),
                  proposedPin != record.tlsSPKIPin else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let currentKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let confirmation = try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: proposalMessage,
                type: .tlsPinRotationConfirm,
                transitionKind: nil,
                transitionDigest: nil,
                signer: currentKey,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            record.pendingLifecycle = DiagnosticsPairingRecord.PendingLifecycle(
                kind: .tlsPin,
                transitionDigest: try proposalMessage.digest(),
                latestMessage: confirmation.canonical,
                proposedAppSeed: nil,
                proposedHelperPublicKey: nil,
                proposedHelperEpoch: nil,
                proposedTLSSPKIPin: proposedPin
            )
            record.lastOutgoing = confirmation.canonical
            record.lastIncoming = nil
            record.state = .lifecyclePending
            record.localDeadline = try makeLocalDeadline(
                expiresAt: try lifecycleExpiry(confirmation)
            )
            try persist(record)
            invalidateCapability(record.id)
            try await advanceLifecycle(&record)
        }
    }

    func continueLifecycle(recordID: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            guard record.state == .lifecyclePending, record.pendingLifecycle != nil else {
                throw DiagnosticsProtocolError.conflict
            }
            try requireLocalDeadline(record)
            try await advanceLifecycle(&record)
        }
    }

    func abortLifecycle(recordID: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            guard let pending = record.pendingLifecycle,
                  record.state == .lifecyclePending else {
                throw DiagnosticsProtocolError.conflict
            }
            try requireLocalDeadline(record)
            var latest = try DiagnosticsPairingProtocol.decode(pending.latestMessage)
            let transport = try makeTransport(record)
            switch latest.type {
            case .appKeyRotationRequest, .helperKeyRotationConfirm, .tlsPinRotationConfirm:
                _ = try await transport.post(
                    path: DiagnosticsPairingProtocol.path,
                    body: latest.canonical,
                    responseBody: false
                )
            case .appKeyRotationNewProof:
                guard let responseData = try await transport.post(
                    path: DiagnosticsPairingProtocol.path,
                    body: latest.canonical,
                    responseBody: true
                ) else { throw DiagnosticsProtocolError.invalidMessage }
                let response = try DiagnosticsPairingProtocol.decode(responseData)
                try DiagnosticsPairingProtocol.validateLifecycleMessage(
                    response,
                    expectedType: .appKeyRotationAccept,
                    record: record,
                    prior: latest,
                    now: now()
                )
                try saveLifecycleLatest(response, outgoing: false, record: &record)
                latest = response
            case .appKeyRotationAccept:
                break
            case .lifecycleAbort:
                try await sendPreparedLifecycleAbort(&record)
                return
            default:
                // Type 21 may already have committed at the helper and type 22
                // proves that it did. Neither can be silently rolled back.
                throw DiagnosticsProtocolError.conflict
            }

            guard let currentPending = record.pendingLifecycle else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let currentKey = try Curve25519.Signing.PrivateKey(
                rawRepresentation: record.appSeed
            )
            let abort = try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                prior: latest,
                type: .lifecycleAbort,
                transitionKind: currentPending.kind,
                transitionDigest: currentPending.transitionDigest,
                signer: currentKey,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            try saveLifecycleLatest(abort, outgoing: true, record: &record)
            try await sendPreparedLifecycleAbort(&record)
        }
    }

    func canDiscardExpiredLifecycle(_ record: DiagnosticsPairingRecord) -> Bool {
        guard record.state == .lifecyclePending,
              let pending = record.pendingLifecycle,
              let latest = try? DiagnosticsPairingProtocol.decode(pending.latestMessage),
              [.appKeyRotationRequest, .appKeyRotationNewProof,
               .appKeyRotationAccept, .helperKeyRotationConfirm,
               .tlsPinRotationConfirm, .lifecycleAbort].contains(latest.type),
              let expires = latest.value.unsigned(for: 22),
              let discardAfter = try? DiagnosticsPairingProtocol.checkedAdding(
                expires,
                DiagnosticsPairingProtocol.maximumClockSkew
              ) else {
            return false
        }
        if let deadline = record.localDeadline, localDeadlineExpired(deadline) {
            return true
        }
        let seconds = now().timeIntervalSince1970.rounded(.down)
        guard seconds >= 0, seconds < Double(UInt64.max) else { return false }
        return UInt64(seconds) > discardAfter
    }

    func discardExpiredLifecycle(recordID: String) {
        do {
            var record = try requiredRecord(recordID)
            guard canDiscardExpiredLifecycle(record) else {
                throw DiagnosticsProtocolError.conflict
            }
            record.pendingLifecycle = nil
            record.state = record.namespaceAuthorizationEpoch > 0 ? .namespaceActive : .active
            record.localDeadline = nil
            invalidateCapability(record.id)
            try persist(record)
            lastError = nil
        } catch let error as DiagnosticsProtocolError {
            lastError = error
        } catch {
            lastError = .unavailable
        }
    }

    func continueNamespaceAuthorizationRefresh(recordID: String, currentFolderPath: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            switch record.state {
            case .namespaceAuthorizationRefreshRequired:
                guard let namespaceID = record.namespaceID,
                      let rootDigest = record.namespaceRootDigest,
                      let storedManifestDigest = record.namespaceManifestDigest,
                      let storedManifestEpoch = record.namespaceManifestEpoch,
                      let priorAuthorizationDigest = record.namespaceAuthorizationDigest else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let rootData = try DiagnosticsNamespaceFileReader.read(
                    folderPath: currentFolderPath,
                    components: [DiagnosticsNamespaceProtocol.rootName, DiagnosticsNamespaceProtocol.rootManifestName]
                )
                guard DiagnosticsNamespaceProtocol.recordDigest(rootData) == rootDigest,
                      let rootValue = try? DiagnosticsDeterministicCBOR.decode(rootData),
                      rootValue.bytes(for: 7, count: 32) == namespaceID,
                      let rootHelperEpoch = rootValue.unsigned(for: 15),
                      storedManifestEpoch >= rootHelperEpoch,
                      storedManifestEpoch <= record.helperEpoch else {
                    throw DiagnosticsProtocolError.conflict
                }
                if storedManifestEpoch == rootHelperEpoch,
                   storedManifestDigest != rootDigest {
                    throw DiagnosticsProtocolError.conflict
                }
                var manifestDigest = storedManifestDigest
                var manifestEpoch = storedManifestEpoch
                if record.helperEpoch > storedManifestEpoch {
                    guard (try DiagnosticsPairingProtocol.checkedAdding(storedManifestEpoch, 1)) ==
                            record.helperEpoch else {
                        throw DiagnosticsProtocolError.conflict
                    }
                    let currentManifest = try DiagnosticsNamespaceFileReader.read(
                        folderPath: currentFolderPath,
                        components: [
                            DiagnosticsNamespaceProtocol.rootName,
                            "manifest-epochs",
                            "\(record.helperEpoch).helper-manifest.cbor",
                        ]
                    )
                    let priorManifest: Data
                    if storedManifestEpoch == rootHelperEpoch {
                        priorManifest = rootData
                    } else {
                        priorManifest = try DiagnosticsNamespaceFileReader.read(
                            folderPath: currentFolderPath,
                            components: [
                                DiagnosticsNamespaceProtocol.rootName,
                                "manifest-epochs",
                                "\(storedManifestEpoch).helper-manifest.cbor",
                            ]
                        )
                    }
                    guard DiagnosticsNamespaceProtocol.recordDigest(priorManifest) == storedManifestDigest else {
                        throw DiagnosticsProtocolError.conflict
                    }
                    manifestDigest = try DiagnosticsNamespaceProtocol.validateHelperEpochManifest(
                        currentManifest,
                        rootData: rootData,
                        priorManifestData: priorManifest,
                        record: record
                    )
                    manifestEpoch = record.helperEpoch
                }
                let root = DiagnosticsNamespaceProtocol.RootManifest(
                    message: rootData,
                    namespaceID: namespaceID,
                    rootDigest: rootDigest,
                    manifestDigest: manifestDigest
                )
                let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
                let candidate = try DiagnosticsNamespaceProtocol.makeAuthorizationEpoch(
                    record: record,
                    root: root,
                    priorAuthorizationDigest: priorAuthorizationDigest,
                    appKey: key,
                    nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                    now: now()
                )
                record.namespaceManifestDigest = manifestDigest
                record.namespaceManifestEpoch = manifestEpoch
                record.lastOutgoing = candidate.message
                record.lastIncoming = rootData
                record.state = .namespaceAuthorizationRefreshPrepared
                record.localDeadline = try makeLocalDeadline(
                    expiresAt: try messageExpiry(candidate.message, label: 27)
                )
                try persist(record)
                let transport = try makeTransport(record)
                _ = try await transport.post(
                    path: DiagnosticsNamespaceProtocol.authorizationPath,
                    body: candidate.message,
                    responseBody: false
                )
                notice = .namespaceAuthorizationPending(recordID: record.id)
            case .namespaceAuthorizationRefreshPrepared:
                if let deadline = record.localDeadline, !localDeadlineExpired(deadline) {
                    let transport = try makeTransport(record)
                    _ = try await transport.post(
                        path: DiagnosticsNamespaceProtocol.authorizationPath,
                        body: record.lastOutgoing,
                        responseBody: false
                    )
                }
                guard let rootData = record.lastIncoming,
                      let namespaceID = record.namespaceID,
                      let rootDigest = record.namespaceRootDigest,
                      let manifestDigest = record.namespaceManifestDigest else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let candidate = DiagnosticsNamespaceProtocol.AuthorizationCandidate(
                    message: record.lastOutgoing,
                    installationBinding: try installationBinding(from: record.lastOutgoing)
                )
                let epoch = try DiagnosticsPairingProtocol.checkedAdding(
                    record.namespaceAuthorizationEpoch,
                    1
                )
                let relative = try DiagnosticsNamespaceProtocol.authorizationEpochRelativePath(
                    installationBinding: candidate.installationBinding,
                    epoch: epoch
                )
                let completed = try DiagnosticsNamespaceFileReader.read(
                    folderPath: currentFolderPath,
                    components: [DiagnosticsNamespaceProtocol.rootName] + relative.split(separator: "/").map(String.init)
                )
                let root = DiagnosticsNamespaceProtocol.RootManifest(
                    message: rootData,
                    namespaceID: namespaceID,
                    rootDigest: rootDigest,
                    manifestDigest: manifestDigest
                )
                let digest = try DiagnosticsNamespaceProtocol.validateCompletedAuthorizationEpoch(
                    completed,
                    candidate: candidate,
                    record: record,
                    root: root
                )
                record.namespaceAuthorizationDigest = digest
                record.namespaceAuthorizationEpoch = epoch
                record.lastIncoming = completed
                record.state = .namespaceActive
                record.localDeadline = nil
                try persist(record)
                notice = .namespaceActive(recordID: record.id)
            default:
                throw DiagnosticsProtocolError.conflict
            }
        }
    }

    func revoke(recordID: String, reason: DiagnosticsPairingProtocol.RevocationReason) async {
        await perform {
            var record = try requiredActiveRecord(recordID)
            let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            let request = try DiagnosticsPairingProtocol.makeRevocationRequest(
                record: record,
                reason: reason,
                currentKey: key,
                nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                now: now()
            )
            record.lastOutgoing = request.canonical
            record.lastIncoming = nil
            record.state = .revocationPrepared
            record.localDeadline = try makeLocalDeadline(
                expiresAt: try lifecycleExpiry(request)
            )
            try persist(record)
            try await sendPreparedRevocation(&record)
        }
    }

    func retryRevocation(recordID: String) async {
        await perform {
            var record = try requiredRecord(recordID)
            guard record.state == .revocationPrepared else { throw DiagnosticsProtocolError.conflict }
            try requireLocalDeadline(record)
            try await sendPreparedRevocation(&record)
        }
    }

    func resetOrphanedCredentialsForRepair() {
        do {
            try credentialStore.resetForExplicitRepair()
            records = []
            capabilityStates = [:]
            capabilityValidUntil = [:]
            hasInstallationMarker = false
            hasInstallationCredential = false
            notice = .none
            lastError = nil
        } catch let error as DiagnosticsProtocolError {
            lastError = error
        } catch {
            lastError = .unavailable
        }
    }

    func clearNotice() {
        notice = .none
    }

    private func prepareBootstrap(
        _ record: inout DiagnosticsPairingRecord,
        type: DiagnosticsPairingProtocol.MessageType,
        preparedState: DiagnosticsPairingRecord.State
    ) throws {
        try requireLocalDeadline(record)
        guard let incoming = record.lastIncoming else { throw DiagnosticsProtocolError.invalidMessage }
        let prior = try DiagnosticsPairingProtocol.decode(incoming)
        let key = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
        let message = try DiagnosticsPairingProtocol.makeBootstrapTransition(
            prior: prior,
            type: type,
            appPrivateKey: key,
            now: now(),
            hardExpiry: record.hardExpiry
        )
        record.lastOutgoing = message.canonical
        record.state = preparedState
        try persist(record)
    }

    private func advanceLifecycle(_ record: inout DiagnosticsPairingRecord) async throws {
        while let pending = record.pendingLifecycle {
            try requireLocalDeadline(record)
            let latest = try DiagnosticsPairingProtocol.decode(pending.latestMessage)
            switch latest.type {
            case .appKeyRotationRequest:
                let transport = try makeTransport(record)
                _ = try await transport.post(
                    path: DiagnosticsPairingProtocol.path,
                    body: latest.canonical,
                    responseBody: false
                )
                guard let proposedSeed = pending.proposedAppSeed else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let proposedKey = try Curve25519.Signing.PrivateKey(rawRepresentation: proposedSeed)
                let proof = try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                    prior: latest,
                    type: .appKeyRotationNewProof,
                    transitionKind: nil,
                    transitionDigest: nil,
                    signer: proposedKey,
                    nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                    now: now()
                )
                try saveLifecycleLatest(proof, outgoing: true, record: &record)
            case .appKeyRotationNewProof:
                let transport = try makeTransport(record)
                guard let responseData = try await transport.post(
                    path: DiagnosticsPairingProtocol.path,
                    body: latest.canonical,
                    responseBody: true
                ) else { throw DiagnosticsProtocolError.invalidMessage }
                let response = try DiagnosticsPairingProtocol.decode(responseData)
                try DiagnosticsPairingProtocol.validateLifecycleMessage(
                    response,
                    expectedType: .appKeyRotationAccept,
                    record: record,
                    prior: latest,
                    now: now()
                )
                try saveLifecycleLatest(response, outgoing: false, record: &record)
            case .appKeyRotationAccept:
                guard let proposedSeed = pending.proposedAppSeed else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                let proposedKey = try Curve25519.Signing.PrivateKey(rawRepresentation: proposedSeed)
                let finalize = try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                    prior: latest,
                    type: .lifecycleFinalize,
                    transitionKind: .appKey,
                    transitionDigest: pending.transitionDigest,
                    signer: proposedKey,
                    nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                    now: now()
                )
                try saveLifecycleLatest(finalize, outgoing: true, record: &record)
            case .helperKeyRotationConfirm, .tlsPinRotationConfirm:
                let transport = try makeTransport(record)
                _ = try await transport.post(
                    path: DiagnosticsPairingProtocol.path,
                    body: latest.canonical,
                    responseBody: false
                )
                let currentKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
                let finalize = try DiagnosticsPairingProtocol.makeLifecycleContinuation(
                    prior: latest,
                    type: .lifecycleFinalize,
                    transitionKind: pending.kind,
                    transitionDigest: pending.transitionDigest,
                    signer: currentKey,
                    nonce: try DiagnosticsCrypto.randomBytes(count: 32),
                    now: now()
                )
                try saveLifecycleLatest(finalize, outgoing: true, record: &record)
            case .lifecycleFinalize:
                let transport = try makeTransport(record)
                guard let responseData = try await transport.post(
                    path: DiagnosticsPairingProtocol.path,
                    body: latest.canonical,
                    responseBody: true
                ) else { throw DiagnosticsProtocolError.invalidMessage }
                let response = try DiagnosticsPairingProtocol.decode(responseData)
                try DiagnosticsPairingProtocol.validateLifecycleMessage(
                    response,
                    expectedType: .lifecycleActiveAck,
                    record: record,
                    prior: latest,
                    now: now()
                )
                try saveLifecycleLatest(response, outgoing: false, record: &record)
            case .lifecycleActiveAck:
                try await confirmProposedLifecycleState(&record, acknowledgment: latest)
                return
            default:
                throw DiagnosticsProtocolError.invalidMessage
            }
        }
        throw DiagnosticsProtocolError.invalidMessage
    }

    private func saveLifecycleLatest(
        _ message: DiagnosticsPairingProtocol.Message,
        outgoing: Bool,
        record: inout DiagnosticsPairingRecord
    ) throws {
        guard var pending = record.pendingLifecycle else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        pending.latestMessage = message.canonical
        record.pendingLifecycle = pending
        if outgoing {
            record.lastOutgoing = message.canonical
        } else {
            record.lastIncoming = message.canonical
        }
        try persist(record)
    }

    private func confirmProposedLifecycleState(
        _ record: inout DiagnosticsPairingRecord,
        acknowledgment: DiagnosticsPairingProtocol.Message
    ) async throws {
        guard let pending = record.pendingLifecycle else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var proposed = record
        let proposedKey: Curve25519.Signing.PrivateKey
        switch pending.kind {
        case .appKey:
            guard let seed = pending.proposedAppSeed else { throw DiagnosticsProtocolError.invalidMessage }
            proposedKey = try Curve25519.Signing.PrivateKey(rawRepresentation: seed)
            proposed.appSeed = seed
            proposed.appPublicKey = proposedKey.publicKey.rawRepresentation
            proposed.appKeyID = DiagnosticsCrypto.keyID(publicKey: proposed.appPublicKey)
            proposed.appEpoch = try DiagnosticsPairingProtocol.checkedAdding(proposed.appEpoch, 1)
        case .helperKey:
            guard let publicKey = pending.proposedHelperPublicKey,
                  let epoch = pending.proposedHelperEpoch else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            proposedKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            proposed.helperPublicKey = publicKey
            proposed.helperKeyID = DiagnosticsCrypto.keyID(publicKey: publicKey)
            proposed.helperEpoch = epoch
        case .tlsPin:
            guard let pin = pending.proposedTLSSPKIPin else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            proposedKey = try Curve25519.Signing.PrivateKey(rawRepresentation: record.appSeed)
            proposed.tlsSPKIPin = pin
        }
        proposed.currentCredentialStateDigest = try acknowledgment.digest()
        let query = try DiagnosticsCapabilityProtocol.makeQuery(
            record: proposed,
            appKey: proposedKey,
            nonce: try DiagnosticsCrypto.randomBytes(count: 32),
            now: now()
        )
        let transport = try makeTransport(proposed)
        guard let response = try await transport.post(
            path: DiagnosticsCapabilityProtocol.path,
            body: query.message,
            responseBody: true
        ) else { throw DiagnosticsProtocolError.invalidMessage }
        let expires = try DiagnosticsCapabilityProtocol.validateResponse(
            response,
            query: query,
            record: proposed,
            now: now()
        )
        proposed.pendingLifecycle = nil
        proposed.state = proposed.namespaceAuthorizationEpoch > 0
            ? .namespaceAuthorizationRefreshRequired
            : .active
        proposed.localDeadline = nil
        record = proposed
        capabilityStates[record.id] = .available
        capabilityValidUntil[record.id] = try makeContinuousDeadline(
            expiresAt: expires,
            maximumLifetime: 120
        )
        try persist(record)
    }

    private func sendPreparedRevocation(_ record: inout DiagnosticsPairingRecord) async throws {
        try requireLocalDeadline(record)
        let request = try DiagnosticsPairingProtocol.decode(record.lastOutgoing)
        guard request.type == .revocationRequest else { throw DiagnosticsProtocolError.invalidMessage }
        let transport = try makeTransport(record)
        guard let responseData = try await transport.post(
            path: DiagnosticsPairingProtocol.path,
            body: request.canonical,
            responseBody: true
        ) else { throw DiagnosticsProtocolError.invalidMessage }
        let response = try DiagnosticsPairingProtocol.decode(responseData)
        try DiagnosticsPairingProtocol.validateLifecycleMessage(
            response,
            expectedType: .revocationRecord,
            record: record,
            prior: request,
            now: now()
        )
        let revokedEpoch = try DiagnosticsPairingProtocol.checkedAdding(record.appEpoch, 1)
        guard response.value.unsigned(for: 18) == revokedEpoch else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        record.appEpoch = revokedEpoch
        record.currentCredentialStateDigest = try response.digest()
        record.lastIncoming = response.canonical
        record.state = .revoked
        record.pendingLifecycle = nil
        record.localDeadline = nil
        capabilityStates[record.id] = .unavailable
        capabilityValidUntil.removeValue(forKey: record.id)
        try persist(record)
    }

    private func sendPreparedLifecycleAbort(
        _ record: inout DiagnosticsPairingRecord
    ) async throws {
        try requireLocalDeadline(record)
        guard let pending = record.pendingLifecycle else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let request = try DiagnosticsPairingProtocol.decode(pending.latestMessage)
        guard request.type == .lifecycleAbort else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let transport = try makeTransport(record)
        guard let responseData = try await transport.post(
            path: DiagnosticsPairingProtocol.path,
            body: request.canonical,
            responseBody: true
        ) else { throw DiagnosticsProtocolError.invalidMessage }
        let response = try DiagnosticsPairingProtocol.decode(responseData)
        try DiagnosticsPairingProtocol.validateLifecycleMessage(
            response,
            expectedType: .lifecycleAbortAck,
            record: record,
            prior: request,
            now: now()
        )
        guard response.value.unsigned(for: 29) == pending.kind.rawValue,
              response.value.bytes(for: 28, count: 32) == pending.transitionDigest else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        record.lastIncoming = response.canonical
        record.pendingLifecycle = nil
        record.state = record.namespaceAuthorizationEpoch > 0 ? .namespaceActive : .active
        record.localDeadline = nil
        invalidateCapability(record.id)
        try persist(record)
    }

    private func sendPreparedAbort(_ record: inout DiagnosticsPairingRecord) async throws {
        try requireLocalDeadline(record)
        let request = try DiagnosticsPairingProtocol.decode(record.lastOutgoing)
        guard request.type == .abort else { throw DiagnosticsProtocolError.invalidMessage }
        let transport = try makeTransport(record)
        guard let responseData = try await transport.post(
            path: DiagnosticsPairingProtocol.path,
            body: request.canonical,
            responseBody: true
        ) else { throw DiagnosticsProtocolError.invalidMessage }
        let response = try DiagnosticsPairingProtocol.decode(responseData)
        try DiagnosticsPairingProtocol.validateBootstrapResponse(
            response,
            expectedType: .abortAck,
            prior: request,
            now: now()
        )
        guard response.value.bytes(for: 9, count: 32) == record.helperPublicKey,
              response.value.bytes(for: 10, count: 32) == record.helperKeyID,
              response.value.bytes(for: 11, count: 32) == record.homeserverBinding,
              response.value.bytes(for: 12, count: 32) == record.folderBinding,
              response.value.bytes(for: 18, count: 32) == record.appPublicKey,
              response.value.bytes(for: 19, count: 32) == record.appKeyID else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        try credentialStore.delete(record)
        records.removeAll { $0.id == record.id }
        capabilityStates.removeValue(forKey: record.id)
        capabilityValidUntil.removeValue(forKey: record.id)
        notice = .none
    }

    private func sendPreparedBootstrap(_ record: inout DiagnosticsPairingRecord) async throws {
        try requireLocalDeadline(record)
        let outgoing = try DiagnosticsPairingProtocol.decode(record.lastOutgoing)
        let expected: DiagnosticsPairingProtocol.MessageType
        let receivedState: DiagnosticsPairingRecord.State
        switch record.state {
        case .requestPrepared:
            expected = .helperAccept
            receivedState = .acceptanceReceived
        case .finalizePrepared:
            expected = .finalizeAck
            receivedState = .finalizeAcknowledged
        case .receiptPrepared:
            expected = .readyAck
            receivedState = .readyAcknowledged
        case .activatePrepared:
            expected = .activeAck
            receivedState = .active
        default:
            throw DiagnosticsProtocolError.conflict
        }
        let transport = try makeTransport(record)
        guard let responseData = try await transport.post(
            path: DiagnosticsPairingProtocol.path,
            body: record.lastOutgoing,
            responseBody: true
        ) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let response = try DiagnosticsPairingProtocol.decode(responseData)
        try DiagnosticsPairingProtocol.validateBootstrapResponse(
            response,
            expectedType: expected,
            prior: outgoing,
            now: now()
        )
        guard response.value.bytes(for: 9, count: 32) == record.helperPublicKey,
              response.value.bytes(for: 10, count: 32) == record.helperKeyID,
              response.value.bytes(for: 11, count: 32) == record.homeserverBinding,
              response.value.bytes(for: 12, count: 32) == record.folderBinding,
              response.value.bytes(for: 18, count: 32) == record.appPublicKey,
              response.value.bytes(for: 19, count: 32) == record.appKeyID else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        record.lastIncoming = response.canonical
        record.currentCredentialStateDigest = try response.digest()
        record.state = receivedState
        if receivedState == .active {
            record.localDeadline = nil
        }
        if expected == .helperAccept {
            let fingerprint = try DiagnosticsCrypto.fingerprint(
                appRequestDigest: try outgoing.digest(),
                helperAcceptDigest: try response.digest()
            )
            record.transcriptFingerprint = fingerprint
            notice = .fingerprint(recordID: record.id, value: fingerprint)
        }
        try persist(record)
    }

    private func installationBinding(from authorization: Data) throws -> Data {
        let value = try DiagnosticsDeterministicCBOR.decode(authorization)
        guard let binding = value.bytes(for: 8, count: 32) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return binding
    }

    private func requiredRecord(_ id: String) throws -> DiagnosticsPairingRecord {
        guard let record = records.first(where: { $0.id == id }) else {
            throw DiagnosticsProtocolError.unavailable
        }
        return record
    }

    private func requiredActiveRecord(_ id: String) throws -> DiagnosticsPairingRecord {
        let record = try requiredRecord(id)
        guard [.active, .namespaceActive].contains(record.state) else {
            throw DiagnosticsProtocolError.unavailable
        }
        return record
    }

    private func proposedInstallationAppKey(
        for record: DiagnosticsPairingRecord
    ) throws -> Curve25519.Signing.PrivateKey {
        let installation = try credentialStore.installationCredential()
        let installationKey = installation.privateKey
        let installationPublic = installationKey.publicKey.rawRepresentation
        if installationPublic != record.appPublicKey {
            // A prior explicit rotation already selected this installation-wide
            // key. This exact authorization catches up independently.
            return installationKey
        }

        let stableStates: Set<DiagnosticsPairingRecord.State> = [.active, .namespaceActive]
        guard records.filter({ $0.state != .revoked }).allSatisfy({ candidate in
            candidate.appPublicKey == installationPublic &&
                stableStates.contains(candidate.state) &&
                candidate.pendingLifecycle == nil
        }) else {
            // Do not create another key generation while any authorization is
            // pending or still bound to the previous installation key.
            throw DiagnosticsProtocolError.conflict
        }
        let proposed = Curve25519.Signing.PrivateKey()
        try credentialStore.advanceInstallationAppKey(
            expected: installationKey,
            proposed: proposed
        )
        return proposed
    }

    private func makeTransport(_ record: DiagnosticsPairingRecord) throws -> any DiagnosticsTransporting {
        try transportFactory(record.endpointHost, record.endpointPort, record.tlsSPKIPin)
    }

    private func invalidateCapability(_ recordID: String) {
        capabilityStates[recordID] = .unavailable
        capabilityValidUntil.removeValue(forKey: recordID)
    }

    private func makeLocalDeadline(
        expiresAt: UInt64
    ) throws -> DiagnosticsPairingRecord.LocalDeadline {
        let wall = try wallSeconds()
        guard expiresAt > wall else { throw DiagnosticsProtocolError.expired }
        let lifetime = min(
            expiresAt - wall,
            DiagnosticsPairingProtocol.maximumLifetime
        )
        let continuous = continuousNow()
        guard continuous.isFinite, continuous >= 0 else {
            throw DiagnosticsProtocolError.unavailable
        }
        let continuousExpiry = continuous + TimeInterval(lifetime)
        guard continuousExpiry.isFinite else {
            throw DiagnosticsProtocolError.unavailable
        }
        return DiagnosticsPairingRecord.LocalDeadline(
            createdWallSeconds: wall,
            createdContinuousSeconds: continuous,
            expiresContinuousSeconds: continuousExpiry
        )
    }

    private func makeContinuousDeadline(
        expiresAt: UInt64,
        maximumLifetime: UInt64
    ) throws -> TimeInterval {
        let wall = try wallSeconds()
        guard expiresAt > wall else { throw DiagnosticsProtocolError.expired }
        let lifetime = min(expiresAt - wall, maximumLifetime)
        let continuous = continuousNow()
        let deadline = continuous + TimeInterval(lifetime)
        guard continuous.isFinite, continuous >= 0, deadline.isFinite else {
            throw DiagnosticsProtocolError.unavailable
        }
        return deadline
    }

    private func lifecycleExpiry(
        _ message: DiagnosticsPairingProtocol.Message
    ) throws -> UInt64 {
        guard let expires = message.value.unsigned(for: 22) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return expires
    }

    private func messageExpiry(_ data: Data, label: UInt64) throws -> UInt64 {
        let value = try DiagnosticsDeterministicCBOR.decode(data)
        guard let expires = value.unsigned(for: label) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return expires
    }

    private func requireLocalDeadline(_ record: DiagnosticsPairingRecord) throws {
        guard let deadline = record.localDeadline,
              !localDeadlineExpired(deadline) else {
            throw DiagnosticsProtocolError.expired
        }
    }

    private func localDeadlineExpired(_ deadline: DiagnosticsPairingRecord.LocalDeadline) -> Bool {
        guard let wall = try? wallSeconds() else { return true }
        let continuous = continuousNow()
        guard continuous.isFinite,
              continuous >= deadline.createdContinuousSeconds,
              continuous < deadline.expiresContinuousSeconds else {
            return true
        }
        let wallElapsed = Double(wall) - Double(deadline.createdWallSeconds)
        let continuousElapsed = continuous - deadline.createdContinuousSeconds
        return abs(wallElapsed - continuousElapsed) >
            Double(DiagnosticsPairingProtocol.maximumClockSkew)
    }

    private func wallSeconds() throws -> UInt64 {
        let seconds = now().timeIntervalSince1970.rounded(.down)
        guard seconds >= 0, seconds < Double(UInt64.max) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return UInt64(seconds)
    }

    private func persist(_ record: DiagnosticsPairingRecord) throws {
        try credentialStore.save(record)
        if let index = records.firstIndex(where: { $0.id == record.id }) {
            records[index] = record
        } else {
            records.append(record)
            records.sort { $0.id < $1.id }
        }
        hasInstallationMarker = true
        hasInstallationCredential = true
    }

    private func perform(_ operation: () async throws -> Void) async {
        guard !isBusy else { return }
        isBusy = true
        lastError = nil
        defer { isBusy = false }
        do {
            try await operation()
        } catch let error as DiagnosticsProtocolError {
            lastError = error
            if error == .recoveryRequired { notice = .recoveryRequired }
        } catch {
            lastError = .unavailable
        }
    }
}

enum DiagnosticsContinuousClock {
    private static let timebase: mach_timebase_info_data_t = {
        var info = mach_timebase_info_data_t()
        mach_timebase_info(&info)
        return info
    }()

    static func seconds() -> TimeInterval {
        let ticks = mach_continuous_time()
        return Double(ticks) * Double(timebase.numer) / Double(timebase.denom) / 1_000_000_000
    }
}

enum DiagnosticsNamespaceFileReader {
    static func read(folderPath: String, components: [String]) throws -> Data {
        guard !folderPath.isEmpty,
              !components.isEmpty,
              components.count <= 5,
              components.allSatisfy({ !$0.isEmpty && $0 != "." && $0 != ".." && !$0.contains("/") }) else {
            throw DiagnosticsProtocolError.unsupported
        }
        let rootDescriptor = open(folderPath, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
        guard rootDescriptor >= 0 else { throw DiagnosticsProtocolError.unsupported }
        var descriptors = [rootDescriptor]
        defer { descriptors.reversed().forEach { close($0) } }

        var current = rootDescriptor
        for component in components.dropLast() {
            let next = component.withCString {
                openat(current, $0, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
            }
            guard next >= 0 else { throw DiagnosticsProtocolError.unavailable }
            descriptors.append(next)
            current = next
        }
        let file = components.last!.withCString {
            openat(current, $0, O_RDONLY | O_CLOEXEC | O_NOFOLLOW)
        }
        guard file >= 0 else { throw DiagnosticsProtocolError.unavailable }
        descriptors.append(file)

        var status = stat()
        guard fstat(file, &status) == 0,
              (status.st_mode & S_IFMT) == S_IFREG,
              status.st_nlink == 1,
              status.st_size > 0,
              status.st_size <= DiagnosticsDeterministicCBOR.maximumMessageBytes else {
            throw DiagnosticsProtocolError.conflict
        }
        var data = Data(count: Int(status.st_size))
        let bytesRead = data.withUnsafeMutableBytes { buffer in
            Darwin.read(file, buffer.baseAddress, buffer.count)
        }
        guard bytesRead == data.count else { throw DiagnosticsProtocolError.conflict }
        var extra: UInt8 = 0
        guard Darwin.read(file, &extra, 1) == 0 else { throw DiagnosticsProtocolError.conflict }
        return data
    }
}

import Foundation
import Observation
import UIKit

struct SyncPathTargetID: Hashable, Sendable {
    let deviceID: String?
    let folderID: String
}

struct SyncPathDeviceSnapshot: Equatable, Sendable {
    let id: String
    let connected: Bool
    let paused: Bool
}

struct SyncPathFolderSnapshot: Equatable, Sendable {
    let id: String
    let type: String
    let paused: Bool
    let deviceIDs: [String]
}

enum SyncPathUnsupportedReason: Equatable, Sendable {
    case folderNotShared
    case multiplePeers
    case sendOnlyFolder
    case encryptedFolder
    case unknownFolderType
    case folderPaused
    case devicePaused
    case unknownDevice
}

enum SyncPathUnavailableReason: Equatable, Sendable {
    case deviceOffline
}

enum SyncPathInterruptionReason: Equatable, Sendable {
    case protectedDataUnavailable
    case engineStopped
    case engineRestarted
    case unreadableBridgeResponse
}

enum SyncPathCancellationReason: Equatable, Sendable {
    case user
    case viewLeft
    case appLifecycle
}

struct SyncPathScopedEvidence: Equatable, Sendable {
    let checkID: UUID
    let targetID: SyncPathTargetID
    let observedAt: Date
    let correlationID: UUID?

    init(
        checkID: UUID,
        targetID: SyncPathTargetID,
        observedAt: Date,
        correlationID: UUID? = nil
    ) {
        self.checkID = checkID
        self.targetID = targetID
        self.observedAt = observedAt
        self.correlationID = correlationID
    }
}

enum SyncPathObservedSignal: Equatable, Sendable {
    case backgroundSyncStarted(SyncPathScopedEvidence)
    case syncthingQueried(SyncPathScopedEvidence)
    case localScanCompleted(SyncPathScopedEvidence)
    case remoteIndexObserved(SyncPathScopedEvidence)
    case folderIdle(SyncPathScopedEvidence)
    case localDataProgress(SyncPathScopedEvidence)
    case uploadConfirmed(SyncPathScopedEvidence)
    case downloadConfirmed(SyncPathScopedEvidence)
}

/// Exact proof fields for one check and one server/folder target. There is no
/// aggregate success flag. A roundtrip exists only when independently confirmed
/// upload and download evidence carry the same in-memory correlation value.
struct SyncPathTargetProof: Equatable, Sendable {
    let checkID: UUID
    let targetID: SyncPathTargetID
    let startedAt: Date

    private(set) var backgroundSyncStartedAt: Date?
    private(set) var syncthingQueriedAt: Date?
    private(set) var localDataProgressObservedAt: Date?
    private(set) var uploadConfirmedAt: Date?
    private(set) var downloadConfirmedAt: Date?
    private(set) var roundTripConfirmedAt: Date?

    private var uploadCorrelationID: UUID?
    private var downloadCorrelationID: UUID?

    init(checkID: UUID, targetID: SyncPathTargetID, startedAt: Date) {
        self.checkID = checkID
        self.targetID = targetID
        self.startedAt = startedAt
    }

    mutating func observe(_ signal: SyncPathObservedSignal) {
        let evidence: SyncPathScopedEvidence
        switch signal {
        case .backgroundSyncStarted(let value),
             .syncthingQueried(let value),
             .localScanCompleted(let value),
             .remoteIndexObserved(let value),
             .folderIdle(let value),
             .localDataProgress(let value),
             .uploadConfirmed(let value),
             .downloadConfirmed(let value):
            evidence = value
        }

        guard evidence.checkID == checkID,
              evidence.targetID == targetID,
              evidence.observedAt >= startedAt else {
            return
        }

        switch signal {
        case .backgroundSyncStarted:
            backgroundSyncStartedAt = newest(backgroundSyncStartedAt, evidence.observedAt)
        case .syncthingQueried:
            syncthingQueriedAt = newest(syncthingQueriedAt, evidence.observedAt)
        case .localDataProgress:
            localDataProgressObservedAt = newest(localDataProgressObservedAt, evidence.observedAt)
        case .uploadConfirmed:
            guard let correlationID = evidence.correlationID else { return }
            if let uploadConfirmedAt, evidence.observedAt <= uploadConfirmedAt { return }
            uploadConfirmedAt = evidence.observedAt
            uploadCorrelationID = correlationID
            updateRoundTripIfComplete()
        case .downloadConfirmed:
            guard let correlationID = evidence.correlationID else { return }
            if let downloadConfirmedAt, evidence.observedAt <= downloadConfirmedAt { return }
            downloadConfirmedAt = evidence.observedAt
            downloadCorrelationID = correlationID
            updateRoundTripIfComplete()
        case .localScanCompleted, .remoteIndexObserved, .folderIdle:
            // These are useful diagnostics, but none is data progress or a
            // directional acknowledgement. Deliberately no stronger field.
            break
        }
    }

    private mutating func updateRoundTripIfComplete() {
        guard let uploadConfirmedAt,
              let downloadConfirmedAt,
              let uploadCorrelationID,
              uploadCorrelationID == downloadCorrelationID,
              downloadConfirmedAt >= uploadConfirmedAt else { return }
        roundTripConfirmedAt = newest(roundTripConfirmedAt, downloadConfirmedAt)
    }

    private func newest(_ current: Date?, _ candidate: Date) -> Date {
        max(current ?? .distantPast, candidate)
    }
}

/// The expert UI renders every stage from this fixed list so local progress can
/// never replace or visually collapse the independent directional proofs.
enum SyncPathDiagnosticStage: CaseIterable, Hashable, Sendable {
    case syncthingQueried
    case localDataProgress
    case upload
    case download
    case roundTrip

    var title: String {
        switch self {
        case .syncthingQueried: return L10n.tr("Sync engine checked")
        case .localDataProgress: return L10n.tr("Local data progress")
        case .upload: return L10n.tr("Upload confirmed")
        case .download: return L10n.tr("Download confirmed")
        case .roundTrip: return L10n.tr("Full roundtrip confirmed")
        }
    }

    func timestamp(in proof: SyncPathTargetProof) -> Date? {
        switch self {
        case .syncthingQueried: return proof.syncthingQueriedAt
        case .localDataProgress: return proof.localDataProgressObservedAt
        case .upload: return proof.uploadConfirmedAt
        case .download: return proof.downloadConfirmedAt
        case .roundTrip: return proof.roundTripConfirmedAt
        }
    }
}

enum SyncPathTargetStatus: Equatable, Sendable {
    case checking
    case localDataProgressObserved
    case timedOut
    case cancelled(SyncPathCancellationReason)
    case interrupted(SyncPathInterruptionReason)
    case unsupported(SyncPathUnsupportedReason)
    case unavailable(SyncPathUnavailableReason)
    case conflictingCheck
}

struct SyncPathTargetResult: Equatable, Sendable {
    let targetID: SyncPathTargetID
    var proof: SyncPathTargetProof
    var status: SyncPathTargetStatus
}

enum SyncPathCheckPhase: Equatable, Sendable {
    case checking
    case completed
    case cancelled
    case interrupted
    case conflicting
}

struct SyncPathCheckSession: Equatable, Sendable {
    let checkID: UUID
    let startedAt: Date
    var completedAt: Date?
    var phase: SyncPathCheckPhase
    var attempt: Int
    let maximumAttempts: Int
    var results: [SyncPathTargetID: SyncPathTargetResult]

    mutating func cancel(reason: SyncPathCancellationReason, at date: Date) {
        for targetID in Array(results.keys) where results[targetID]?.status == .checking {
            results[targetID]?.status = .cancelled(reason)
        }
        completedAt = date
        phase = .cancelled
    }
}

enum SyncPathPresentedState: Equatable, Sendable {
    case checking
    case localDataProgressObserved
    case stale
    case incomplete
    case cancelled
    case interrupted
    case unsupported
    case unavailable
    case conflicting

    var userFacingTitle: String {
        switch self {
        case .checking: return L10n.tr("Synchronization path is being checked")
        case .localDataProgressObserved: return L10n.tr("Local data progress was observed")
        case .stale: return L10n.tr("The last successful check is out of date")
        case .incomplete: return L10n.tr("The check could not be completed")
        case .cancelled: return L10n.tr("The check was cancelled")
        case .interrupted: return L10n.tr("The check was interrupted")
        case .unsupported: return L10n.tr("This server or folder does not support this check yet")
        case .unavailable: return L10n.tr("Try again later")
        case .conflicting: return L10n.tr("Another synchronization check is already running")
        }
    }

    var userFacingDetail: String {
        switch self {
        case .checking:
            return L10n.tr("VaultSync is waiting briefly for new local data progress.")
        case .localDataProgressObserved:
            return L10n.tr("A new incoming file change was applied on this iPhone during the check.")
        case .stale:
            return L10n.tr("Run the check again before relying on this result.")
        case .incomplete:
            return L10n.tr("No new local data progress was observed in time. This is not a synchronization failure.")
        case .cancelled:
            return L10n.tr("No result was inferred after the check stopped.")
        case .interrupted:
            return L10n.tr("Keep VaultSync open and unlocked, then try again.")
        case .unsupported:
            return L10n.tr("The current server or folder setup cannot be checked honestly yet.")
        case .unavailable:
            return L10n.tr("The server is not available for this check right now.")
        case .conflicting:
            return L10n.tr("Wait for the current check to finish or cancel it first.")
        }
    }
}

enum SyncPathCheckPresentation {
    static let freshnessWindow: TimeInterval = 15 * 60

    static func state(
        for result: SyncPathTargetResult,
        now: Date,
        freshnessWindow: TimeInterval = freshnessWindow
    ) -> SyncPathPresentedState {
        if let observedAt = result.proof.localDataProgressObservedAt {
            return now.timeIntervalSince(observedAt) < freshnessWindow
                ? .localDataProgressObserved
                : .stale
        }

        switch result.status {
        case .checking: return .checking
        case .localDataProgressObserved: return .incomplete
        case .timedOut: return .incomplete
        case .cancelled: return .cancelled
        case .interrupted: return .interrupted
        case .unsupported: return .unsupported
        case .unavailable: return .unavailable
        case .conflictingCheck: return .conflicting
        }
    }
}

struct SyncPathCheckPolicy: Equatable, Sendable {
    let retryDelays: [Duration]

    static let diagnostics = SyncPathCheckPolicy(
        retryDelays: [.seconds(2), .seconds(4), .seconds(8), .seconds(12)]
    )

    var maximumAttempts: Int { retryDelays.count + 1 }
}

struct SyncPathBridgeEvent: Decodable, Equatable, Sendable {
    let id: Int
    let type: String
    let time: String
    let data: [String: String]?

    func localDataProgressDate(startedAt: Date, cursor: Int) -> Date? {
        guard id > cursor,
              type == "ItemFinished",
              let data,
              data["type"] == "file",
              data["action"] == "update" || data["action"] == "delete",
              data["error", default: ""].isEmpty,
              let observedAt = SyncBridgeService.parseBridgeTimestamp(time),
              observedAt >= startedAt else {
            return nil
        }
        return data["folder"]?.isEmpty == false ? observedAt : nil
    }

    var folderID: String? { data?["folder"] }
}

actor SyncPathCheckLease {
    static let shared = SyncPathCheckLease()

    private var activeCheckID: UUID?

    func acquire(checkID: UUID) -> Bool {
        guard activeCheckID == nil else { return false }
        activeCheckID = checkID
        return true
    }

    func release(checkID: UUID) {
        guard activeCheckID == checkID else { return }
        activeCheckID = nil
    }
}

enum SyncPathTargetBuilding {
    static func makeResults(
        checkID: UUID,
        startedAt: Date,
        devices: [SyncPathDeviceSnapshot],
        folders: [SyncPathFolderSnapshot]
    ) -> [SyncPathTargetID: SyncPathTargetResult] {
        let deviceByID = Dictionary(devices.map { ($0.id, $0) }, uniquingKeysWith: { _, newest in newest })
        var results: [SyncPathTargetID: SyncPathTargetResult] = [:]

        for folder in folders.sorted(by: { $0.id < $1.id }) {
            let deviceIDs = Array(Set(folder.deviceIDs)).sorted()
            let targetDeviceIDs: [String?] = deviceIDs.isEmpty ? [nil] : deviceIDs.map(Optional.some)

            for deviceID in targetDeviceIDs {
                let targetID = SyncPathTargetID(deviceID: deviceID, folderID: folder.id)
                let status = initialStatus(
                    folder: folder,
                    targetDeviceID: deviceID,
                    distinctDeviceCount: deviceIDs.count,
                    deviceByID: deviceByID
                )
                results[targetID] = SyncPathTargetResult(
                    targetID: targetID,
                    proof: SyncPathTargetProof(checkID: checkID, targetID: targetID, startedAt: startedAt),
                    status: status
                )
            }
        }
        return results
    }

    private static func initialStatus(
        folder: SyncPathFolderSnapshot,
        targetDeviceID: String?,
        distinctDeviceCount: Int,
        deviceByID: [String: SyncPathDeviceSnapshot]
    ) -> SyncPathTargetStatus {
        if folder.paused { return .unsupported(.folderPaused) }

        switch folder.type.lowercased() {
        case "sendreceive", "receiveonly":
            break
        case "sendonly":
            return .unsupported(.sendOnlyFolder)
        case "receiveencrypted":
            return .unsupported(.encryptedFolder)
        default:
            return .unsupported(.unknownFolderType)
        }

        guard distinctDeviceCount > 0, let targetDeviceID else {
            return .unsupported(.folderNotShared)
        }
        guard distinctDeviceCount == 1 else {
            return .unsupported(.multiplePeers)
        }
        guard let device = deviceByID[targetDeviceID] else {
            return .unsupported(.unknownDevice)
        }
        if device.paused { return .unsupported(.devicePaused) }
        if !device.connected { return .unavailable(.deviceOffline) }
        return .checking
    }
}

struct SyncPathCheckEnvironment: Sendable {
    let now: @Sendable () -> Date
    let isEngineRunning: @Sendable () async -> Bool
    let eventStreamGeneration: @Sendable () async -> Int64
    let eventsSince: @Sendable (_ cursor: Int) async -> String
    let sleep: @Sendable (_ duration: Duration) async throws -> Void

    static let live = SyncPathCheckEnvironment(
        now: { Date() },
        isEngineRunning: {
            await Task.detached(priority: .utility) { SyncBridgeService.isRunning() }.value
        },
        eventStreamGeneration: {
            await Task.detached(priority: .utility) { SyncBridgeService.eventStreamGeneration() }.value
        },
        eventsSince: { cursor in
            await Task.detached(priority: .utility) {
                SyncBridgeService.getEventsSince(lastID: cursor)
            }.value
        },
        sleep: { try await Task.sleep(for: $0) }
    )
}

enum SyncPathChecking {
    typealias StateDidChange = @Sendable (SyncPathCheckSession) async -> Void

    static func run(
        checkID: UUID = UUID(),
        devices: [SyncPathDeviceSnapshot],
        folders: [SyncPathFolderSnapshot],
        protectedDataAvailable: Bool,
        policy: SyncPathCheckPolicy = .diagnostics,
        environment: SyncPathCheckEnvironment = .live,
        lease: SyncPathCheckLease = .shared,
        stateDidChange: StateDidChange = { _ in }
    ) async -> SyncPathCheckSession {
        let startedAt = environment.now()
        var session = SyncPathCheckSession(
            checkID: checkID,
            startedAt: startedAt,
            completedAt: nil,
            phase: .checking,
            attempt: 0,
            maximumAttempts: policy.maximumAttempts,
            results: SyncPathTargetBuilding.makeResults(
                checkID: checkID,
                startedAt: startedAt,
                devices: devices,
                folders: folders
            )
        )

        if Task.isCancelled {
            session.cancel(reason: .user, at: environment.now())
            await stateDidChange(session)
            return session
        }

        guard await lease.acquire(checkID: checkID) else {
            finishActiveTargets(&session, status: .conflictingCheck, phase: .conflicting, at: environment.now())
            await stateDidChange(session)
            return session
        }

        let result = await runWithLease(
            session: session,
            protectedDataAvailable: protectedDataAvailable,
            policy: policy,
            environment: environment,
            stateDidChange: stateDidChange
        )
        await lease.release(checkID: checkID)
        return result
    }

    private static func runWithLease(
        session initialSession: SyncPathCheckSession,
        protectedDataAvailable: Bool,
        policy: SyncPathCheckPolicy,
        environment: SyncPathCheckEnvironment,
        stateDidChange: StateDidChange
    ) async -> SyncPathCheckSession {
        var session = initialSession
        await stateDidChange(session)

        guard !Task.isCancelled else {
            session.cancel(reason: .user, at: environment.now())
            await stateDidChange(session)
            return session
        }

        guard protectedDataAvailable else {
            interrupt(&session, reason: .protectedDataUnavailable, at: environment.now())
            await stateDidChange(session)
            return session
        }

        let activeTargetIDs = session.results.keys.filter { session.results[$0]?.status == .checking }
        guard !activeTargetIDs.isEmpty else {
            session.completedAt = environment.now()
            session.phase = .completed
            await stateDidChange(session)
            return session
        }

        guard await environment.isEngineRunning() else {
            interrupt(&session, reason: .engineStopped, at: environment.now())
            await stateDidChange(session)
            return session
        }

        let generationBeforeBaseline = await environment.eventStreamGeneration()
        let baselineJSON = await environment.eventsSince(0)
        let generationAfterBaseline = await environment.eventStreamGeneration()
        guard generationBeforeBaseline > 0,
              generationBeforeBaseline == generationAfterBaseline else {
            interrupt(&session, reason: .engineRestarted, at: environment.now())
            await stateDidChange(session)
            return session
        }
        guard let baselineEvents = decodeEvents(baselineJSON) else {
            interrupt(&session, reason: .unreadableBridgeResponse, at: environment.now())
            await stateDidChange(session)
            return session
        }

        var cursor = baselineEvents.map(\.id).max() ?? 0
        for targetID in activeTargetIDs {
            let evidence = SyncPathScopedEvidence(
                checkID: session.checkID,
                targetID: targetID,
                observedAt: environment.now()
            )
            session.results[targetID]?.proof.observe(.syncthingQueried(evidence))
        }

        await stateDidChange(session)

        for attempt in 1...policy.maximumAttempts {
            if Task.isCancelled {
                session.cancel(reason: .user, at: environment.now())
                await stateDidChange(session)
                return session
            }
            guard await environment.isEngineRunning() else {
                interrupt(&session, reason: .engineStopped, at: environment.now())
                await stateDidChange(session)
                return session
            }

            session.attempt = attempt
            await stateDidChange(session)

            let generationBeforePoll = await environment.eventStreamGeneration()
            let eventsJSON = await environment.eventsSince(cursor)
            let generationAfterPoll = await environment.eventStreamGeneration()
            guard generationBeforePoll == generationBeforeBaseline,
                  generationAfterPoll == generationBeforeBaseline else {
                interrupt(&session, reason: .engineRestarted, at: environment.now())
                await stateDidChange(session)
                return session
            }
            guard let events = decodeEvents(eventsJSON) else {
                interrupt(&session, reason: .unreadableBridgeResponse, at: environment.now())
                await stateDidChange(session)
                return session
            }
            guard !Task.isCancelled else {
                session.cancel(reason: .user, at: environment.now())
                await stateDidChange(session)
                return session
            }

            let previousCursor = cursor
            if let latestID = events.map(\.id).max() {
                cursor = max(cursor, latestID)
            }
            for event in events {
                guard let observedAt = event.localDataProgressDate(
                    startedAt: session.startedAt,
                    cursor: previousCursor
                ) else { continue }
                guard let folderID = event.folderID else { continue }
                for targetID in activeTargetIDs where targetID.folderID == folderID {
                    guard session.results[targetID]?.status == .checking else { continue }
                    let evidence = SyncPathScopedEvidence(
                        checkID: session.checkID,
                        targetID: targetID,
                        observedAt: observedAt
                    )
                    session.results[targetID]?.proof.observe(.localDataProgress(evidence))
                    session.results[targetID]?.status = .localDataProgressObserved
                }
            }
            await stateDidChange(session)

            if !session.results.values.contains(where: { $0.status == .checking }) {
                session.completedAt = environment.now()
                session.phase = .completed
                await stateDidChange(session)
                return session
            }

            guard attempt < policy.maximumAttempts else { break }
            do {
                try await environment.sleep(policy.retryDelays[attempt - 1])
            } catch {
                session.cancel(reason: .user, at: environment.now())
                await stateDidChange(session)
                return session
            }
        }

        finishActiveTargets(&session, status: .timedOut, phase: .completed, at: environment.now())
        await stateDidChange(session)
        return session
    }

    private static func decodeEvents(_ json: String) -> [SyncPathBridgeEvent]? {
        guard let data = json.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode([SyncPathBridgeEvent].self, from: data)
    }

    private static func finishActiveTargets(
        _ session: inout SyncPathCheckSession,
        status: SyncPathTargetStatus,
        phase: SyncPathCheckPhase,
        at date: Date
    ) {
        for targetID in Array(session.results.keys) where session.results[targetID]?.status == .checking {
            session.results[targetID]?.status = status
        }
        session.completedAt = date
        session.phase = phase
    }

    private static func interrupt(
        _ session: inout SyncPathCheckSession,
        reason: SyncPathInterruptionReason,
        at date: Date
    ) {
        finishActiveTargets(&session, status: .interrupted(reason), phase: .interrupted, at: date)
    }
}

@MainActor
@Observable
final class SyncPathCheckController {
    private(set) var session: SyncPathCheckSession?
    private(set) var isRunning = false
    private(set) var isCancellationPending = false
    @ObservationIgnored private var checkTask: Task<Void, Never>?
    @ObservationIgnored private var activeCheckID: UUID?
    @ObservationIgnored private var pendingCancellationReason: SyncPathCancellationReason?
    @ObservationIgnored private var discardResultOnFinish = false
    @ObservationIgnored private let environment: SyncPathCheckEnvironment
    @ObservationIgnored private let policy: SyncPathCheckPolicy
    @ObservationIgnored private let lease: SyncPathCheckLease

    init(
        environment: SyncPathCheckEnvironment = .live,
        policy: SyncPathCheckPolicy = .diagnostics,
        lease: SyncPathCheckLease = .shared
    ) {
        self.environment = environment
        self.policy = policy
        self.lease = lease
    }

    @discardableResult
    func start(
        devices: [SyncthingManager.DeviceInfo],
        folders: [SyncthingManager.FolderInfo],
        protectedDataAvailable: Bool = UIApplication.shared.isProtectedDataAvailable
    ) -> Bool {
        guard !isRunning, checkTask == nil else { return false }

        let checkID = UUID()
        isRunning = true
        isCancellationPending = false
        activeCheckID = checkID
        pendingCancellationReason = nil
        discardResultOnFinish = false

        let deviceSnapshots = devices.map {
            SyncPathDeviceSnapshot(id: $0.deviceID, connected: $0.connected, paused: $0.paused)
        }
        let folderSnapshots = folders.map {
            SyncPathFolderSnapshot(
                id: $0.id,
                type: $0.type,
                paused: $0.paused,
                deviceIDs: $0.deviceIDs
            )
        }

        let checkEnvironment = environment
        let checkPolicy = policy
        let checkLease = lease
        let startedAt = checkEnvironment.now()
        session = SyncPathCheckSession(
            checkID: checkID,
            startedAt: startedAt,
            completedAt: nil,
            phase: .checking,
            attempt: 0,
            maximumAttempts: checkPolicy.maximumAttempts,
            results: SyncPathTargetBuilding.makeResults(
                checkID: checkID,
                startedAt: startedAt,
                devices: deviceSnapshots,
                folders: folderSnapshots
            )
        )

        checkTask = Task { [weak self] in
            let result = await SyncPathChecking.run(
                checkID: checkID,
                devices: deviceSnapshots,
                folders: folderSnapshots,
                protectedDataAvailable: protectedDataAvailable,
                policy: checkPolicy,
                environment: checkEnvironment,
                lease: checkLease,
                stateDidChange: { [weak self] update in
                    await self?.apply(update)
                }
            )
            self?.finish(result)
        }
        return true
    }

    func cancel(reason: SyncPathCancellationReason) {
        guard let checkTask, pendingCancellationReason == nil else { return }
        pendingCancellationReason = reason
        isCancellationPending = true
        checkTask.cancel()
        if var session, session.checkID == activeCheckID {
            session.cancel(reason: reason, at: environment.now())
            self.session = session
        }
    }

    func reset() {
        if let checkTask {
            pendingCancellationReason = pendingCancellationReason ?? .user
            isCancellationPending = true
            discardResultOnFinish = true
            checkTask.cancel()
        }
        session = nil
    }

    private func apply(_ update: SyncPathCheckSession) {
        guard activeCheckID == update.checkID, !discardResultOnFinish else { return }
        guard let pendingCancellationReason else {
            session = update
            return
        }
        var cancelled = update
        cancelled.cancel(reason: pendingCancellationReason, at: environment.now())
        session = cancelled
    }

    private func finish(_ result: SyncPathCheckSession) {
        guard activeCheckID == result.checkID else { return }
        if discardResultOnFinish {
            session = nil
        } else if let pendingCancellationReason {
            var cancelled = result
            cancelled.cancel(reason: pendingCancellationReason, at: environment.now())
            session = cancelled
        } else {
            session = result
        }
        activeCheckID = nil
        pendingCancellationReason = nil
        discardResultOnFinish = false
        checkTask = nil
        isCancellationPending = false
        isRunning = false
    }
}

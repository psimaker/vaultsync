import Foundation
import Testing
@testable import VaultSync

@Suite("Conflict notification suppression")
struct ConflictNotificationActionTests {

    @Test("First conflicts (none surfaced yet) alert")
    func firstConflictsAlert() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 28, lastNotifiedCount: 0) == .alert)
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 1, lastNotifiedCount: 0) == .alert)
    }

    @Test("Unchanged count is suppressed — the core of issue #10")
    func unchangedCountSuppressed() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 28, lastNotifiedCount: 28) == .suppress)
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 1, lastNotifiedCount: 1) == .suppress)
    }

    @Test("A rising count alerts (genuinely new conflicts)")
    func risingCountAlerts() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 29, lastNotifiedCount: 28) == .alert)
    }

    @Test("A falling-but-nonzero count refreshes quietly")
    func fallingCountUpdatesQuietly() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 25, lastNotifiedCount: 28) == .updateQuiet)
    }

    @Test("No conflicts left clears the banner")
    func zeroClears() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 0, lastNotifiedCount: 28) == .clear)
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 0, lastNotifiedCount: 0) == .clear)
    }

    @Test("Negative/garbage current count is treated as cleared, never as a post")
    func negativeCurrentClears() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: -3, lastNotifiedCount: 5) == .clear)
    }
}

@Suite("Folder settlement classification")
struct FolderSettlementTests {

    @Test("Idle with no pending work is idle")
    func idleNoWork() {
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .idle)
    }

    @Test("Idle but with pending work is still active (the scan→sync gap)")
    func idleWithPendingIsActive() {
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 3, needBytes: 0, inProgressBytes: 0) == .active)
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 0, needBytes: 4096, inProgressBytes: 0) == .active)
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 0, needBytes: 0, inProgressBytes: 512) == .active)
    }

    @Test("Scanning and syncing are active")
    func scanningSyncingActive() {
        #expect(BackgroundSyncService.folderSettlement(state: "scanning", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .active)
        #expect(BackgroundSyncService.folderSettlement(state: "syncing", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .active)
    }

    @Test("Error is terminal even with outstanding work — lets the deadline loop break early")
    func errorIsTerminal() {
        #expect(BackgroundSyncService.folderSettlement(state: "error", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .errored)
        #expect(BackgroundSyncService.folderSettlement(state: "error", needFiles: 12, needBytes: 9000, inProgressBytes: 1) == .errored)
    }
}

@Suite("Continued-processing completion (#146)")
struct ContinuedProcessingCompletionTests {

    typealias Run = BackgroundSyncService.ContinuedProcessingRun

    private func observation(
        engineRunning: Bool = true,
        sceneActive: Bool = false,
        folders: Run.FolderSnapshot
    ) -> Run.Observation {
        .init(
            engineRunning: engineRunning,
            sceneActive: sceneActive,
            folders: folders
        )
    }

    private func snapshot(
        _ settlements: [String: BackgroundSyncService.FolderSettlement],
        folderIDs: Set<String>? = nil
    ) -> Run.FolderSnapshot {
        .readable(
            folderIDs: folderIDs ?? Set(settlements.keys),
            settlements: settlements
        )
    }

    @Test("Engine death without verified idle fails")
    func issue146EngineDeathWithoutVerifiedIdleFails() {
        var run = Run()

        let effects = run.receive(.observed(.init(
            engineRunning: false,
            sceneActive: false,
            folders: .unreadable
        )))

        #expect(effects == [.complete(.failure(.engineDied))])
    }

    @Test("All expected folders must remain idle through conflict inspection")
    func issue146AllExpectedFoldersIdleSucceedsAfterConflictInspection() {
        var run = Run()
        let idle = observation(folders: snapshot([
            "vault-a": .idle,
            "vault-b": .idle,
        ]))

        #expect(run.receive(.observed(idle)) == [.inspectConflicts(token: 1)])
        #expect(
            run.receive(.conflictInspectionFinished(token: 1, observation: idle))
                == [.complete(.success)]
        )
        #expect(run.receive(.expired(sceneActive: false)).isEmpty)
    }

    @Test("Engine death during the wait is failure")
    func issue146EngineDeathDuringWaitFails() {
        var run = Run()
        let active = observation(folders: snapshot(["vault-a": .active]))

        #expect(run.receive(.observed(active)) == [.wait])
        #expect(
            run.receive(.observed(observation(
                engineRunning: false,
                folders: snapshot(["vault-a": .active])
            ))) == [.complete(.failure(.engineDied))]
        )
    }

    @Test("No configured folders is failure")
    func issue146NoFoldersFails() {
        var run = Run()

        #expect(
            run.receive(.observed(observation(
                folders: snapshot([:], folderIDs: [])
            ))) == [.complete(.failure(.noFolders))]
        )
    }

    @Test("Unreadable folder list or status is failure")
    func issue146UnreadableFolderEvidenceFails() {
        var unreadableListRun = Run()
        #expect(
            unreadableListRun.receive(.observed(observation(folders: .unreadable)))
                == [.complete(.failure(.unreadableFolders))]
        )

        var unreadableStatusRun = Run()
        #expect(
            unreadableStatusRun.receive(.observed(observation(
                folders: snapshot([:], folderIDs: ["vault-a"])
            ))) == [.complete(.failure(.unreadableFolders))]
        )
    }

    @Test("Folder error is failure")
    func issue146FolderErrorFails() {
        var run = Run()

        #expect(
            run.receive(.observed(observation(
                folders: snapshot(["vault-a": .errored])
            ))) == [.complete(.failure(.folderError))]
        )
    }

    @Test("Missing expected folder cannot disappear from the idle proof")
    func issue146MissingExpectedFolderFails() {
        var run = Run()
        #expect(
            run.receive(.observed(observation(folders: snapshot([
                "vault-a": .active,
                "vault-b": .active,
            ])))) == [.wait]
        )

        #expect(
            run.receive(.observed(observation(folders: snapshot([
                "vault-a": .idle,
            ])))) == [.complete(.failure(.expectedFolderMissing))]
        )
    }

    @Test("A newly appearing folder invalidates the fixed proof set")
    func issue146ChangedFolderSetFails() {
        var run = Run()
        #expect(
            run.receive(.observed(observation(
                folders: snapshot(["vault-a": .active])
            ))) == [.wait]
        )

        #expect(
            run.receive(.observed(observation(folders: snapshot([
                "vault-a": .idle,
                "vault-b": .idle,
            ])))) == [.complete(.failure(.folderSetChanged))]
        )
    }

    @Test("Expiration wins during conflict inspection and blocks late success")
    func issue146ExpirationIsFailureAndLateConflictCallbackIsIgnored() {
        var run = Run()
        let idle = observation(folders: snapshot(["vault-a": .idle]))

        #expect(run.receive(.observed(idle)) == [.inspectConflicts(token: 1)])
        #expect(
            run.receive(.expired(sceneActive: false))
                == [.complete(.failure(.expired))]
        )
        #expect(
            run.receive(.conflictInspectionFinished(token: 1, observation: idle)).isEmpty
        )
    }

    @Test("Foreground takeover is distinct from engine death")
    func issue146ForegroundTakeoverFailsWithoutEngineDeathClassification() {
        var run = Run()
        let active = observation(folders: snapshot(["vault-a": .active]))

        #expect(run.receive(.observed(active)) == [.wait])

        #expect(
            run.receive(.observed(observation(
                engineRunning: true,
                sceneActive: true,
                folders: snapshot(["vault-a": .active])
            ))) == [.complete(.failure(.foregroundTakeover))]
        )
    }

    @Test("Cancellation wins during conflict inspection and blocks late success")
    func issue146CancellationIsFailureAndLateConflictCallbackIsIgnored() {
        var run = Run()
        let idle = observation(folders: snapshot(["vault-a": .idle]))

        #expect(run.receive(.observed(idle)) == [.inspectConflicts(token: 1)])
        #expect(
            run.receive(.cancelled(sceneActive: false))
                == [.complete(.failure(.cancelled))]
        )
        #expect(
            run.receive(.conflictInspectionFinished(token: 1, observation: idle)).isEmpty
        )
    }

    @Test("Conflict inspection starts only after idle and must revalidate idle")
    func issue146ConflictInspectionCannotUpgradeUnverifiedWorkToSuccess() {
        var run = Run()
        let active = observation(folders: snapshot(["vault-a": .active]))
        let idle = observation(folders: snapshot(["vault-a": .idle]))

        #expect(run.receive(.observed(active)) == [.wait])
        #expect(run.receive(.observed(idle)) == [.inspectConflicts(token: 1)])
        #expect(
            run.receive(.conflictInspectionFinished(token: 1, observation: active))
                == [.wait]
        )
        #expect(run.receive(.observed(idle)) == [.inspectConflicts(token: 2)])
        #expect(
            run.receive(.conflictInspectionFinished(token: 2, observation: idle))
                == [.complete(.success)]
        )
    }

    @Test("Engine death during conflict inspection is still failure")
    func issue146ConflictInspectionRechecksEngineLiveness() {
        var run = Run()
        let idle = observation(folders: snapshot(["vault-a": .idle]))

        #expect(run.receive(.observed(idle)) == [.inspectConflicts(token: 1)])
        #expect(
            run.receive(.conflictInspectionFinished(
                token: 1,
                observation: observation(
                    engineRunning: false,
                    folders: snapshot(["vault-a": .idle])
                )
            )) == [.complete(.failure(.engineDied))]
        )
    }
}

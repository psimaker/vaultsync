import Testing
import UIKit
@testable import VaultSync

private final class Issue150EffectCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: Int] = [:]

    func record(_ key: String) {
        lock.lock()
        values[key, default: 0] += 1
        lock.unlock()
    }

    func value(_ key: String) -> Int {
        lock.lock()
        defer { lock.unlock() }
        return values[key, default: 0]
    }
}

@Suite("Conflict safety stays fail-closed across Swift surfaces (#150)")
struct ConflictRetentionSafetyIntegrationTests {
    @MainActor
    private func makeManager(
        folderIDs: [String] = ["fixture-folder-a"],
        folderType: String = "sendreceive"
    ) -> SyncthingManager {
        let historyDefaults = TestSupport.makeIsolatedDefaults(label: "Issue150History")
        let manager = SyncthingManager(
            syncHistoryStore: SyncHistoryStore(
                defaults: historyDefaults,
                storageKey: "issue-150-history"
            )
        )
        manager._testSetLastBackgroundSyncOutcome(nil)
        manager._testSetFolders(folderIDs.map {
            .init(
                id: $0,
                label: "Fixture Label",
                path: "/fixture/path",
                type: folderType,
                paused: false,
                deviceIDs: []
            )
        })
        return manager
    }

    private func status(
        state: String = "error",
        reason: String?,
        message: String? = nil,
        path: String? = nil
    ) -> SyncthingManager.FolderStatusInfo {
        .init(payload: .init(
            state: state,
            stateChanged: "2000-01-01T00:00:00Z",
            completionPct: 100,
            globalBytes: 200,
            globalFiles: 2,
            localBytes: 200,
            localFiles: 2,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: reason,
            errorMessage: message,
            errorPath: path,
            errorChanged: "2000-01-01T00:00:00Z"
        ))
    }

    private func liveStatusJSON(
        state: String = "idle",
        reason: String? = nil,
        message: String? = nil,
        path: String? = nil
    ) -> String {
        let payload = SyncBridgeService.FolderStatusPayload(
            state: state,
            stateChanged: "2000-01-01T00:00:00Z",
            completionPct: 100,
            globalBytes: 200,
            globalFiles: 2,
            localBytes: 200,
            localFiles: 2,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: reason,
            errorMessage: message,
            errorPath: path,
            errorChanged: nil
        )
        guard let data = try? JSONEncoder().encode(payload),
              let json = String(data: data, encoding: .utf8) else {
            return ""
        }
        return json
    }

    private func folder(
        _ id: String,
        type: String = "sendreceive"
    ) -> SyncthingManager.FolderInfo {
        .init(
            id: id,
            label: "Fixture Label",
            path: "/fixture/path",
            type: type,
            paused: false,
            deviceIDs: []
        )
    }

    private var conflict: SyncthingManager.ConflictInfo {
        .init(
            originalPath: "fixture-note.md",
            conflictPath: "fixture-note.sync-conflict-20000101-000000-FIXTURE.md",
            conflictDate: "20000101-000000",
            deviceShortID: "FIXTURE"
        )
    }

    @Test("Stopped and unknown live status create only a critical read-only issue (#150)")
    @MainActor
    func stoppedAndUnknownAreDedicatedIssues() {
        let manager = makeManager(folderIDs: ["stopped", "unknown"])
        manager._testSetFolderStatuses([
            "stopped": status(state: "idle", reason: ConflictSafetyPolicy.stoppedReason),
            "unknown": status(
                state: "idle",
                reason: ConflictSafetyPolicy.folderErrorEvidenceUnavailableReason
            ),
        ])

        let issues = manager.unresolvedIssues.filter { $0.kind == .conflictRetentionSafety }
        #expect(issues.count == 2)
        #expect(issues.allSatisfy { $0.severity == .critical })
        #expect(!manager.unresolvedIssues.contains { $0.kind == .folderErrors })
        #expect(!manager.hasRescanableFolderErrors)
        #expect(manager.folderUserError(folderID: "stopped")?.technicalDetails == nil)
        #expect(manager.folderUserError(folderID: "unknown")?.technicalDetails == nil)
        #expect(issues.allSatisfy { !$0.remediation.localizedCaseInsensitiveContains("Keep Both") })
        #expect(issues.allSatisfy { !$0.remediation.localizedCaseInsensitiveContains("retry") })
    }

    @Test("Normal SendOnly diagnostics stay outside conflict safety gates (#150)")
    @MainActor
    func sendOnlyDiagnosticsRetainTheirExistingSemanticsIssue150() {
        let diagnosticCases: [(
            status: SyncthingManager.FolderStatusInfo,
            category: SyncUserErrorCategory,
            isRescanable: Bool,
            isUnreachable: Bool
        )] = [
            (
                status: status(
                    reason: "permission_denied",
                    message: "fixture permission failure",
                    path: "/fixture/path"
                ),
                category: .permission,
                isRescanable: true,
                isUnreachable: true
            ),
            (
                status: status(
                    reason: "unknown_error",
                    message: "fixture folder marker missing"
                ),
                category: .folderMarkerMissing,
                isRescanable: false,
                isUnreachable: false
            ),
            (
                status: status(reason: "disk_full", message: "fixture disk failure"),
                category: .config,
                isRescanable: true,
                isUnreachable: false
            ),
            (
                status: status(reason: "unknown_error"),
                category: .config,
                isRescanable: true,
                isUnreachable: false
            ),
            (
                status: status(reason: nil, message: "fixture raw detail"),
                category: .config,
                isRescanable: true,
                isUnreachable: false
            ),
        ]

        #expect(SyncthingManager.effectiveConflictSafetyState(
            folderType: "sendonly",
            status: nil
        ) == .clear)

        let missingStatusManager = makeManager(folderType: "sendonly")
        #expect(missingStatusManager.conflictSafetyState(folderID: "fixture-folder-a") == .clear)
        #expect(missingStatusManager.conflictSafetyBlockedFolderIDs.isEmpty)

        for diagnostic in diagnosticCases {
            #expect(SyncthingManager.effectiveConflictSafetyState(
                folderType: "sendonly",
                status: diagnostic.status
            ) == .clear)

            let manager = makeManager(folderType: "sendonly")
            manager._testSetFolderStatuses(["fixture-folder-a": diagnostic.status])

            let safetyState = manager.conflictSafetyState(folderID: "fixture-folder-a")
            #expect(safetyState == .clear)
            #expect(ConflictSafetyPolicy.allowsMutation(for: safetyState))
            #expect(manager.conflictSafetyBlockedFolderIDs.isEmpty)
            #expect(!manager.unresolvedIssues.contains { $0.kind == .conflictRetentionSafety })
            #expect(manager.unresolvedIssues.contains { $0.kind == .folderErrors }
                == !diagnostic.isUnreachable)
            #expect(manager.folderUserError(folderID: "fixture-folder-a")?.category
                == diagnostic.category)
            #expect(manager.hasRescanableFolderErrors == diagnostic.isRescanable)
            #expect(manager.unreachableFolders.contains { $0.id == "fixture-folder-a" }
                == diagnostic.isUnreachable)
        }
    }

    @Test("Fixed conflict safety codes remain global for SendOnly folders (#150)")
    @MainActor
    func sendOnlyFixedSafetyCodesRemainBlockedAndSanitizedIssue150() {
        for (reason, expectedState) in [
            (ConflictSafetyPolicy.stoppedReason, ConflictSafetyPolicy.State.stopped),
            (
                ConflictSafetyPolicy.folderErrorEvidenceUnavailableReason,
                ConflictSafetyPolicy.State.unknown
            ),
            (
                ConflictSafetyPolicy.folderCompletionEvidenceUnavailableReason,
                ConflictSafetyPolicy.State.unknown
            ),
        ] {
            let fixedStatus = status(
                state: "idle",
                reason: reason,
                message: "redaction-probe-detail",
                path: "redaction-probe/path"
            )
            #expect(SyncthingManager.effectiveConflictSafetyState(
                folderType: "sendonly",
                status: fixedStatus
            ) == expectedState)

            let manager = makeManager(folderType: "sendonly")
            manager._testSetFolderStatuses(["fixture-folder-a": fixedStatus])

            #expect(manager.conflictSafetyState(folderID: "fixture-folder-a") == expectedState)
            #expect(manager.conflictSafetyBlockedFolderIDs == ["fixture-folder-a"])
            #expect(manager.unresolvedIssues.contains {
                $0.kind == .conflictRetentionSafety && $0.folderID == "fixture-folder-a"
            })
            #expect(!manager.unresolvedIssues.contains { $0.kind == .folderErrors })
            #expect(manager.folderUserError(folderID: "fixture-folder-a")?.category
                == .conflictRetentionSafetyStop)
            #expect(manager.folderUserError(folderID: "fixture-folder-a")?.technicalDetails == nil)
        }
    }

    @Test("Every conflict recovery is unavailable while rescans keep their safety gate (#150)")
    @MainActor
    func directMutationGatesStopAndUnknown() {
        for reason in [
            ConflictSafetyPolicy.stoppedReason,
            ConflictSafetyPolicy.folderCompletionEvidenceUnavailableReason,
        ] {
            let manager = makeManager(folderType: "sendonly")
            manager._testSetFolderStatuses([
                "fixture-folder-a": status(state: "idle", reason: reason),
            ])
            manager._testSetConflictFiles(["fixture-folder-a": [conflict]])

            #expect(manager.rescanFolder(id: "fixture-folder-a")
                == ConflictSafetyPolicy.engineStopMarker)
            #expect(manager.resolveConflict(
                folderID: "fixture-folder-a",
                conflictFileName: conflict.conflictPath,
                keepConflict: false
            ) == "vaultsync-conflict-recovery-unavailable")
            let keepBoth = manager.keepBothConflict(folderID: "fixture-folder-a", conflict: conflict)
            #expect(keepBoth.error == "vaultsync-conflict-recovery-unavailable")
            #expect(keepBoth.newPath == nil)
            let skip = manager.skipFileAndCleanupConflicts(
                folderID: "fixture-folder-a",
                originalPath: conflict.originalPath
            )
            #expect(skip.error?.category == .conflictRetentionSafetyStop)
            #expect(skip.removedConflicts == 0)
            #expect(manager.conflictFiles["fixture-folder-a"]?.map(\.conflictPath)
                == [conflict.conflictPath])
        }

        let missingStatus = makeManager()
        #expect(missingStatus.resolveConflict(
            folderID: "fixture-folder-a",
            conflictFileName: conflict.conflictPath,
            keepConflict: true
        ) == "vaultsync-conflict-recovery-unavailable")
    }

    @Test("Only fixed safety evidence blocks normal SendOnly controls (#150)")
    func sendOnlyControlGateUsesFixedEvidenceIssue150() {
        let cachedClear = status(state: "idle", reason: nil)
        let liveClear = liveStatusJSON()
        let normalLiveErrors = [
            liveStatusJSON(
                state: "error",
                reason: "permission_denied",
                message: "fixture permission failure",
                path: "/fixture/path"
            ),
            liveStatusJSON(
                state: "error",
                reason: "unknown_error",
                message: "fixture folder marker missing"
            ),
            liveStatusJSON(
                state: "error",
                reason: "disk_full",
                message: "fixture disk failure"
            ),
            liveStatusJSON(state: "error", reason: "unknown_error"),
            liveStatusJSON(state: "error", reason: nil, message: "fixture raw detail"),
        ]

        for reason in [
            ConflictSafetyPolicy.stoppedReason,
            ConflictSafetyPolicy.folderErrorEvidenceUnavailableReason,
            ConflictSafetyPolicy.folderCompletionEvidenceUnavailableReason,
        ] {
            #expect(SyncthingManager.conflictMutationBlockCode(
                folderType: "sendonly",
                cachedStatus: cachedClear,
                liveStatusJSON: liveStatusJSON(reason: reason)
            ) == ConflictSafetyPolicy.engineStopMarker)
        }
        #expect(SyncthingManager.conflictMutationBlockCode(
            folderType: "sendonly",
            cachedStatus: cachedClear,
            liveStatusJSON: "{}"
        ) == nil)
        #expect(SyncthingManager.conflictMutationBlockCode(
            folderType: "sendonly",
            cachedStatus: cachedClear,
            liveStatusJSON: "not-json"
        ) == nil)
        #expect(SyncthingManager.conflictMutationBlockCode(
            folderType: "sendonly",
            cachedStatus: cachedClear,
            liveStatusJSON: liveClear
        ) == nil)
        for liveError in normalLiveErrors {
            #expect(SyncthingManager.conflictMutationBlockCode(
                folderType: "sendonly",
                cachedStatus: cachedClear,
                liveStatusJSON: liveError
            ) == nil)
        }

        let cachedStopped = status(
            state: "idle",
            reason: ConflictSafetyPolicy.stoppedReason
        )
        #expect(SyncthingManager.conflictMutationBlockCode(
            folderType: "sendonly",
            cachedStatus: cachedStopped,
            liveStatusJSON: liveClear
        ) == ConflictSafetyPolicy.engineStopMarker)

        let cachedPermissionError = status(
            reason: "permission_denied",
            message: "fixture permission failure",
            path: "/fixture/path"
        )
        #expect(SyncthingManager.conflictMutationBlockCode(
            folderType: "sendonly",
            cachedStatus: cachedPermissionError,
            liveStatusJSON: liveClear
        ) == nil)

        for folderType in ["sendreceive", "receiveonly", "receiveencrypted", "future-mode"] {
            #expect(SyncthingManager.conflictMutationBlockCode(
                folderType: folderType,
                cachedStatus: cachedClear,
                liveStatusJSON: liveClear
            ) == ConflictSafetyPolicy.engineStopMarker)
        }
    }

    @Test("Sync-filter APIs are read-only while conflict safety is stopped or unknown (#150)")
    @MainActor
    func syncFilterMutationGatesStopAndUnknown() {
        for reason in [
            ConflictSafetyPolicy.stoppedReason,
            ConflictSafetyPolicy.folderErrorEvidenceUnavailableReason,
        ] {
            let manager = makeManager()
            manager._testSetFolderStatuses([
                "fixture-folder-a": status(state: "idle", reason: reason),
            ])

            let errors = [
                manager.setIgnorePatterns(
                    folderID: "fixture-folder-a",
                    patterns: ["fixture-pattern"]
                ),
                manager.togglePreset(
                    .workspace,
                    folderID: "fixture-folder-a",
                    enabled: true
                ),
                manager.addIgnorePatterns(
                    ["fixture-pattern"],
                    folderID: "fixture-folder-a"
                ),
                manager.removeIgnorePatterns(
                    ["fixture-pattern"],
                    folderID: "fixture-folder-a"
                ),
                manager.applyRecommendedFilters(
                    folderID: "fixture-folder-a",
                    enabledPresetIDs: [],
                    detectedPatterns: [],
                    enabledDetectedPatterns: []
                ),
            ]
            #expect(errors.allSatisfy { $0?.category == .conflictRetentionSafetyStop })
        }
    }

    @Test("Startup, regular add, and pending accept have no delayed ignore or rescan mutation (#150, #167)")
    func automaticFolderFollowUpsAreAbsent() throws {
        let source = try productSource("VaultSync/Services/SyncthingManager.swift")
        #expect(!source.contains("hasAppliedStartupIgnores"))
        #expect(!source.contains("applyDefaultIgnoresIfNeeded"))
        #expect(!source.contains("ensureDefaultIgnores"))

        let addFolder = try sourceSection(
            source,
            from: "func addFolder(id:",
            to: "/// Remove a folder by ID."
        )
        #expect(!addFolder.contains("Task.detached"))

        let pendingAccept = try sourceSection(
            source,
            from: "func acceptPendingFolder(folderID:",
            to: "// MARK: - Device rename"
        )
        #expect(!pendingAccept.contains("Task.detached"))
        #expect(!pendingAccept.contains("SyncBridgeService.rescanFolder"))
    }

    @Test("Blocked folders remain available as read-only conflict review destinations (#150)")
    @MainActor
    func blockedFolderIsExcludedFromConflictRouting() {
        let manager = makeManager(folderIDs: ["blocked", "clear"])
        manager._testSetFolderStatuses([
            "blocked": status(state: "idle", reason: ConflictSafetyPolicy.stoppedReason),
            "clear": status(state: "idle", reason: nil),
        ])
        manager._testSetConflictFiles(["blocked": [conflict], "clear": [conflict]])

        #expect(SyncIssuesView.conflictDestination(
            preferredFolderID: "blocked",
            conflictFiles: manager.conflictFiles,
            allowFallback: true
        ) == "blocked")
        #expect(manager.unresolvedIssues.contains {
            $0.kind == .conflicts && $0.folderID == "blocked"
        })
    }

    @Test("Background settlement never treats safety or missing evidence as idle (#150)")
    func backgroundSettlementIsFailClosed() {
        let folders = """
        [{"id":"fixture-folder-a"},{"id":"fixture-folder-b"}]
        """
        let stopped = """
        {"state":"idle","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0,"errorReason":"conflict_retention_safety_stop"}
        """
        let collection = BackgroundSyncService.collectFolderStatusSnapshot(
            foldersJSON: folders,
            statusJSON: { $0 == "fixture-folder-a" ? stopped : "{}" }
        )

        #expect(collection?.allStatusesReadable == false)
        #expect(collection?.settlements == ["fixture-folder-a": .errored])
        #expect(BackgroundSyncService.continuedProcessingFolderSnapshot(
            foldersJSON: folders,
            statusJSON: { $0 == "fixture-folder-a" ? stopped : "{}" }
        ) == .unreadable)
        #expect(BackgroundSyncService.collectFolderStatusSnapshot(
            foldersJSON: "[{\"id\":\"duplicate\"},{\"id\":\"duplicate\"}]",
            statusJSON: { _ in stopped }
        ) == nil)

        for reason in [
            ConflictSafetyPolicy.stoppedReason,
            ConflictSafetyPolicy.folderErrorEvidenceUnavailableReason,
            ConflictSafetyPolicy.folderCompletionEvidenceUnavailableReason,
        ] {
            #expect(BackgroundSyncService.folderSettlement(
                state: "idle",
                needFiles: 0,
                needBytes: 0,
                inProgressBytes: 0,
                errorReason: reason
            ) == .errored)
        }
        #expect(BackgroundSyncService.folderSettlement(
            state: "idle",
            needFiles: 0,
            needBytes: 0,
            inProgressBytes: 0,
            errorReason: nil,
            hasRawErrorDetail: true
        ) == .errored)
    }

    @Test("Automatic background rescan targets only SendOnly folders after full preflight (#150)")
    func backgroundRescanIsFailClosed() {
        let folders = """
        [{"id":"clear","type":"sendonly"},{"id":"blocked","type":"receiveonly"}]
        """
        let sendOnlyFolders = """
        [{"id":"clear","type":"sendonly"},{"id":"also-clear","type":"sendonly"}]
        """
        let clear = """
        {"state":"idle","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0}
        """
        let stopped = """
        {"state":"idle","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0,"errorReason":"conflict_retention_safety_stop"}
        """
        var rescanned: [String] = []
        var mixedStatusReads: [String] = []

        let mixed = BackgroundSyncService.requestFolderRescans(
            foldersJSON: folders,
            statusJSON: {
                mixedStatusReads.append($0)
                return $0 == "clear" ? clear : stopped
            },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(mixed == .rescanned(1))
        #expect(mixedStatusReads == ["clear", "clear"])
        #expect(rescanned == ["clear"])
        #expect(BackgroundSyncService.syncResultForRescanFailure(mixed) == nil)
        rescanned.removeAll()

        var receiveStatusReads = 0
        for folderType in ["sendreceive", "receiveonly", "receiveencrypted", "future-mode"] {
            let receiveBlocked = BackgroundSyncService.requestFolderRescans(
                foldersJSON: "[{\"id\":\"receive\",\"type\":\"\(folderType)\"}]",
                statusJSON: { _ in
                    receiveStatusReads += 1
                    return clear
                },
                rescan: {
                    rescanned.append($0)
                    return nil
                }
            )
            #expect(receiveBlocked == .blocked)
        }
        let missingTypeBlocked = BackgroundSyncService.requestFolderRescans(
            foldersJSON: "[{\"id\":\"missing-type\"}]",
            statusJSON: { _ in
                receiveStatusReads += 1
                return clear
            },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(missingTypeBlocked == .blocked)
        #expect(receiveStatusReads == 0)
        #expect(rescanned.isEmpty)

        for unsafeStatus in [
            stopped,
            """
            {"state":"idle","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0,"errorReason":"folder_error_evidence_unavailable"}
            """,
            """
            {"state":"idle","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0,"errorReason":"folder_completion_evidence_unavailable"}
            """,
        ] {
            let result = BackgroundSyncService.requestFolderRescans(
                foldersJSON: "[{\"id\":\"unknown\",\"type\":\"sendonly\"}]",
                statusJSON: { _ in unsafeStatus },
                rescan: {
                    rescanned.append($0)
                    return nil
                }
            )
            #expect(result == .blocked)
            #expect(rescanned.isEmpty)
            #expect(BackgroundSyncService.syncResultForRescanFailure(result)
                != .alreadyIdle)
        }

        for ordinarySendOnlyStatus in [
            """
            {"state":"error","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0,"errorReason":"generic_error"}
            """,
            """
            {"state":"idle","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0,"errorMessage":"redaction probe"}
            """,
            """
            {"state":"future-state","completionPct":100,"needFiles":0,"needBytes":0,"inProgressBytes":0}
            """,
            "{}",
        ] {
            let ordinaryResult = BackgroundSyncService.requestFolderRescans(
                foldersJSON: "[{\"id\":\"ordinary-send-only\",\"type\":\"sendonly\"}]",
                statusJSON: { _ in ordinarySendOnlyStatus },
                rescan: {
                    rescanned.append($0)
                    return nil
                }
            )
            #expect(ordinaryResult == .rescanned(1))
            #expect(rescanned == ["ordinary-send-only"])
            rescanned.removeAll()
        }

        var statusReads = 0
        let changedAtGate = BackgroundSyncService.requestFolderRescans(
            foldersJSON: "[{\"id\":\"changed-at-gate\",\"type\":\"sendonly\"}]",
            statusJSON: { _ in
                statusReads += 1
                return statusReads == 1 ? clear : stopped
            },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(changedAtGate == .blocked)
        #expect(statusReads == 2)
        #expect(rescanned.isEmpty)

        let clearResult = BackgroundSyncService.requestFolderRescans(
            foldersJSON: sendOnlyFolders,
            statusJSON: { _ in clear },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(clearResult == .rescanned(2))
        #expect(rescanned == ["clear", "also-clear"])
        #expect(BackgroundSyncService.syncResultForRescanFailure(clearResult) == nil)
    }

    @Test("Coalesced and terminal background success require fresh idle evidence (#150)")
    func backgroundSuccessRequiresFinalIdleEvidence() {
        #expect(BackgroundSyncService.coalescedSyncResult(
            rescanResult: .rescanned(1)
        ) == .failed)
        #expect(BackgroundSyncService.coalescedSyncResult(
            rescanResult: .blocked
        ) == .settledWithFolderError)

        for proposed in [
            BackgroundSyncService.SyncResult.synced,
            .alreadyIdle,
        ] {
            #expect(BackgroundSyncService.resultAfterFinalStatusValidation(
                proposed: proposed,
                finalSettlements: [.idle]
            ) == proposed)
            #expect(BackgroundSyncService.resultAfterFinalStatusValidation(
                proposed: proposed,
                finalSettlements: [.active]
            ) == .failed)
            #expect(BackgroundSyncService.resultAfterFinalStatusValidation(
                proposed: proposed,
                finalSettlements: [.idle, .errored]
            ) == .settledWithFolderError)
            #expect(BackgroundSyncService.resultAfterFinalStatusValidation(
                proposed: proposed,
                finalSettlements: nil
            ) == .failed)
        }
    }

    @Test("Empty background folder batches report no vaults without reads or mutation (#150)")
    func emptyBackgroundFolderBatchReportsNoVaults() {
        var statusReads = 0
        var rescans = 0
        let result = BackgroundSyncService.requestFolderRescans(
            foldersJSON: "[]",
            statusJSON: { _ in
                statusReads += 1
                return "{}"
            },
            rescan: { _ in
                rescans += 1
                return nil
            }
        )

        #expect(result == .noFolders)
        #expect(BackgroundSyncService.syncResultForRescanFailure(result) == .noFoldersConfigured)
        #expect(BackgroundSyncService.coalescedSyncResult(
            rescanResult: result
        ) == .noFoldersConfigured)
        #expect(statusReads == 0)
        #expect(rescans == 0)
    }

    @Test("Foreground receive rescans stop globally while send-only keeps its semantics (#150)")
    func foregroundRescanIsFailClosed() {
        let clear = liveStatusJSON()
        let stopped = liveStatusJSON(reason: ConflictSafetyPolicy.stoppedReason)
        var rescanned: [String] = []
        var statusReads = 0

        let blocked = SyncthingManager.performForegroundRescans(
            configuredFolders: [folder("send", type: "sendonly"), folder("receive")],
            targetFolderIDs: ["send", "receive"],
            statusJSON: { _ in
                statusReads += 1
                return clear
            },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(blocked == .blocked(ConflictSafetyPolicy.engineStopMarker))
        #expect(rescanned.isEmpty)
        #expect(statusReads == 0)

        var reads = 0
        let changedAtGate = SyncthingManager.performForegroundRescans(
            configuredFolders: [folder("changed-at-gate", type: "sendonly")],
            targetFolderIDs: ["changed-at-gate"],
            statusJSON: { _ in
                reads += 1
                return reads == 1 ? clear : stopped
            },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(changedAtGate == .blocked(ConflictSafetyPolicy.engineStopMarker))
        #expect(reads == 2)
        #expect(rescanned.isEmpty)

        for ordinarySendOnlyStatus in [
            liveStatusJSON(state: "error", reason: "generic_error"),
            liveStatusJSON(state: "idle", reason: nil, message: "redaction probe"),
            "{\"state\":\"idle\"}",
            "not-json",
        ] {
            let ordinary = SyncthingManager.performForegroundRescans(
                configuredFolders: [folder("ordinary-send-only", type: "sendonly")],
                targetFolderIDs: ["ordinary-send-only"],
                statusJSON: { _ in ordinarySendOnlyStatus },
                rescan: {
                    rescanned.append($0)
                    return nil
                }
            )
            #expect(ordinary == .triggered)
            #expect(rescanned == ["ordinary-send-only"])
            rescanned.removeAll()
        }

        let duplicate = SyncthingManager.performForegroundRescans(
            configuredFolders: [folder("duplicate", type: "sendonly")],
            targetFolderIDs: ["duplicate", "duplicate"],
            statusJSON: { _ in clear },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(duplicate == .blocked(ConflictSafetyPolicy.engineStopMarker))
        #expect(rescanned.isEmpty)

        let succeeded = SyncthingManager.performForegroundRescans(
            configuredFolders: [
                folder("receive-sibling"),
                folder("a", type: "sendonly"),
                folder("b", type: "sendonly"),
            ],
            targetFolderIDs: ["a", "b"],
            statusJSON: { _ in clear },
            rescan: {
                rescanned.append($0)
                return nil
            }
        )
        #expect(succeeded == .triggered)
        #expect(rescanned == ["a", "b"])
    }

    @Test("Clear-shaped receive status cannot authorize mutation or success history (#150)")
    @MainActor
    func immutableRuntimeOverridesClearShapedReceiveStatus() {
        let clearStatus = status(state: "idle", reason: nil)
        #expect(SyncthingManager.effectiveConflictSafetyState(
            folderType: "sendreceive",
            status: clearStatus
        ) == .stopped)
        #expect(SyncthingManager.effectiveConflictSafetyState(
            folderType: "receiveonly",
            status: clearStatus
        ) == .stopped)
        #expect(SyncthingManager.effectiveConflictSafetyState(
            folderType: "receiveencrypted",
            status: clearStatus
        ) == .stopped)
        #expect(SyncthingManager.effectiveConflictSafetyState(
            folderType: "sendonly",
            status: clearStatus
        ) == .clear)

        let receiveManager = makeManager()
        receiveManager._testSetFolderStatuses(["fixture-folder-a": clearStatus])
        #expect(receiveManager.setIgnorePatterns(
            folderID: "fixture-folder-a",
            patterns: ["fixture-pattern"]
        )?.category == .conflictRetentionSafetyStop)

        #expect(!SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: "syncing",
            status: clearStatus,
            hasConnectedPeer: true,
            safetyState: .stopped
        ))
        #expect(!SyncthingManager.shouldTreatIdleStateAsSuccess(
            status: clearStatus,
            stateChangedAt: Date(),
            existingDate: nil,
            hasConnectedPeer: true,
            safetyState: .stopped
        ))
    }

    @Test("Failed final safety validation discards local progress proof (#150)")
    func failedFinalSafetyValidationDiscardsProgressProof() {
        #expect(BackgroundSyncService.validatedLocalDataProgressObserved(
            proposed: true,
            finalResult: .settledWithFolderError
        ) == false)
        #expect(BackgroundSyncService.validatedLocalDataProgressObserved(
            proposed: true,
            finalResult: .failed
        ) == false)
        #expect(BackgroundSyncService.validatedLocalDataProgressObserved(
            proposed: true,
            finalResult: .synced
        ))
    }

    @Test("Normal folder controls still require clear safety evidence (#150)")
    func normalFolderMutationRequiresClearSafetyState() {
        #expect(ConflictSafetyPolicy.allowsMutation(for: .clear))
        #expect(!ConflictSafetyPolicy.allowsMutation(for: .stopped))
        #expect(!ConflictSafetyPolicy.allowsMutation(for: .unknown))

        #expect(ContentView.shouldOfferSyncFilterRecommendation(
            safetyState: .clear,
            isUnreachable: false,
            hasShown: false,
            isAlreadyPresented: false
        ))
        for state in [ConflictSafetyPolicy.State.stopped, .unknown] {
            #expect(!ContentView.shouldOfferSyncFilterRecommendation(
                safetyState: state,
                isUnreachable: false,
                hasShown: false,
                isAlreadyPresented: false
            ))
        }
        #expect(!ContentView.shouldOfferSyncFilterRecommendation(
            safetyState: .clear,
            isUnreachable: true,
            hasShown: false,
            isAlreadyPresented: false
        ))
        #expect(!ContentView.shouldOfferSyncFilterRecommendation(
            safetyState: .clear,
            isUnreachable: false,
            hasShown: true,
            isAlreadyPresented: false
        ))
        #expect(!ContentView.shouldOfferSyncFilterRecommendation(
            safetyState: .clear,
            isUnreachable: false,
            hasShown: false,
            isAlreadyPresented: true
        ))
    }

    @Test("Safety event reason and live status both beat success and redacted fields (#150)")
    @MainActor
    func safetyEventsAreFixedAndRedacted() throws {
        let manager = makeManager()
        let forbidden = [
            "redaction-probe-folder-id",
            "Redaction Probe Vault",
            "redaction-probe/path/item.md",
            "REDACTION-PROBE-DEVICE",
            "redaction-probe-reason",
        ]

        let byReason = try #require(manager._testMakeSyncEventItem(
            type: "StateChanged",
            data: [
                "folder": forbidden[0],
                "from": "syncing",
                "to": "idle",
                "reason": ConflictSafetyPolicy.stoppedReason,
                "message": forbidden[4],
                "path": forbidden[2],
                "device": forbidden[3],
            ],
            folderNamesByID: [forbidden[0]: forbidden[1]],
            deviceNamesByID: [forbidden[3]: "Redaction Probe Device"]
        ))
        let byLiveState = try #require(manager._testMakeSyncEventItem(
            id: 2,
            type: "ItemFinished",
            data: [
                "folder": forbidden[0],
                "item": forbidden[2],
                "type": "file",
                "action": "update",
            ],
            folderNamesByID: [forbidden[0]: forbidden[1]],
            folderSafetyStates: [forbidden[0]: .unknown]
        ))

        for event in [byReason, byLiveState] {
            #expect(event.kind == .folderError)
            #expect(event.folderID == nil)
            #expect(event.deviceID == nil)
            #expect(event.filePath == nil)
            let rendered = "\(event.title)|\(event.detail)|\(event.folderID ?? "")|\(event.deviceID ?? "")|\(event.filePath ?? "")"
            for secret in forbidden {
                #expect(!rendered.contains(secret))
            }
        }
    }

    @Test("A folder error is a failed background fetch with no success copy (#150)")
    @MainActor
    func terminalFolderErrorIsFailure() {
        #expect(AppDelegate.backgroundFetchResult(for: .settledWithFolderError) == .failed)
        #expect(BackgroundSyncService.SyncResult.settledWithFolderError.shouldSurfaceIssue)
        #expect(BackgroundSyncService.SyncResult.settledWithFolderError.issueTitle
            != L10n.tr("Background Sync Completed"))
        #expect(BackgroundSyncService.SyncResult.settledWithFolderError.remediation
            != L10n.tr("No action needed."))
    }

    @Test("Conflict UI exposes inspection but no recovery entry point (#150)")
    func conflictUIIsInspectionOnly() throws {
        let detail = try productSource("VaultSync/Views/ConflictDiffView.swift")
        #expect(detail.components(separatedBy: "SyncBridgeService.readFileContent").count == 3)
        #expect(detail.contains("comparisonContent"))
        for forbidden in [
            "ResolveAction",
            "resolveConflict(",
            "keepBothConflict(",
            "skipFileAndCleanupConflicts(",
            "Always skip on this iPhone",
            "Conflict Resolved",
            "Nothing is discarded",
            "Both versions were kept",
            "was overwritten",
            "was discarded",
            "was renamed",
            "guard allowsResolution",
        ] {
            #expect(!detail.contains(forbidden))
        }

        let list = try productSource("VaultSync/Views/ConflictListView.swift")
        #expect(list.contains("NavigationLink"))
        #expect(list.contains("Conflict Recovery Unavailable"))
        #expect(!list.contains("allowsResolution"))
        #expect(!list.contains("All conflicts resolved"))
    }

    @Test("Pending shares expose inspection only and no shipping mutation wiring (#150)")
    @MainActor
    func pendingShareSurfacesAreReadOnlyIssue150() throws {
        let pending = try productSource("VaultSync/Views/PendingSharesView.swift")
        #expect(pending.contains("Pending shares are read-only in this version."))
        #expect(pending.contains("Read Only"))
        for forbidden in [
            "Button(",
            "onAccept",
            "onRetry",
            "onIgnore",
            "onRestoreIgnored",
            "onChooseTarget",
            "onReconnectObsidian",
            "Accept Share",
            "Review and Accept",
            "Choose Vault…",
            "Restore Share",
            "Applying…",
            "L10n.tr(\"Ready\")",
        ] {
            #expect(!pending.contains(forbidden))
        }

        let content = try productSource("VaultSync/Views/ContentView.swift")
        for forbidden in [
            "var shareAccept:",
            "shareTargetPickerFolder",
            "Merge and Sync",
            "ShareTargetPickerView(",
            "acceptFirstPendingShareFromIssues",
            "runAutomaticPass()",
            "confirmMergeAccept(",
            "acceptManually(",
        ] {
            #expect(!content.contains(forbidden))
        }

        let onboarding = try productSource("VaultSync/Views/OnboardingView.swift")
        for forbidden in [
            "var shareAccept:",
            "runAutomaticPass()",
            "clearRecordedFailures()",
            "accepts it automatically",
            "accepting…",
            "needs your attention",
        ] {
            #expect(!onboarding.contains(forbidden))
        }

        let issues = try productSource("VaultSync/Views/SyncIssuesView.swift")
        #expect(!issues.contains("onAcceptFirstPendingShare"))
        let pendingIssueCase = try sourceSection(
            issues,
            from: "case .pendingShares:\n            // Retained enum case",
            to: "case .conflicts:"
        )
        #expect(!pendingIssueCase.contains("Button("))
        #expect(!pendingIssueCase.contains("Accept"))

        let app = try productSource("VaultSync/App/VaultSyncApp.swift")
        #expect(!app.contains("ShareAcceptCoordinator"))

        let coordinator = try productSource("VaultSync/ViewModels/ShareAcceptCoordinator.swift")
        #expect(!coordinator.contains("static func live("))
        #expect(!coordinator.contains("vaultManager.acceptPendingShare"))

        let reconnect = try productSource("VaultSync/ViewModels/ObsidianReconnectFlow.swift")
        #expect(!reconnect.contains("retryPendingShares"))

        let bridge = try productSource("VaultSync/Services/SyncBridgeService.swift")
        let acceptABI = try sourceSection(
            bridge,
            from: "/// Stable wrapper for the retained gomobile ABI.",
            to: "// MARK: - Phase 6: Device rename"
        )
        #expect(acceptABI.contains("unavailable in 2.0.2"))

        let manager = makeManager(folderIDs: [])
        manager._testSetPendingFolders([
            .init(id: "fixture-offer", label: "Fixture Offer", offeredBy: []),
        ])
        #expect(!manager.unresolvedIssues.contains { $0.kind == .pendingShares })
    }

    @Test("Every conflict recovery facade stops before bridge filter cleanup and rescan work (#150)")
    func recoveryFacadesAreGlobalStubs() throws {
        let source = try productSource("VaultSync/Services/SyncthingManager.swift")
        let directRecovery = try sourceSection(
            source,
            from: "func resolveConflict(folderID:",
            to: "// MARK: - Pending folder shares"
        )
        #expect(directRecovery.contains("vaultsync-conflict-recovery-unavailable"))
        #expect(!directRecovery.contains("SyncBridgeService.resolveConflict"))
        #expect(!directRecovery.contains("SyncBridgeService.keepBothConflict"))
        #expect(!directRecovery.contains("refreshConflicts"))

        let alwaysSkip = try sourceSection(
            source,
            from: "func skipFileAndCleanupConflicts(folderID:",
            to: "// MARK: - Test hooks"
        )
        #expect(alwaysSkip.contains("vaultsync-conflict-recovery-unavailable"))
        for forbidden in [
            "conflictMutationBlockCode",
            "readIgnorePatternsOrNil",
            "setIgnorePatterns",
            "removeConflictFilesForOriginal",
            "rescanFolder",
            "refreshConflicts",
        ] {
            #expect(!alwaysSkip.contains(forbidden))
        }
    }

    @Test("Automatic path, accept, diagnostics, and new safety-pause writers are unreachable (#150)")
    func automaticConfigurationAndFileWritersAreUnavailable() throws {
        let reconciler = try productSource("VaultSync/Services/FolderPathReconciler.swift")
        #expect(reconciler.contains("liveReconcileCandidates"))

        let manager = try productSource("VaultSync/Services/SyncthingManager.swift")
        let add = try sourceSection(
            manager,
            from: "func addFolder(id:",
            to: "/// Remove a folder by ID."
        )
        #expect(add.contains(ConflictSafetyPolicy.engineStopMarker))
        #expect(!add.contains("SyncBridgeService.addFolder"))

        let accept = try sourceSection(
            manager,
            from: "func acceptPendingFolder(folderID:",
            to: "// MARK: - Device rename"
        )
        #expect(accept.contains(ConflictSafetyPolicy.engineStopMarker))
        #expect(!accept.contains("SyncBridgeService.acceptPendingFolder"))

        let diagnostics = try productSource("VaultSync/Views/ControlledDiagnosticsView.swift")
        let namespaceAction = try sourceSection(
            diagnostics,
            from: "case .namespaceActive:",
            to: "default:"
        )
        #expect(!namespaceAction.contains("Start Foreground Upload and Download Check"))
        #expect(!namespaceAction.contains("beginForegroundUpload"))

        let app = try productSource("VaultSync/App/VaultSyncApp.swift")
        let content = try productSource("VaultSync/Views/ContentView.swift")
        #expect(!app.contains("setFolderPaused"))
        #expect(!content.contains("setFolderPaused"))

        let collisionGuard = try productSource("VaultSync/Services/PathCollisionGuard.swift")
        #expect(collisionGuard.contains("setPaused"))
    }

    @Test("Conflict inspection distinguishes content, empty files, and unavailable reads (#150)")
    func conflictFileInspectionPayloadIsUnambiguousIssue150() {
        #expect(SyncBridgeService.decodeFileInspectionResult(
            #"{"content":"error:legitimate note text"}"#
        ) == .content("error:legitimate note text"))
        #expect(SyncBridgeService.decodeFileInspectionResult(
            #"{"content":""}"#
        ) == .content(""))
        #expect(SyncBridgeService.decodeFileInspectionResult(
            #"{"error":"vaultsync-conflict-inspection-unavailable"}"#
        ) == .unavailable)
        #expect(SyncBridgeService.decodeFileInspectionResult(
            "error:redaction-probe-note.md"
        ) == .unavailable)
    }

    @Test("Unavailable conflict inspection preserves prior review copies without claiming empty (#150)")
    func conflictInspectionCacheIsFailClosedIssue150() {
        let previous = ["a": [conflict], "removed": [conflict]]
        let snapshot = SyncthingManager.mergeConflictInspection(
            previous: previous,
            activeFolderIDs: ["a", "b", "c"],
            rawByFolder: [
                "a": "vaultsync-conflict-inspection-unavailable",
                "b": "[]",
                "c": String(data: try! JSONEncoder().encode([conflict]), encoding: .utf8)!,
            ]
        )

        #expect(snapshot.conflicts["a"]?.map(\.conflictPath) == [conflict.conflictPath])
        #expect(snapshot.conflicts["b"] == nil)
        #expect(snapshot.conflicts["c"]?.map(\.conflictPath) == [conflict.conflictPath])
        #expect(snapshot.conflicts["removed"] == nil)
        #expect(snapshot.unavailableFolderIDs == ["a"])
    }

    @Test("Default foreground rescans select only SendOnly folders as one batch (#150)")
    func defaultForegroundRescanTargetsAreSendOnlyIssue150() throws {
        let folders = [
            folder("receive-first", type: "sendreceive"),
            folder("send-b", type: "sendonly"),
            folder("send-a", type: "sendonly"),
            folder("receive-last", type: "receiveonly"),
        ]
        #expect(SyncthingManager.defaultForegroundRescanTargetFolderIDs(folders) == ["send-a", "send-b"])

        let content = try productSource("VaultSync/Views/ContentView.swift")
        let issueRescans = try sourceSection(
            content,
            from: "private func rescanFailedVaults()",
            to: "// MARK: - Unreachable Vaults"
        )
        #expect(!issueRescans.contains("syncthingManager.rescanFolder(id:"))
        #expect(issueRescans.contains("triggerForegroundSync(folderIDs:"))
    }

    @Test("Protected marker loss keeps specific manual guidance while mutations stay stopped (#150, #65)")
    @MainActor
    func protectedMarkerLossKeepsIntegrityGuidanceIssue150() {
        let manager = makeManager()
        manager._testSetFolderStatuses([
            "fixture-folder-a": status(
                state: "error",
                reason: "unknown_error",
                message: "folder marker missing"
            ),
        ])

        let diagnostic = manager.folderUserError(folderID: "fixture-folder-a")
        #expect(diagnostic?.category == .folderMarkerMissing)
        #expect(diagnostic?.remediation.localizedCaseInsensitiveContains("rescan") == false)
        #expect(manager.rescanFolder(id: "fixture-folder-a") == ConflictSafetyPolicy.engineStopMarker)
        #expect(manager.unresolvedIssues.contains { $0.kind == .folderErrors })
        #expect(!manager.hasRescanableFolderErrors)
    }

    @Test("Conflict views use neutral provenance and never expose retry or false-empty copy (#150)")
    func conflictInspectionUIIsNeutralIssue150() throws {
        let detail = try productSource("VaultSync/Views/ConflictDiffView.swift")
        #expect(detail.contains("Current File"))
        #expect(detail.contains("Conflict Copy"))
        #expect(detail.contains("This copy is unavailable for inspection."))
        #expect(detail.contains("(empty)"))
        for forbidden in ["This Device", "Other Device", "SyncUserError.from", "(empty or unreadable)"] {
            #expect(!detail.contains(forbidden))
        }

        let list = try productSource("VaultSync/Views/ConflictListView.swift")
        #expect(list.contains("conflictInspectionUnavailableFolderIDs"))
        #expect(list.contains("Conflict inspection is unavailable."))
        #expect(!list.contains("Label(conflict.deviceShortID"))
    }

    @Test("Production engine databases stay in the main app's private home (#150)")
    func appPrivateDatabaseOwnershipBoundaryIssue150() throws {
        let manager = try productSource("VaultSync/Services/SyncthingManager.swift")
        let foregroundHome = try sourceSection(
            manager,
            from: "private static func configDirectory()",
            to: "private func markPendingShareSeen()"
        )
        #expect(foregroundHome.contains(".documentDirectory"))
        #expect(foregroundHome.contains("appendingPathComponent(\"syncthing\""))

        let background = try productSource("VaultSync/Services/BackgroundSyncService.swift")
        let backgroundHome = try sourceSection(
            background,
            from: "private static func syncthingConfigDir()",
            to: "enum FolderRescanResult"
        )
        #expect(backgroundHome.contains(".documentDirectory"))
        #expect(backgroundHome.contains("appendingPathComponent(\"syncthing\""))

        let project = try productSource("project.yml")
        #expect(!project.contains("UIFileSharingEnabled"))
        #expect(!project.contains("LSSupportsOpeningDocumentsInPlace"))
        #expect(project.components(separatedBy: "framework: ../go/build/SyncBridge.xcframework").count == 2)

        let targetsStart = try #require(project.range(of: "targets:\n")?.upperBound)
        let targetNames = Set(project[targetsStart...].split(separator: "\n").compactMap { line -> String? in
            guard line.hasPrefix("  "), !line.hasPrefix("    "), line.hasSuffix(":") else {
                return nil
            }
            return String(line.dropFirst(2).dropLast())
        })
        #expect(targetNames == Set(["VaultSync", "VaultSyncWidget", "VaultSyncTests"]))

        let widgetTarget = try sourceSection(
            project,
            from: "  VaultSyncWidget:",
            to: "  VaultSyncTests:"
        )
        #expect(!widgetTarget.contains("SyncBridge"))
        let widget = try productSource("VaultSyncWidget/VaultSyncWidget.swift")
        #expect(!widget.contains("SyncBridgeService"))
        #expect(!widget.contains("BridgeStartSyncthing"))
        #expect(widget.contains("UserDefaults(suiteName:"))

        let app = try productSource("VaultSync/App/VaultSyncApp.swift")
        #expect(app.components(separatedBy: "SyncthingManager()").count == 2)
    }

    @Test("Controlled diagnostics stops before every file, scan, event, or success effect (#150)")
    @MainActor
    func controlledDiagnosticsStopsBeforeEveryEffect() {
        let counter = Issue150EffectCounter()
        let controller = DiagnosticsPairingController(
            uploadFileWriter: { _, _, _ in counter.record("writer") }
        )

        controller.beginForegroundUpload(
            recordID: "fixture-record",
            receiveSafetyState: { .stopped },
            preflight: { _, _, _ in
                counter.record("preflight")
                fatalError("preflight must remain unreachable")
            },
            rescan: {
                counter.record("rescan")
                return true
            },
            events: { _ in
                counter.record("events")
                return nil
            }
        )

        #expect(controller.uploadStatuses["fixture-record"]?.phase == .unavailable)
        #expect(controller.lastError == .unavailable)
        for effect in ["preflight", "writer", "rescan", "events"] {
            #expect(counter.value(effect) == 0)
        }
    }

    private func productSource(
        _ relativePath: String,
        filePath: StaticString = #filePath
    ) throws -> String {
        let iosDirectory = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        return try String(
            contentsOf: iosDirectory.appendingPathComponent(relativePath),
            encoding: .utf8
        )
    }

    private func sourceSection(
        _ source: String,
        from startMarker: String,
        to endMarker: String
    ) throws -> String {
        let start = try #require(source.range(of: startMarker)?.lowerBound)
        let end = try #require(source.range(of: endMarker, range: start..<source.endIndex)?.lowerBound)
        return String(source[start..<end])
    }
}


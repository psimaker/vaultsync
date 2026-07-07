import Testing
@testable import VaultSync

@Suite("Sync header derives from the issue list (#66)")
struct SyncHeaderModelTests {
    /// Baseline: everything healthy, folders syncing fine.
    private func healthy() -> SyncHeaderModel.Inputs {
        SyncHeaderModel.Inputs(
            hasEngineError: false,
            engineRunning: true,
            issueSeverities: [],
            hasUnreachableFolders: false,
            isSyncing: false,
            hasSyncFolders: true,
            vaultAccessible: true,
            vaultNeedsReconnect: false,
            hasDetectedVaults: true
        )
    }

    // The reported lie: green check + "All Synced" while a parked share sat
    // in the issue list. Any warning-tier issue must surface in the header.
    @Test("Header is never synced while a warning-tier issue exists")
    func warningTierReachesHeader() {
        var inputs = healthy()
        inputs.issueSeverities = [.warning]
        let state = SyncHeaderModel.derive(inputs)
        #expect(state.status != .synced)
        #expect(state == .init(status: .attention, titleKey: "Action Needed"))
    }

    @Test("Multiple warnings still map to a single attention state")
    func multipleWarnings() {
        var inputs = healthy()
        inputs.issueSeverities = [.warning, .warning, .warning]
        #expect(SyncHeaderModel.derive(inputs).status == .attention)
    }

    @Test("Critical issues outrank warnings and show the issue title")
    func criticalOutranksWarning() {
        var inputs = healthy()
        inputs.issueSeverities = [.warning, .critical]
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .attention, titleKey: "Sync Issue"))
    }

    // Unreachable folders (#25) live in their own dashboard section, not in
    // unresolvedIssues — the header must not say "All Synced" above their
    // recovery card.
    @Test("Unreachable folders count as a sync issue")
    func unreachableFoldersReachHeader() {
        var inputs = healthy()
        inputs.hasUnreachableFolders = true
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .attention, titleKey: "Sync Issue"))
    }

    @Test("Engine error outranks everything")
    func engineErrorWins() {
        var inputs = healthy()
        inputs.hasEngineError = true
        inputs.issueSeverities = [.critical]
        inputs.isSyncing = true
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .error, titleKey: "Error"))
    }

    @Test("Stopped engine shows starting, not a health claim")
    func stoppedEngine() {
        var inputs = healthy()
        inputs.engineRunning = false
        inputs.issueSeverities = [.warning]
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .starting, titleKey: "Starting…"))
    }

    // Documented precedence: an active transfer outranks the warning tier
    // (short-lived; the warning surfaces the moment the transfer settles) —
    // but never a critical issue.
    @Test("Active transfer outranks warnings but not criticals")
    func syncingPrecedence() {
        var inputs = healthy()
        inputs.isSyncing = true
        inputs.issueSeverities = [.warning]
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .syncing, titleKey: "Syncing…"))

        inputs.issueSeverities = [.critical]
        #expect(SyncHeaderModel.derive(inputs).titleKey == "Sync Issue")
    }

    @Test("All healthy with folders reads All Synced")
    func healthyReadsAllSynced() {
        #expect(SyncHeaderModel.derive(healthy()) == .init(status: .synced, titleKey: "All Synced"))
    }

    // "Ready" only when genuinely armed: vault accessible and a vault exists,
    // so the auto-accept pass could act the moment a share arrives.
    @Test("No sync folders but armed reads Ready")
    func armedReadsReady() {
        var inputs = healthy()
        inputs.hasSyncFolders = false
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .synced, titleKey: "Ready"))
    }

    // The reported contradiction: green "Ready" next to "No vaults found /
    // Create a vault in Obsidian first".
    @Test("No detected vaults is never a green Ready")
    func noVaultsIsNotReady() {
        var inputs = healthy()
        inputs.hasSyncFolders = false
        inputs.hasDetectedVaults = false
        let state = SyncHeaderModel.derive(inputs)
        #expect(state.status != .synced)
        #expect(state.titleKey == "No Vaults Yet")
    }

    @Test("Inaccessible vault on first run reads Finish Setup")
    func firstRunReadsFinishSetup() {
        var inputs = healthy()
        inputs.hasSyncFolders = false
        inputs.vaultAccessible = false
        inputs.hasDetectedVaults = false
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .attention, titleKey: "Finish Setup"))
    }

    @Test("Expired vault access reads Action Needed, not Finish Setup")
    func expiredAccessReadsActionNeeded() {
        var inputs = healthy()
        inputs.vaultAccessible = false
        inputs.vaultNeedsReconnect = true
        #expect(SyncHeaderModel.derive(inputs) == .init(status: .attention, titleKey: "Action Needed"))
    }

    // Every title key must resolve in all four languages — a typo here would
    // silently fall back to the raw key at runtime. Guarded by comparing
    // against the known key set; the per-language files are checked by the
    // key-count verification.
    @Test("Derivation only ever produces known title keys")
    func onlyKnownTitleKeys() {
        let knownKeys: Set<String> = [
            "Error", "Starting…", "Sync Issue", "Syncing…", "Action Needed",
            "Finish Setup", "All Synced", "Ready", "No Vaults Yet",
        ]
        var inputs = SyncHeaderModel.Inputs(
            hasEngineError: false,
            engineRunning: false,
            issueSeverities: [],
            hasUnreachableFolders: false,
            isSyncing: false,
            hasSyncFolders: false,
            vaultAccessible: false,
            vaultNeedsReconnect: false,
            hasDetectedVaults: false
        )
        for engineError in [false, true] {
            for running in [false, true] {
                for severities in [[], [SyncthingManager.SyncIssueSeverity.warning], [.critical]] {
                    for unreachable in [false, true] {
                        for syncing in [false, true] {
                            for folders in [false, true] {
                                for accessible in [false, true] {
                                    for reconnect in [false, true] {
                                        for vaults in [false, true] {
                                            inputs.hasEngineError = engineError
                                            inputs.engineRunning = running
                                            inputs.issueSeverities = severities
                                            inputs.hasUnreachableFolders = unreachable
                                            inputs.isSyncing = syncing
                                            inputs.hasSyncFolders = folders
                                            inputs.vaultAccessible = accessible
                                            inputs.vaultNeedsReconnect = reconnect
                                            inputs.hasDetectedVaults = vaults
                                            #expect(knownKeys.contains(SyncHeaderModel.derive(inputs).titleKey))
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

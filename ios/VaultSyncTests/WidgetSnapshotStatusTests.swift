import Testing
@testable import VaultSync

@Suite("Widget snapshot status derives from the issue list (#73)")
struct WidgetSnapshotStatusTests {
    /// Baseline: engine healthy, folders configured, nothing wrong.
    private func derive(
        hasEngineError: Bool = false,
        engineRunning: Bool = true,
        issueSeverities: [SyncthingManager.SyncIssueSeverity] = [],
        hasUnreachableFolders: Bool = false,
        isSyncing: Bool = false,
        hasSyncFolders: Bool = true
    ) -> SyncStatus {
        SyncHeaderModel.deriveWidgetStatus(
            hasEngineError: hasEngineError,
            engineRunning: engineRunning,
            issueSeverities: issueSeverities,
            hasUnreachableFolders: hasUnreachableFolders,
            isSyncing: isSyncing,
            hasSyncFolders: hasSyncFolders
        )
    }

    // The reported lie: the widget kept a green check while a share sat
    // parked or a required peer was offline — both warning-tier issues that
    // the pre-#73 snapshot (idle/syncing/error only) could not express.
    @Test("A warning-tier issue surfaces as attention, never as synced")
    func warningTierReachesWidget() {
        let status = derive(issueSeverities: [.warning])
        #expect(status == .attention)
        #expect(status != .synced)
    }

    @Test("Critical issues surface as attention")
    func criticalTierReachesWidget() {
        #expect(derive(issueSeverities: [.critical]) == .attention)
        #expect(derive(issueSeverities: [.warning, .critical]) == .attention)
    }

    @Test("Unreachable folders surface as attention")
    func unreachableFoldersReachWidget() {
        #expect(derive(hasUnreachableFolders: true) == .attention)
    }

    @Test("Engine errors keep the error tier")
    func engineErrorStaysError() {
        #expect(derive(hasEngineError: true) == .error)
        #expect(derive(hasEngineError: true, issueSeverities: [.critical]) == .error)
    }

    // Same deliberate precedence as the header: a transfer is short-lived and
    // the warning surfaces the moment it settles.
    @Test("Active transfer outranks warnings, matching the header")
    func syncingOutranksWarnings() {
        #expect(derive(issueSeverities: [.warning], isSyncing: true) == .syncing)
    }

    @Test("Engine not running maps to starting, never to synced")
    func notRunningIsStarting() {
        #expect(derive(engineRunning: false) == .starting)
    }

    @Test("Clean state stays synced")
    func cleanStateIsSynced() {
        #expect(derive() == .synced)
        #expect(derive(hasSyncFolders: false) == .synced)
    }

    // Structural guarantee of decision 012: the widget tier IS the header
    // cascade with the vault tiers pinned "armed" — a new issue kind that
    // reaches the header can never miss the widget.
    @Test("Widget tier equals the header cascade for every input combination")
    func matchesHeaderCascade() {
        let severityMatrix: [[SyncthingManager.SyncIssueSeverity]] =
            [[], [.warning], [.critical], [.warning, .critical]]
        for hasEngineError in [false, true] {
            for engineRunning in [false, true] {
                for severities in severityMatrix {
                    for hasUnreachableFolders in [false, true] {
                        for isSyncing in [false, true] {
                            for hasSyncFolders in [false, true] {
                                let widget = SyncHeaderModel.deriveWidgetStatus(
                                    hasEngineError: hasEngineError,
                                    engineRunning: engineRunning,
                                    issueSeverities: severities,
                                    hasUnreachableFolders: hasUnreachableFolders,
                                    isSyncing: isSyncing,
                                    hasSyncFolders: hasSyncFolders
                                )
                                let header = SyncHeaderModel.derive(.init(
                                    hasEngineError: hasEngineError,
                                    engineRunning: engineRunning,
                                    issueSeverities: severities,
                                    hasUnreachableFolders: hasUnreachableFolders,
                                    isSyncing: isSyncing,
                                    hasSyncFolders: hasSyncFolders,
                                    vaultAccessible: true,
                                    vaultNeedsReconnect: false,
                                    hasDetectedVaults: true
                                )).status
                                #expect(widget == header)
                            }
                        }
                    }
                }
            }
        }
    }

    // End-to-end wire trip: the tier the app persists must decode in the
    // widget to the same tier — attention can never round-trip into the
    // green branch.
    @Test("Persisted wire value decodes back to the same tier in the widget")
    func wireRoundTrip() {
        let parkedShare = derive(issueSeverities: [.warning])
        #expect(SyncStatus.fromWire(parkedShare.wireValue) == .attention)

        let clean = derive()
        #expect(SyncStatus.fromWire(clean.wireValue) == .synced)
    }
}

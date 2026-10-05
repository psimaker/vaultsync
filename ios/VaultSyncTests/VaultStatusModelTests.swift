import Testing
@testable import VaultSync

@Suite("One honest status per vault (#187)")
struct VaultStatusModelTests {
    /// Baseline: an idle folder that has synced before, nothing waiting.
    private func settled() -> VaultStatusModel.Inputs {
        VaultStatusModel.Inputs(
            engineState: "idle",
            folderPaused: false,
            unreachable: false,
            conflictCount: 0,
            hasCompletedSync: true,
            completionPct: 100
        )
    }

    @Test("A settled vault that has synced before is up to date")
    func settledVaultIsSynced() {
        #expect(VaultStatusModel.derive(settled()) == .init(status: .synced, label: .upToDate))
    }

    // #94: idle before the first exchange is not "Up to Date".
    @Test("An idle vault that never synced waits, it does not claim green")
    func neverSyncedIsNotGreen() {
        var inputs = settled()
        inputs.hasCompletedSync = false
        let state = VaultStatusModel.derive(inputs)
        #expect(state.status != .synced)
        #expect(state == .init(status: .starting, label: .waitingForFirstSync))
    }

    @Test("A state the engine has not reported renders neutral, never green")
    func missingStateIsUnknown() {
        var inputs = settled()
        inputs.engineState = nil
        #expect(VaultStatusModel.derive(inputs) == .init(status: nil, label: .unknown))
    }

    // Only the header's two transfer states count as syncing (decision 012):
    // a wider mapping would let a row say "Syncing" under "All Synced".
    @Test("Engine states the header does not count as a transfer stay unknown")
    func unmappedEngineStatesStayUnknown() {
        for state in ["sync-preparing", "sync-waiting", "scan-waiting", "cleaning", "unknown", ""] {
            var inputs = settled()
            inputs.engineState = state
            #expect(VaultStatusModel.derive(inputs) == .init(status: nil, label: .unknown), "state \(state)")
        }
    }

    @Test("Syncing and scanning render as a transfer with a meaningful percentage")
    func transferStates() {
        var inputs = settled()
        inputs.engineState = "syncing"
        inputs.completionPct = 42.7
        #expect(VaultStatusModel.derive(inputs) == .init(status: .syncing, label: .syncing(percent: 42)))

        inputs.engineState = "scanning"
        inputs.completionPct = nil
        #expect(VaultStatusModel.derive(inputs) == .init(status: .syncing, label: .scanning(percent: nil)))
    }

    @Test("0 % and 100 % are not shown as progress")
    func percentBounds() {
        #expect(VaultStatusModel.inProgressPercent(0) == nil)
        #expect(VaultStatusModel.inProgressPercent(100) == nil)
        #expect(VaultStatusModel.inProgressPercent(nil) == nil)
        #expect(VaultStatusModel.inProgressPercent(0.5) == 0)
        #expect(VaultStatusModel.inProgressPercent(99.9) == 99)
    }

    @Test("Conflicts put an idle vault into attention with the file count")
    func conflictsNeedAttention() {
        var inputs = settled()
        inputs.conflictCount = 3
        #expect(VaultStatusModel.derive(inputs) == .init(status: .attention, label: .conflicts(3)))
    }

    // Same precedence as the header: a short-lived transfer outranks the
    // warning tier, a failure outranks both.
    @Test("A transfer outranks conflicts, an error outranks a transfer")
    func precedenceMirrorsHeader() {
        var inputs = settled()
        inputs.conflictCount = 2
        inputs.engineState = "syncing"
        #expect(VaultStatusModel.derive(inputs).status == .syncing)

        inputs.engineState = "error"
        #expect(VaultStatusModel.derive(inputs) == .init(status: .error, label: .error))
    }

    @Test("A paused folder is paused, even when the engine reports it as an error")
    func pausedWins() {
        var inputs = settled()
        inputs.folderPaused = true
        inputs.engineState = "error"
        inputs.conflictCount = 1
        #expect(VaultStatusModel.derive(inputs) == .init(status: .paused, label: .paused))
    }

    @Test("An unreachable folder outranks everything, including a pause")
    func unreachableWins() {
        var inputs = settled()
        inputs.unreachable = true
        inputs.folderPaused = true
        inputs.engineState = "error"
        #expect(VaultStatusModel.derive(inputs) == .init(status: .error, label: .unreachable))
    }

    // The scheme pins the test language to English, so these are the
    // literal keys (decision 005).
    @Test("Labels read as short English phrases")
    func labelText() {
        #expect(VaultStatusModel.Label.upToDate.text == "Up to Date")
        #expect(VaultStatusModel.Label.conflicts(1).text == "1 conflict")
        #expect(VaultStatusModel.Label.conflicts(4).text == "4 conflicts")
        #expect(VaultStatusModel.Label.syncing(percent: 42).text == "Syncing (42%)")
        #expect(VaultStatusModel.Label.scanning(percent: nil).text == "Scanning")
        #expect(VaultStatusModel.Label.unreachable.text == "Folder Path Missing")
    }
}

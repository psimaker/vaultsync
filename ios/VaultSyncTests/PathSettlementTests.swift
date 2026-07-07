import Testing
@testable import VaultSync

@Suite("Accept decisions only run on settled paths (#56)")
struct PathSettlementTests {

    @Test("Cold start: unsettled until the first reconcile completes — the reported race window")
    func coldStartHoldsUntilFirstReconcileCompletes() {
        var settlement = PathSettlement()
        #expect(!settlement.settled)

        let token = settlement.reconcileBegan()
        #expect(!settlement.settled)

        settlement.reconcileFinished(token: token)
        #expect(settlement.settled)
    }

    @Test("Overlapping reconciles: settled only after the last in-flight pass finishes")
    func overlappingReconcilesNeedBothCompletions() {
        var settlement = PathSettlement()
        let coldStart = settlement.reconcileBegan()
        let reconnect = settlement.reconcileBegan()

        settlement.reconcileFinished(token: reconnect)
        #expect(!settlement.settled) // the cold-start pass is still running

        settlement.reconcileFinished(token: coldStart)
        #expect(settlement.settled)
    }

    @Test("An abandoned reconcile never settles — a dead engine must not unlock accepts")
    func abandonedReconcileDoesNotSettle() {
        var settlement = PathSettlement()
        let token = settlement.reconcileBegan()
        settlement.reconcileAbandoned(token: token)
        #expect(!settlement.settled)
    }

    @Test("Recovery after abandonment: reset (new engine generation) plus a fresh completed reconcile settles — no permanent hold")
    func abandonThenResetThenFreshReconcileSettles() {
        var settlement = PathSettlement()
        let dying = settlement.reconcileBegan()
        settlement.reconcileAbandoned(token: dying)
        #expect(!settlement.settled)

        settlement.reset() // engine restart
        let fresh = settlement.reconcileBegan()
        settlement.reconcileFinished(token: fresh)
        #expect(settlement.settled)
    }

    @Test("A stale completion from before a reset cannot settle the new generation")
    func staleCompletionIgnoredAfterReset() {
        var settlement = PathSettlement()
        let stale = settlement.reconcileBegan()

        settlement.reset() // stop() while the pass is still running detached
        settlement.reconcileFinished(token: stale)
        #expect(!settlement.settled)

        // The new generation still requires its own completed pass.
        let fresh = settlement.reconcileBegan()
        #expect(!settlement.settled)
        settlement.reconcileFinished(token: fresh)
        #expect(settlement.settled)
    }

    @Test("A stale abandonment cannot corrupt the new generation's in-flight count")
    func staleAbandonmentIgnoredAfterReset() {
        var settlement = PathSettlement()
        let stale = settlement.reconcileBegan()
        settlement.reset()

        let fresh = settlement.reconcileBegan()
        settlement.reconcileAbandoned(token: stale) // must not decrement the fresh pass
        #expect(!settlement.settled)

        settlement.reconcileFinished(token: fresh)
        #expect(settlement.settled)
    }

    @Test("Reset unsettles: after an engine stop, accepts hold until the next start's reconcile")
    func resetUnsettles() {
        var settlement = PathSettlement()
        let token = settlement.reconcileBegan()
        settlement.reconcileFinished(token: token)
        #expect(settlement.settled)

        settlement.reset()
        #expect(!settlement.settled)
    }

    @Test("A later reconcile in the same generation re-holds and re-settles (Obsidian reconnect, #53)")
    func laterReconcileReHoldsThenSettles() {
        var settlement = PathSettlement()
        let coldStart = settlement.reconcileBegan()
        settlement.reconcileFinished(token: coldStart)
        #expect(settlement.settled)

        let reconnect = settlement.reconcileBegan()
        #expect(!settlement.settled)
        settlement.reconcileFinished(token: reconnect)
        #expect(settlement.settled)
    }
}

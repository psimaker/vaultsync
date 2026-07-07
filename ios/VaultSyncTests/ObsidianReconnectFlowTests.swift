import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Auto-accept fires after Obsidian reconnect (#53)")
struct ObsidianReconnectFlowTests {

    @Test("Successful grant runs the full sequence in order: immediate feedback, reconcile, then the accept pass")
    func successRunsFullSequenceInOrder() async {
        var events: [String] = []

        let error = await ObsidianReconnectFlow.run(
            grantAccess: { events.append("grant"); return nil },
            onGrantSucceeded: { events.append("feedback") },
            reconcile: {
                events.append("reconcile-start")
                // Suspend at least once so an accept pass wrongly fired
                // concurrently (instead of sequenced) would interleave here
                // and break the order assertion below.
                await Task.yield()
                events.append("reconcile-end")
            },
            retryPendingShares: { events.append("retry") }
        )

        #expect(error == nil)
        #expect(events == ["grant", "feedback", "reconcile-start", "reconcile-end", "retry"])
    }

    @Test("Failed grant short-circuits: no feedback, no reconcile, no accept pass")
    func failedGrantShortCircuits() async {
        var events: [String] = []

        let error = await ObsidianReconnectFlow.run(
            grantAccess: { events.append("grant"); return "no access" },
            onGrantSucceeded: { events.append("feedback") },
            reconcile: { events.append("reconcile") },
            retryPendingShares: { events.append("retry") }
        )

        #expect(error == "no access")
        #expect(events == ["grant"])
    }

    @Test("A reconcile that does not return fires no accept pass — no timeout fallback; the standing pendingFolders change trigger covers that case — and a late reconcile still completes the sequence")
    func hangingReconcileFiresNoRetryUntilItReturns() async {
        var retried = false
        var releaseReconcile: CheckedContinuation<Void, Never>?

        let flow = Task {
            await ObsidianReconnectFlow.run(
                grantAccess: { nil },
                onGrantSucceeded: { },
                reconcile: {
                    await withCheckedContinuation { releaseReconcile = $0 }
                },
                retryPendingShares: { retried = true }
            )
        }

        // Wait until the flow is suspended inside the reconcile, then give it
        // ample opportunity to (wrongly) fire the retry while still pending.
        while releaseReconcile == nil { await Task.yield() }
        for _ in 0..<50 { await Task.yield() }
        #expect(!retried)

        // A reconcile that eventually returns still completes the sequence.
        releaseReconcile?.resume()
        let error = await flow.value
        #expect(error == nil)
        #expect(retried)
    }
}

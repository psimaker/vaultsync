import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Obsidian reconnect never mutates pending shares (#150)")
struct ObsidianReconnectFlowTests {

    @Test("Successful grant publishes feedback then reconciles without a pending-share effect (#150)")
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
            }
        )

        #expect(error == nil)
        #expect(events == ["grant", "feedback", "reconcile-start", "reconcile-end"])
    }

    @Test("Failed grant short-circuits before feedback and reconcile (#150)")
    func failedGrantShortCircuits() async {
        var events: [String] = []

        let error = await ObsidianReconnectFlow.run(
            grantAccess: { events.append("grant"); return "no access" },
            onGrantSucceeded: { events.append("feedback") },
            reconcile: { events.append("reconcile") }
        )

        #expect(error == "no access")
        #expect(events == ["grant"])
    }

    @Test("A reconnect waits for reconciliation and has no timeout side effect (#150)")
    func hangingReconcileHasNoTimeoutSideEffect() async {
        var finished = false
        var releaseReconcile: CheckedContinuation<Void, Never>?

        let flow = Task {
            let result = await ObsidianReconnectFlow.run(
                grantAccess: { nil },
                onGrantSucceeded: { },
                reconcile: {
                    await withCheckedContinuation { releaseReconcile = $0 }
                }
            )
            finished = true
            return result
        }

        // Wait until the flow is suspended inside the reconcile, then give it
        // ample opportunity to (wrongly) finish while still pending.
        while releaseReconcile == nil { await Task.yield() }
        for _ in 0..<50 { await Task.yield() }
        #expect(!finished)

        // A reconcile that eventually returns still completes the sequence.
        releaseReconcile?.resume()
        let error = await flow.value
        #expect(error == nil)
        #expect(finished)
    }
}

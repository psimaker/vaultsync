import Foundation
import Testing
@testable import VaultSync

/// The ownership core of #183 (decision 040), pinned without an engine: the
/// claim state machine and the single-flight admission.
@Suite("Engine lifecycle ownership core (#183)")
struct LifecycleOwnershipCoreTests {

    @Test("A background stop claim is refused while the foreground owns the lifecycle")
    func stopClaimRefusedUnderForeground() {
        var state = SyncLifecycleState()
        state.claimForegroundForStart()
        let refused = state.beginBackgroundStop()
        #expect(!refused)
        #expect(!state.backgroundStopInProgress)

        state.releaseForeground()
        let claimed = state.beginBackgroundStop()
        #expect(claimed)
        #expect(state.backgroundStopInProgress)
        state.endBackgroundStop()
        #expect(!state.backgroundStopInProgress)
    }

    @Test("Adoption is refused while a stop claim holds; a cold-start claim still lands and blocks new stops")
    func adoptionRefusedDuringStopClaim() {
        var state = SyncLifecycleState()
        let stopClaimed = state.beginBackgroundStop()
        #expect(stopClaimed)
        let adoption = state.claimForegroundForAdoption()
        #expect(adoption == .refusedStopInProgress)
        #expect(!state.foregroundActive, "a refused adoption leaves no claim behind")

        state.claimForegroundForStart()
        #expect(state.foregroundActive)
        #expect(state.backgroundStopInProgress, "the in-flight stop keeps its claim until it ends")
        state.endBackgroundStop()
        let newStop = state.beginBackgroundStop()
        #expect(!newStop, "no new stop may begin under a foreground claim")
    }

    @Test("Adoption claims once the stop claim clears")
    func adoptionAfterStopClears() {
        var state = SyncLifecycleState()
        let stopClaimed = state.beginBackgroundStop()
        #expect(stopClaimed)
        state.endBackgroundStop()
        let adoption = state.claimForegroundForAdoption()
        #expect(adoption == .claimed)
        #expect(state.foregroundActive)
    }

    @Test("A follower mirrors the leader's result and the slot frees for the next leader")
    func followerMirrorsLeader() async {
        let flight = BackgroundSyncSingleFlight()
        #expect(await flight.enter() == .leader)
        #expect(flight.isLeaderInFlight)

        let registration = AsyncStream<Void>.makeStream()
        let follower = Task {
            await flight.enter(onFollow: { registration.continuation.yield(()) })
        }
        for await _ in registration.stream { break }

        flight.finish(.bridgeStartFailed)
        #expect(await follower.value == .follower(leaderResult: .bridgeStartFailed))
        #expect(!flight.isLeaderInFlight)

        #expect(await flight.enter() == .leader)
        flight.finish(.synced)
    }

    @Test("A follower cancelled before the leader finishes reports no result, and the later finish is harmless")
    func cancelledFollowerReportsNil() async {
        let flight = BackgroundSyncSingleFlight()
        #expect(await flight.enter() == .leader)

        let registration = AsyncStream<Void>.makeStream()
        let follower = Task {
            await flight.enter(onFollow: { registration.continuation.yield(()) })
        }
        for await _ in registration.stream { break }

        follower.cancel()
        #expect(await follower.value == .follower(leaderResult: nil))
        flight.finish(.synced)
        #expect(await flight.enter() == .leader)
        flight.finish(.synced)
    }

    @Test("A follower admitted on an already-cancelled task does not wait for the leader")
    func precancelledFollowerDoesNotWait() async {
        let flight = BackgroundSyncSingleFlight()
        #expect(await flight.enter() == .leader)

        let follower = Task { () -> BackgroundSyncSingleFlight.Entry in
            withUnsafeCurrentTask { $0?.cancel() }
            return await flight.enter()
        }
        #expect(await follower.value == .follower(leaderResult: nil))
        flight.finish(.synced)
    }
}

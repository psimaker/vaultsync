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
        #expect(refused == .refusedForegroundOwns)
        #expect(!state.backgroundStopInProgress)

        state.releaseForeground()
        let claimed = state.beginBackgroundStop()
        #expect(claimed == .claimed)
        #expect(state.backgroundStopInProgress)
        state.endBackgroundStop()
        #expect(!state.backgroundStopInProgress)
    }

    @Test("Adoption is refused while a stop claim holds; a cold-start claim still lands and blocks new stops")
    func adoptionRefusedDuringStopClaim() {
        var state = SyncLifecycleState()
        let stopClaimed = state.beginBackgroundStop()
        #expect(stopClaimed == .claimed)
        let adoption = state.claimForegroundForAdoption()
        #expect(adoption == .refusedStopInProgress)
        #expect(!state.foregroundActive, "a refused adoption leaves no claim behind")

        state.claimForegroundForStart()
        #expect(state.foregroundActive)
        #expect(state.backgroundStopInProgress, "the in-flight stop keeps its claim until it ends")
        state.endBackgroundStop()
        let newStop = state.beginBackgroundStop()
        #expect(newStop == .refusedForegroundOwns, "no new stop may begin under a foreground claim")
    }

    @Test("Adoption claims once the stop claim clears")
    func adoptionAfterStopClears() {
        var state = SyncLifecycleState()
        let stopClaimed = state.beginBackgroundStop()
        #expect(stopClaimed == .claimed)
        state.endBackgroundStop()
        let adoption = state.claimForegroundForAdoption()
        #expect(adoption == .claimed)
        #expect(state.foregroundActive)
    }

    @Test("A start that kept its claim through the wait needs no reclaim")
    func startClaimHeldThroughWait() {
        var state = SyncLifecycleState()
        state.claimForegroundForStart()
        #expect(state.reclaimForegroundForStart(sceneForeground: true) == .held)
        #expect(state.foregroundActive)
        // Even with the scene already gone, a claim still held is not dropped
        // here — only the release paths drop it.
        #expect(state.reclaimForegroundForStart(sceneForeground: false) == .held)
        #expect(state.foregroundActive)
    }

    @Test("A start whose claim was released mid-wait reclaims only while the scene is in the foreground")
    func startClaimReleasedMidWait() {
        var state = SyncLifecycleState()
        state.claimForegroundForStart()
        state.releaseForeground() // scene went to the background

        #expect(state.reclaimForegroundForStart(sceneForeground: false) == .abandonedSceneLeftForeground)
        #expect(!state.foregroundActive, "an abandoned start leaves no claim behind")
        // A background stop can still begin — the engine is the background's.
        #expect(state.beginBackgroundStop() == .claimed)
        state.endBackgroundStop()

        // Scene returned inside the wait window: the start takes it back.
        #expect(state.reclaimForegroundForStart(sceneForeground: true) == .reclaimed)
        #expect(state.foregroundActive)
        #expect(state.beginBackgroundStop() == .refusedForegroundOwns, "the reclaimed lifecycle excludes background stops again")
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

    @Test("A second background stop claim is refused while the first holds, and distinct from the foreground refusal")
    func secondStopClaimRefused() {
        var state = SyncLifecycleState()
        let first = state.beginBackgroundStop()
        #expect(first == .claimed)
        let second = state.beginBackgroundStop()
        #expect(second == .refusedAlreadyStopping, "a second holder would release the flag under the first stop")
        #expect(state.backgroundStopInProgress)
        state.endBackgroundStop()
        #expect(!state.backgroundStopInProgress, "only one release, by the one holder")
        let third = state.beginBackgroundStop()
        #expect(third == .claimed)
    }

    @Test("The single-flight knows which run token leads; followers and finished leaders are not leaders")
    func leaderTokenIsTracked() async {
        let flight = BackgroundSyncSingleFlight()
        let leaderToken = BackgroundSyncRunToken()
        let followerToken = BackgroundSyncRunToken()
        #expect(await flight.enter(token: leaderToken) == .leader)
        #expect(flight.isLeader(leaderToken))
        #expect(!flight.isLeader(followerToken))

        let registration = AsyncStream<Void>.makeStream()
        let follower = Task {
            await flight.enter(token: followerToken, onFollow: { registration.continuation.yield(()) })
        }
        for await _ in registration.stream { break }
        #expect(!flight.isLeader(followerToken), "a follower never owns the engine")
        #expect(flight.isLeader(leaderToken))

        flight.finish(.synced)
        _ = await follower.value
        #expect(!flight.isLeader(leaderToken), "a finished leader owns nothing")
    }
}

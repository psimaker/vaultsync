import Foundation
import os
import Testing
@testable import VaultSync

@Suite("Security-scoped lease ownership (#147)")
struct SecurityScopedLeaseOwnershipTests {

    private final class LeaseLedger: @unchecked Sendable {
        private struct State {
            var startAttempts = 0
            var starts = 0
            var stops = 0
        }

        private let state = OSAllocatedUnfairLock(initialState: State())

        var startAttempts: Int {
            state.withLock { $0.startAttempts }
        }

        var starts: Int {
            state.withLock { $0.starts }
        }

        var stops: Int {
            state.withLock { $0.stops }
        }

        var active: Int {
            state.withLock { $0.starts - $0.stops }
        }

        func environment(startSucceeds: Bool = true) -> BookmarkService.AccessEnvironment {
            BookmarkService.AccessEnvironment(
                start: { [self] _ in
                    state.withLock {
                        $0.startAttempts += 1
                        if startSucceeds {
                            $0.starts += 1
                        }
                    }
                    return startSucceeds
                },
                stop: { [self] _ in
                    state.withLock { $0.stops += 1 }
                }
            )
        }
    }

    private let url = URL(fileURLWithPath: "/issue-147/Obsidian", isDirectory: true)

    @Test("A failed acquire never produces a stop")
    func issue147FailedAcquireDoesNotStop() {
        let ledger = LeaseLedger()

        let lease = BookmarkService.acquireAccess(
            to: url,
            owner: .foreground,
            environment: ledger.environment(startSucceeds: false)
        )

        #expect(lease == nil)
        #expect(ledger.startAttempts == 1)
        #expect(ledger.starts == 0)
        #expect(ledger.stops == 0)
        #expect(ledger.active == 0)
    }

    @Test("Explicit release and deinit remain idempotent")
    func issue147ExplicitReleaseAndDeinitStopExactlyOnce() {
        let ledger = LeaseLedger()
        var lease = BookmarkService.acquireAccess(
            to: url,
            owner: .foreground,
            environment: ledger.environment()
        )

        #expect(lease != nil)
        lease?.release()
        lease?.release()
        #expect(lease?.isActive == false)
        #expect(ledger.starts == 1)
        #expect(ledger.stops == 1)

        lease = nil
        #expect(ledger.stops == 1)
        #expect(ledger.active == 0)
    }

    @Test("Deinit balances an otherwise unreleased lease")
    func issue147DeinitBackstopStopsExactlyOnce() {
        let ledger = LeaseLedger()
        var lease = BookmarkService.acquireAccess(
            to: url,
            owner: .foreground,
            environment: ledger.environment()
        )
        weak let weakLease = lease

        #expect(lease != nil)
        #expect(ledger.starts == 1)
        #expect(ledger.stops == 0)

        lease = nil

        #expect(weakLease == nil)
        #expect(ledger.stops == 1)
        #expect(ledger.active == 0)
    }

    @Test("Concurrent terminal cleanup stops one lease exactly once")
    func issue147ConcurrentReleaseStopsExactlyOnce() async throws {
        let ledger = LeaseLedger()
        let lease = try #require(BookmarkService.acquireAccess(
            to: url,
            owner: .background(UUID()),
            environment: ledger.environment()
        ))

        await withTaskGroup(of: Void.self) { group in
            for _ in 0..<32 {
                group.addTask {
                    lease.release()
                }
            }
        }

        #expect(!lease.isActive)
        #expect(ledger.starts == 1)
        #expect(ledger.stops == 1)
        #expect(ledger.active == 0)
    }

    @Test("Background cleanup releases only its token for a shared URL")
    func issue147BackgroundReleasePreservesForegroundToken() throws {
        let ledger = LeaseLedger()
        let environment = ledger.environment()
        let runID = UUID()
        let foreground = try #require(BookmarkService.acquireAccess(
            to: url,
            owner: .foreground,
            environment: environment
        ))
        let background = try #require(BookmarkService.acquireAccess(
            to: url,
            owner: .background(runID),
            environment: environment
        ))

        #expect(foreground.id != background.id)
        #expect(foreground.owner == .foreground)
        #expect(background.owner == .background(runID))
        #expect(ledger.starts == 2)
        #expect(ledger.active == 2)

        background.release()

        #expect(foreground.isActive)
        #expect(!background.isActive)
        #expect(ledger.stops == 1)
        #expect(ledger.active == 1)

        foreground.release()

        #expect(ledger.stops == 2)
        #expect(ledger.active == 0)
    }

    @Test("Forced restart reuses one run-owned background lease")
    func issue147ForcedRestartEnsureReusesAndReleasesLease() {
        let ledger = LeaseLedger()
        let runID = UUID()
        let managedAccess = BackgroundSecurityScopedAccess()
        let testURL = url

        let acquire: @Sendable () -> SecurityScopedLease? = {
            BookmarkService.acquireAccess(
                to: testURL,
                owner: .background(runID),
                environment: ledger.environment()
            )
        }

        #expect(managedAccess.ensureAccess(using: acquire))
        #expect(managedAccess.ensureAccess(using: acquire))
        #expect(managedAccess.hasLease)
        #expect(managedAccess.url == url)
        #expect(ledger.starts == 1)
        #expect(ledger.stops == 0)

        managedAccess.release()
        managedAccess.release()

        #expect(!managedAccess.hasLease)
        #expect(managedAccess.url == nil)
        #expect(ledger.stops == 1)
        #expect(ledger.active == 0)
    }

    @Test("Failed background ensure stays empty and never stops")
    func issue147FailedBackgroundEnsureDoesNotOwnOrStopLease() {
        let ledger = LeaseLedger()
        let managedAccess = BackgroundSecurityScopedAccess()
        let testURL = url

        let acquired = managedAccess.ensureAccess {
            BookmarkService.acquireAccess(
                to: testURL,
                owner: .background(UUID()),
                environment: ledger.environment(startSucceeds: false)
            )
        }

        #expect(!acquired)
        #expect(!managedAccess.hasLease)
        #expect(managedAccess.url == nil)

        managedAccess.release()

        #expect(ledger.startAttempts == 1)
        #expect(ledger.starts == 0)
        #expect(ledger.stops == 0)
    }

    @Test("Task cancellation balances the production background run lease scope")
    func issue147CancellationBalancesProductionBackgroundRunLeaseExactlyOnce() async {
        let ledger = LeaseLedger()
        let (events, eventContinuation) = AsyncStream<Void>.makeStream()
        let observedAccess = OSAllocatedUnfairLock<BackgroundSecurityScopedAccess?>(
            initialState: nil
        )
        let testURL = url

        let task = Task {
            await BackgroundSecurityScopedAccess.withRunOwnedLease { managedAccess, owner in
                observedAccess.withLock { $0 = managedAccess }
                guard managedAccess.ensureAccess(using: {
                    BookmarkService.acquireAccess(
                        to: testURL,
                        owner: owner,
                        environment: ledger.environment()
                    )
                }) else {
                    Issue.record("The deterministic background acquire unexpectedly failed")
                    eventContinuation.finish()
                    return
                }

                eventContinuation.yield(())
                eventContinuation.finish()
                do {
                    try await Task.sleep(for: .seconds(30))
                } catch {
                    // Cancellation returns through the exact defer scope used
                    // by BackgroundSyncService.performBackgroundSync.
                }
            }
        }

        var observedAcquire = false
        for await _ in events {
            observedAcquire = true
        }
        #expect(observedAcquire)
        #expect(observedAccess.withLock { $0?.hasLease } == true)
        #expect(ledger.starts == 1)
        #expect(ledger.stops == 0)

        task.cancel()
        await task.value

        #expect(observedAccess.withLock { $0?.hasLease } == false)
        #expect(ledger.stops == 1)
        #expect(ledger.active == 0)

        observedAccess.withLock { $0 }?.release()
        #expect(ledger.stops == 1)
    }

    @Test("Expiration before task creation gates work as cancelled")
    func issue147ExpirationBeforeTaskCreationStartsCancelled() async {
        let relay = BackgroundTaskCancellationRelay()
        let cancellations = OSAllocatedUnfairLock(initialState: 0)

        relay.expire()
        let task = relay.makeTask {
            if Task.isCancelled {
                cancellations.withLock { $0 += 1 }
            }
        }
        await task.value
        relay.expire()
        relay.finish()

        #expect(cancellations.withLock { $0 } == 1)
    }

    @Test("Expiration after task creation cancels running work exactly once")
    func issue147ExpirationAfterTaskCreationCancelsExactlyOnce() async {
        let relay = BackgroundTaskCancellationRelay()
        let cancellations = OSAllocatedUnfairLock(initialState: 0)
        let (events, eventContinuation) = AsyncStream<Void>.makeStream()

        let task = relay.makeTask {
            eventContinuation.yield(())
            eventContinuation.finish()
            do {
                try await Task.sleep(for: .seconds(30))
            } catch {
                cancellations.withLock { $0 += 1 }
            }
        }
        for await _ in events {
            break
        }
        relay.expire()
        relay.expire()
        await task.value
        relay.finish()

        #expect(cancellations.withLock { $0 } == 1)
    }
}

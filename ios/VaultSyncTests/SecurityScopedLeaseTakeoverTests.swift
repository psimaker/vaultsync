import Foundation
import Testing
import os
@testable import VaultSync

@MainActor
@Suite("Security-scoped lease takeover (#147)")
struct SecurityScopedLeaseTakeoverTests {

    private enum Event: Equatable, Sendable {
        case start(URL)
        case startFailed(URL)
        case stop(URL)
        case validate(URL)
        case scan(URL)
        case makeBookmark(URL)
        case persistBookmark(URL)
        case refreshBookmark(URL)
        case resolveBookmark
        case hasBookmark
        case cleanupLegacyBookmarks
    }

    private final class EventLog: @unchecked Sendable {
        private let state = OSAllocatedUnfairLock(initialState: [Event]())

        var values: [Event] {
            state.withLock { $0 }
        }

        func append(_ event: Event) {
            state.withLock { $0.append(event) }
        }

        func reset() {
            state.withLock { $0.removeAll() }
        }
    }

    private final class LeaseLedger: @unchecked Sendable {
        private struct State: Sendable {
            var attempts: [URL: Int] = [:]
            var starts: [URL: Int] = [:]
            var stops: [URL: Int] = [:]
            var startFailures: Set<URL> = []
        }

        private let state = OSAllocatedUnfairLock(initialState: State())
        private let events: EventLog
        private let onStop: @Sendable (URL) -> Void

        init(
            events: EventLog,
            onStop: @escaping @Sendable (URL) -> Void = { _ in }
        ) {
            self.events = events
            self.onStop = onStop
        }

        var attempts: [URL: Int] {
            state.withLock { $0.attempts }
        }

        var starts: [URL: Int] {
            state.withLock { $0.starts }
        }

        var stops: [URL: Int] {
            state.withLock { $0.stops }
        }

        func failStart(for url: URL) {
            _ = state.withLock { $0.startFailures.insert(url) }
        }

        func start(_ url: URL) -> Bool {
            let succeeded = state.withLock { state in
                state.attempts[url, default: 0] += 1
                guard !state.startFailures.contains(url) else { return false }
                state.starts[url, default: 0] += 1
                return true
            }
            events.append(succeeded ? .start(url) : .startFailed(url))
            return succeeded
        }

        func stop(_ url: URL) {
            state.withLock { $0.stops[url, default: 0] += 1 }
            events.append(.stop(url))
            onStop(url)
        }

        func activeCount(for url: URL) -> Int {
            state.withLock {
                $0.starts[url, default: 0] - $0.stops[url, default: 0]
            }
        }
    }

    private enum ScanResult {
        case vaults([String])
        case failure
    }

    private struct InjectedBookmarkError: LocalizedError {
        var errorDescription: String? { "Injected bookmark failure" }
    }

    @MainActor
    private final class Harness {
        let events: EventLog
        let leases: LeaseLedger

        var validationErrors: [URL: String] = [:]
        var scanResults: [URL: ScanResult] = [:]
        var bookmarkFailures: Set<URL> = []
        var resolvedBookmark: BookmarkService.ResolvedBookmark?
        var bookmarkRefreshSucceeds = true
        var hasBookmarkValue = false
        var storedBookmarkURL: URL?
        var legacyCleanupCount = 0

        private var nextBookmarkPayload = 0
        private var preparedBookmarks: [Data: URL] = [:]

        init(onStop: @escaping @Sendable (URL) -> Void = { _ in }) {
            let events = EventLog()
            self.events = events
            self.leases = LeaseLedger(events: events, onStop: onStop)
        }

        func environment() -> VaultManager.Environment {
            let leases = leases
            let accessEnvironment = BookmarkService.AccessEnvironment(
                start: { leases.start($0) },
                stop: { leases.stop($0) }
            )

            return VaultManager.Environment(
                acquireAccess: { url, owner in
                    BookmarkService.acquireAccess(
                        to: url,
                        owner: owner,
                        environment: accessEnvironment
                    )
                },
                sameResource: { $0.standardizedFileURL == $1.standardizedFileURL },
                validateDirectory: { [self] url in
                    events.append(.validate(url))
                    return validationErrors[url]
                },
                makeBookmarkData: { [self] url in
                    events.append(.makeBookmark(url))
                    if bookmarkFailures.contains(url) {
                        throw InjectedBookmarkError()
                    }
                    nextBookmarkPayload += 1
                    let data = Data("issue-147-bookmark-\(nextBookmarkPayload)".utf8)
                    preparedBookmarks[data] = url
                    return data
                },
                persistBookmarkData: { [self] data in
                    guard let url = preparedBookmarks[data] else {
                        preconditionFailure("Persisted an unknown prepared bookmark")
                    }
                    storedBookmarkURL = url
                    events.append(.persistBookmark(url))
                },
                refreshBookmarkData: { [self] data, _ in
                    guard let url = preparedBookmarks[data] else {
                        preconditionFailure("Refreshed with an unknown prepared bookmark")
                    }
                    events.append(.refreshBookmark(url))
                    if bookmarkRefreshSucceeds {
                        storedBookmarkURL = url
                    }
                    return bookmarkRefreshSucceeds
                },
                resolveBookmark: { [self] in
                    events.append(.resolveBookmark)
                    return resolvedBookmark
                },
                hasBookmark: { [self] in
                    events.append(.hasBookmark)
                    return hasBookmarkValue
                },
                scanVaults: { [self] url in
                    events.append(.scan(url))
                    switch scanResults[url] ?? .vaults([]) {
                    case .vaults(let names):
                        return names
                    case .failure:
                        return nil
                    }
                },
                cleanupLegacyBookmarks: { [self] in
                    legacyCleanupCount += 1
                    events.append(.cleanupLegacyBookmarks)
                }
            )
        }
    }

    private struct ManagerSnapshot: Equatable, Sendable {
        let url: URL?
        let isAccessible: Bool
        let detectedVaults: [String]
        let needsReconnect: Bool
        let hasAccessIssue: Bool
        let selectionAdvisory: String?
    }

    /// The lease callback is `@Sendable`, while the manager is MainActor-bound.
    /// Production releases the replaced foreground token synchronously inside
    /// `grantAccess`, so this probe safely verifies the state at that exact stop.
    private final class CommitBeforeStopProbe: @unchecked Sendable {
        private let replacedURL: URL
        private let snapshots = OSAllocatedUnfairLock(initialState: [ManagerSnapshot]())
        weak var manager: VaultManager?

        init(replacedURL: URL) {
            self.replacedURL = replacedURL
        }

        func recordIfReplacedLease(_ stoppedURL: URL) {
            guard stoppedURL == replacedURL else { return }
            let snapshot = MainActor.assumeIsolated { [weak self] () -> ManagerSnapshot? in
                guard let manager = self?.manager else { return nil }
                return ManagerSnapshot(
                    url: manager.obsidianDirectoryURL,
                    isAccessible: manager.isAccessible,
                    detectedVaults: manager.detectedVaults,
                    needsReconnect: manager.needsReconnect,
                    hasAccessIssue: manager.accessIssue != nil,
                    selectionAdvisory: manager.selectionAdvisory
                )
            }
            if let snapshot {
                snapshots.withLock { $0.append(snapshot) }
            }
        }

        var values: [ManagerSnapshot] {
            snapshots.withLock { $0 }
        }
    }

    private static func url(_ name: String) -> URL {
        URL(
            fileURLWithPath: "/VaultSyncTests/Issue147/\(name)/Obsidian",
            isDirectory: true
        )
    }

    private static func resolvedBookmark(
        _ url: URL,
        isStale: Bool,
        source: String
    ) -> BookmarkService.ResolvedBookmark {
        BookmarkService.ResolvedBookmark(
            url: url,
            isStale: isStale,
            sourceData: Data(source.utf8)
        )
    }

    private func adoptPrior(
        _ url: URL,
        vaults: [String] = ["PriorVault"],
        harness: Harness
    ) -> VaultManager {
        harness.scanResults[url] = .vaults(vaults)
        let manager = VaultManager(environment: harness.environment())
        #expect(manager.grantAccess(url: url) == nil)
        expectAdopted(manager, url: url, vaults: vaults)
        #expect(harness.leases.activeCount(for: url) == 1)
        #expect(harness.storedBookmarkURL == url)
        return manager
    }

    private func expectAdopted(
        _ manager: VaultManager,
        url: URL,
        vaults: [String]
    ) {
        #expect(manager.obsidianDirectoryURL == url)
        #expect(manager.isAccessible)
        #expect(manager.detectedVaults == vaults)
        #expect(!manager.needsReconnect)
        #expect(manager.accessIssue == nil)
    }

    private func expectReconnectRequired(_ manager: VaultManager) {
        #expect(manager.obsidianDirectoryURL == nil)
        #expect(!manager.isAccessible)
        #expect(manager.detectedVaults.isEmpty)
        #expect(manager.needsReconnect)
        #expect(manager.accessIssue != nil)
    }

    @Test("Repeated selection of the same URL reuses one active lease (#147)")
    func issue147SameURLDoesNotAcquireAnotherLease() {
        let url = Self.url("same-url")
        let harness = Harness()
        let manager = adoptPrior(url, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount

        harness.scanResults[url] = .vaults(["RefreshedVault"])
        harness.events.reset()

        #expect(manager.grantAccess(url: url) == nil)

        #expect(harness.leases.attempts[url] == 1)
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url, default: 0] == 0)
        #expect(harness.leases.activeCount(for: url) == 1)
        #expect(harness.events.values == [
            .validate(url),
            .scan(url),
            .makeBookmark(url),
            .persistBookmark(url),
            .cleanupLegacyBookmarks,
        ])
        #expect(harness.legacyCleanupCount == cleanupBefore + 1)
        expectAdopted(manager, url: url, vaults: ["RefreshedVault"])
    }

    @Test("A successful URL change commits B before stopping A (#147)")
    func issue147SuccessfulChangeCommitsBeforeStoppingPriorLease() {
        let priorURL = Self.url("successful-change-a")
        let candidateURL = Self.url("successful-change-b")
        let probe = CommitBeforeStopProbe(replacedURL: priorURL)
        let harness = Harness(onStop: { probe.recordIfReplacedLease($0) })
        let manager = adoptPrior(priorURL, harness: harness)
        probe.manager = manager
        harness.scanResults[candidateURL] = .vaults(["CandidateVault"])
        harness.events.reset()

        #expect(manager.grantAccess(url: candidateURL) == nil)

        #expect(harness.events.values == [
            .start(candidateURL),
            .validate(candidateURL),
            .scan(candidateURL),
            .makeBookmark(candidateURL),
            .persistBookmark(candidateURL),
            .stop(priorURL),
            .cleanupLegacyBookmarks,
        ])
        #expect(probe.values == [ManagerSnapshot(
            url: candidateURL,
            isAccessible: true,
            detectedVaults: ["CandidateVault"],
            needsReconnect: false,
            hasAccessIssue: false,
            selectionAdvisory: nil
        )])
        #expect(harness.storedBookmarkURL == candidateURL)
        #expect(harness.leases.activeCount(for: priorURL) == 0)
        #expect(harness.leases.activeCount(for: candidateURL) == 1)
        expectAdopted(manager, url: candidateURL, vaults: ["CandidateVault"])
    }

    @Test("A failed start creates no lease and preserves A (#147)")
    func issue147StartFailurePreservesPriorStateWithoutStop() {
        let priorURL = Self.url("start-failure-a")
        let candidateURL = Self.url("start-failure-b")
        let harness = Harness()
        let manager = adoptPrior(priorURL, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount
        harness.leases.failStart(for: candidateURL)
        harness.events.reset()

        let error = manager.grantAccess(url: candidateURL)

        #expect(error == L10n.tr("Could not access the selected folder."))
        #expect(harness.events.values == [.startFailed(candidateURL)])
        #expect(harness.leases.attempts[candidateURL] == 1)
        #expect(harness.leases.starts[candidateURL, default: 0] == 0)
        #expect(harness.leases.stops[candidateURL, default: 0] == 0)
        #expect(harness.leases.activeCount(for: priorURL) == 1)
        #expect(harness.storedBookmarkURL == priorURL)
        #expect(harness.legacyCleanupCount == cleanupBefore)
        expectAdopted(manager, url: priorURL, vaults: ["PriorVault"])
    }

    @Test("Validation failure releases only B and preserves A (#147)")
    func issue147ValidationFailureRollsBackCandidate() {
        let priorURL = Self.url("validation-failure-a")
        let candidateURL = Self.url("validation-failure-b")
        let validationError = "Injected validation failure"
        let harness = Harness()
        let manager = adoptPrior(priorURL, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount
        harness.validationErrors[candidateURL] = validationError
        harness.events.reset()

        #expect(manager.grantAccess(url: candidateURL) == validationError)

        #expect(harness.events.values == [
            .start(candidateURL),
            .validate(candidateURL),
            .stop(candidateURL),
        ])
        #expect(harness.leases.activeCount(for: priorURL) == 1)
        #expect(harness.leases.activeCount(for: candidateURL) == 0)
        #expect(harness.leases.stops[candidateURL] == 1)
        #expect(harness.storedBookmarkURL == priorURL)
        #expect(harness.legacyCleanupCount == cleanupBefore)
        expectAdopted(manager, url: priorURL, vaults: ["PriorVault"])
    }

    @Test("Bookmark creation failure releases only B and preserves A (#147)")
    func issue147BookmarkFailureRollsBackCandidate() {
        let priorURL = Self.url("bookmark-failure-a")
        let candidateURL = Self.url("bookmark-failure-b")
        let harness = Harness()
        let manager = adoptPrior(priorURL, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount
        harness.scanResults[candidateURL] = .vaults(["CandidateVault"])
        harness.bookmarkFailures.insert(candidateURL)
        harness.events.reset()

        let error = manager.grantAccess(url: candidateURL)

        #expect(error?.contains("Injected bookmark failure") == true)
        #expect(harness.events.values == [
            .start(candidateURL),
            .validate(candidateURL),
            .scan(candidateURL),
            .makeBookmark(candidateURL),
            .stop(candidateURL),
        ])
        #expect(harness.leases.activeCount(for: priorURL) == 1)
        #expect(harness.leases.activeCount(for: candidateURL) == 0)
        #expect(harness.leases.stops[candidateURL] == 1)
        #expect(harness.storedBookmarkURL == priorURL)
        #expect(harness.legacyCleanupCount == cleanupBefore)
        expectAdopted(manager, url: priorURL, vaults: ["PriorVault"])
    }

    @Test("Scan failure preserves the prior adopted lease and reports failure (#147)")
    func issue147ScanFailurePreservesPriorLeaseAndReturnsFailure() {
        let priorURL = Self.url("scan-failure-a")
        let candidateURL = Self.url("scan-failure-b")
        let harness = Harness()
        let manager = adoptPrior(priorURL, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount
        harness.scanResults[candidateURL] = .failure
        harness.events.reset()

        let error = manager.grantAccess(url: candidateURL)

        #expect(error == L10n.tr("VaultSync can no longer read your Obsidian directory. Reconnect the folder to restore sync access."))
        #expect(harness.events.values == [
            .start(candidateURL),
            .validate(candidateURL),
            .scan(candidateURL),
            .stop(candidateURL),
        ])
        #expect(harness.leases.starts[priorURL] == 1)
        #expect(harness.leases.stops[priorURL, default: 0] == 0)
        #expect(harness.leases.starts[candidateURL] == 1)
        #expect(harness.leases.stops[candidateURL] == 1)
        #expect(harness.leases.activeCount(for: priorURL) == 1)
        #expect(harness.leases.activeCount(for: candidateURL) == 0)
        #expect(harness.storedBookmarkURL == priorURL)
        #expect(harness.legacyCleanupCount == cleanupBefore)
        expectAdopted(manager, url: priorURL, vaults: ["PriorVault"])
    }

    @Test("Restore while a foreground lease is active is a no-op (#147)")
    func issue147RestoreWhileActiveDoesNotAcquireOrResolveAgain() {
        let activeURL = Self.url("restore-no-op-a")
        let ignoredURL = Self.url("restore-no-op-b")
        let harness = Harness()
        let manager = adoptPrior(activeURL, harness: harness)
        harness.resolvedBookmark = Self.resolvedBookmark(
            ignoredURL,
            isStale: false,
            source: "ignored-active-restore"
        )
        harness.events.reset()

        manager.restoreAccess()

        #expect(harness.events.values.isEmpty)
        #expect(harness.leases.attempts[activeURL] == 1)
        #expect(harness.leases.attempts[ignoredURL, default: 0] == 0)
        #expect(harness.leases.activeCount(for: activeURL) == 1)
        expectAdopted(manager, url: activeURL, vaults: ["PriorVault"])
    }

    @Test("Fresh restore adopts one lease after validation and scan (#147)")
    func issue147FreshRestoreAdoptsOneLease() {
        let url = Self.url("fresh-restore")
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            url,
            isStale: false,
            source: "fresh-restore"
        )
        harness.hasBookmarkValue = true
        harness.storedBookmarkURL = url
        harness.scanResults[url] = .vaults(["RestoredVault"])
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .start(url),
            .validate(url),
            .scan(url),
        ])
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url, default: 0] == 0)
        #expect(harness.leases.activeCount(for: url) == 1)
        #expect(harness.storedBookmarkURL == url)
        expectAdopted(manager, url: url, vaults: ["RestoredVault"])
    }

    @Test("Restore acquire failure creates no lease or stop and requires reconnect (#147)")
    func issue147RestoreAcquireFailureDoesNotStop() {
        let url = Self.url("restore-acquire-failure")
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            url,
            isStale: false,
            source: "restore-acquire-failure"
        )
        harness.hasBookmarkValue = true
        harness.storedBookmarkURL = url
        harness.leases.failStart(for: url)
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .startFailed(url),
        ])
        #expect(harness.leases.attempts[url] == 1)
        #expect(harness.leases.starts[url, default: 0] == 0)
        #expect(harness.leases.stops[url, default: 0] == 0)
        #expect(harness.leases.activeCount(for: url) == 0)
        #expect(harness.storedBookmarkURL == url)
        expectReconnectRequired(manager)
    }

    @Test("Restore validation failure releases its candidate and requires reconnect (#147)")
    func issue147RestoreValidationFailureReleasesCandidate() {
        let url = Self.url("restore-validation-failure")
        let validationError = "Injected restore validation failure"
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            url,
            isStale: false,
            source: "restore-validation-failure"
        )
        harness.hasBookmarkValue = true
        harness.storedBookmarkURL = url
        harness.validationErrors[url] = validationError
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .start(url),
            .validate(url),
            .stop(url),
        ])
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url] == 1)
        #expect(harness.leases.activeCount(for: url) == 0)
        #expect(harness.storedBookmarkURL == url)
        expectReconnectRequired(manager)
    }

    @Test("Restore scan failure releases its candidate and requires reconnect (#147)")
    func issue147RestoreScanFailureReleasesCandidate() {
        let url = Self.url("restore-scan-failure")
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            url,
            isStale: false,
            source: "restore-scan-failure"
        )
        harness.hasBookmarkValue = true
        harness.storedBookmarkURL = url
        harness.scanResults[url] = .failure
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .start(url),
            .validate(url),
            .scan(url),
            .stop(url),
        ])
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url] == 1)
        #expect(harness.leases.activeCount(for: url) == 0)
        #expect(harness.storedBookmarkURL == url)
        expectReconnectRequired(manager)
    }

    @Test("Stale restore bookmark failure releases its candidate without replacing the bookmark (#147)")
    func issue147StaleRestoreBookmarkFailureReleasesCandidate() {
        let url = Self.url("restore-stale-bookmark-failure")
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            url,
            isStale: true,
            source: "restore-stale-bookmark-failure"
        )
        harness.hasBookmarkValue = true
        harness.storedBookmarkURL = url
        harness.scanResults[url] = .vaults(["RestoredVault"])
        harness.bookmarkFailures.insert(url)
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .start(url),
            .validate(url),
            .scan(url),
            .makeBookmark(url),
            .stop(url),
        ])
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url] == 1)
        #expect(harness.leases.activeCount(for: url) == 0)
        #expect(harness.storedBookmarkURL == url)
        expectReconnectRequired(manager)
    }

    @Test("Successful stale restore refreshes by CAS and adopts exactly one lease (#147)")
    func issue147SuccessfulStaleRestoreRefreshesAndAdoptsOneLease() {
        let url = Self.url("restore-stale-success")
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            url,
            isStale: true,
            source: "restore-stale-success-source"
        )
        harness.hasBookmarkValue = true
        harness.storedBookmarkURL = url
        harness.scanResults[url] = .vaults(["RestoredVault"])
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .start(url),
            .validate(url),
            .scan(url),
            .makeBookmark(url),
            .refreshBookmark(url),
        ])
        #expect(harness.leases.attempts[url] == 1)
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url, default: 0] == 0)
        #expect(harness.leases.activeCount(for: url) == 1)
        #expect(harness.storedBookmarkURL == url)
        expectAdopted(manager, url: url, vaults: ["RestoredVault"])
    }

    @Test("A stale restore CAS conflict cannot overwrite newer permission state (#147)")
    func issue147StaleRestoreCASConflictReleasesCandidateWithoutAdoption() {
        let staleURL = Self.url("restore-stale-cas-source")
        let newerURL = Self.url("restore-stale-cas-newer")
        let harness = Harness()
        harness.resolvedBookmark = Self.resolvedBookmark(
            staleURL,
            isStale: true,
            source: "stale-source-before-newer-grant"
        )
        harness.hasBookmarkValue = true
        // Models a foreground grant that replaced the stored permission after
        // the stale bookmark snapshot above was resolved.
        harness.storedBookmarkURL = newerURL
        harness.scanResults[staleURL] = .vaults(["StaleVault"])
        harness.bookmarkRefreshSucceeds = false
        let manager = VaultManager(environment: harness.environment())

        manager.restoreAccess()

        #expect(harness.events.values == [
            .resolveBookmark,
            .start(staleURL),
            .validate(staleURL),
            .scan(staleURL),
            .makeBookmark(staleURL),
            .refreshBookmark(staleURL),
            .stop(staleURL),
        ])
        #expect(harness.leases.starts[staleURL] == 1)
        #expect(harness.leases.stops[staleURL] == 1)
        #expect(harness.leases.activeCount(for: staleURL) == 0)
        #expect(harness.storedBookmarkURL == newerURL)
        #expect(harness.legacyCleanupCount == 0)
        expectReconnectRequired(manager)
    }

    @Test("Bookmark refresh compare-and-swap preserves a newer committed bookmark (#147)")
    func issue147BookmarkRefreshCASRejectsStaleSourceBytes() {
        let identifier = "issue-147-cas-\(UUID().uuidString)"
        let sourceA = Data("bookmark-a".utf8)
        let staleRefreshA = Data("bookmark-a-refreshed".utf8)
        let newerB = Data("bookmark-b".utf8)
        let refreshedB = Data("bookmark-b-refreshed".utf8)
        let finalB = Data("bookmark-b-final".utf8)
        defer { BookmarkService.deleteBookmark(identifier: identifier) }

        BookmarkService.persistBookmarkData(sourceA, identifier: identifier)
        BookmarkService.persistBookmarkData(newerB, identifier: identifier)

        #expect(!BookmarkService.refreshBookmarkData(
            staleRefreshA,
            replacing: sourceA,
            identifier: identifier
        ))
        // Success against B proves the rejected A refresh left B untouched.
        #expect(BookmarkService.refreshBookmarkData(
            refreshedB,
            replacing: newerB,
            identifier: identifier
        ))
        #expect(BookmarkService.refreshBookmarkData(
            finalB,
            replacing: refreshedB,
            identifier: identifier
        ))
    }

    @Test("An active rescan failure releases the owned foreground lease exactly once (#147)")
    func issue147ActiveRescanFailureReleasesForegroundLeaseOnce() {
        let url = Self.url("active-rescan-failure")
        let harness = Harness()
        let manager = adoptPrior(url, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount
        harness.scanResults[url] = .failure
        harness.events.reset()

        manager.scanForVaults()

        #expect(harness.events.values == [.scan(url), .stop(url)])
        #expect(harness.leases.starts[url] == 1)
        #expect(harness.leases.stops[url] == 1)
        #expect(harness.leases.activeCount(for: url) == 0)
        #expect(harness.storedBookmarkURL == url)
        #expect(harness.legacyCleanupCount == cleanupBefore)
        expectReconnectRequired(manager)

        harness.events.reset()
        manager.scanForVaults()
        #expect(harness.events.values.isEmpty)
        #expect(harness.leases.stops[url] == 1)
    }

    @Test("A real failed grant suppresses reconnect success, reconcile, and retry (#147)")
    func issue147FailedGrantShortCircuitsObsidianReconnectFlow() async {
        let priorURL = Self.url("flow-failure-a")
        let candidateURL = Self.url("flow-failure-b")
        let harness = Harness()
        let manager = adoptPrior(priorURL, harness: harness)
        let cleanupBefore = harness.legacyCleanupCount
        harness.scanResults[candidateURL] = .failure
        var flowEvents: [String] = []

        let error = await ObsidianReconnectFlow.run(
            grantAccess: {
                flowEvents.append("grant")
                return manager.grantAccess(url: candidateURL)
            },
            onGrantSucceeded: { flowEvents.append("success") },
            reconcile: { flowEvents.append("reconcile") },
            retryPendingShares: { flowEvents.append("retry") }
        )

        #expect(error != nil)
        #expect(flowEvents == ["grant"])
        #expect(harness.leases.activeCount(for: priorURL) == 1)
        #expect(harness.leases.activeCount(for: candidateURL) == 0)
        #expect(harness.leases.stops[candidateURL] == 1)
        #expect(harness.storedBookmarkURL == priorURL)
        #expect(harness.legacyCleanupCount == cleanupBefore)
        expectAdopted(manager, url: priorURL, vaults: ["PriorVault"])
    }
}

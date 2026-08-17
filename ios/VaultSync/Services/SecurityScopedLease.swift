import Foundation
import os

/// The subsystem that owns one successful security-scoped access start.
/// Foreground and background starts remain distinct even for the same URL.
enum SecurityScopedLeaseOwner: Equatable, Sendable {
    case foreground
    case background(UUID)
}

/// A one-shot capability to balance exactly one successful
/// `startAccessingSecurityScopedResource()` call.
///
/// The unchecked Sendable conformance is narrow: the URL, owner, identifier,
/// and stop callback are immutable, while the only mutable bit is protected by
/// `OSAllocatedUnfairLock`. Callers still release explicitly; deinit is only a
/// final leak backstop.
final class SecurityScopedLease: @unchecked Sendable {
    let id: UUID
    let url: URL
    let owner: SecurityScopedLeaseOwner

    private let active = OSAllocatedUnfairLock(initialState: true)
    private let stop: @Sendable (URL) -> Void

    init(
        id: UUID = UUID(),
        url: URL,
        owner: SecurityScopedLeaseOwner,
        stop: @escaping @Sendable (URL) -> Void
    ) {
        self.id = id
        self.url = url
        self.owner = owner
        self.stop = stop
    }

    var isActive: Bool {
        active.withLock { $0 }
    }

    /// Releases this lease at most once, even when terminal paths race or a
    /// defensive cleanup repeats.
    func release() {
        let shouldStop = active.withLock { isActive in
            guard isActive else { return false }
            isActive = false
            return true
        }
        if shouldStop {
            stop(url)
        }
    }

    deinit {
        release()
    }
}

/// Owns the single security-scoped lease acquired by one background run.
/// Production and tests use this same core for forced-restart reuse and
/// idempotent terminal cleanup.
final class BackgroundSecurityScopedAccess: @unchecked Sendable {
    private let lease = OSAllocatedUnfairLock<SecurityScopedLease?>(initialState: nil)

    /// Runs one background operation with a distinct owner and guarantees the
    /// operation's token is released on every return, including cancellation.
    /// `BackgroundSyncService` and its cancellation regression use this exact
    /// scope rather than duplicating terminal cleanup logic.
    static func withRunOwnedLease<Result: Sendable>(
        _ operation: @Sendable (
            BackgroundSecurityScopedAccess,
            SecurityScopedLeaseOwner
        ) async -> Result
    ) async -> Result {
        let managedAccess = BackgroundSecurityScopedAccess()
        let owner = SecurityScopedLeaseOwner.background(UUID())
        defer { managedAccess.release() }
        return await operation(managedAccess, owner)
    }

    var url: URL? {
        lease.withLock { $0?.url }
    }

    var hasLease: Bool {
        lease.withLock { $0 != nil }
    }

    /// Keeps an existing run-owned lease (forced restart) or acquires exactly
    /// one new lease. A failed acquire leaves the owner empty.
    @discardableResult
    func ensureAccess(
        using acquire: @Sendable () -> SecurityScopedLease?
    ) -> Bool {
        lease.withLock { current in
            if current != nil {
                return true
            }
            guard let acquired = acquire() else {
                return false
            }
            current = acquired
            return true
        }
    }

    /// Detaches before stopping so re-entrant or repeated cleanup cannot
    /// consume the same token twice.
    func release() {
        let owned = lease.withLock { current in
            defer { current = nil }
            return current
        }
        owned?.release()
    }

    deinit {
        release()
    }
}

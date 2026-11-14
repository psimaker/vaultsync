import Foundation

/// Tracks whether the folder paths that accept decisions judge against are
/// *settled*: no path reconcile in flight, and one has completed since the
/// engine (re)started. Accept decisions — the automatic pass, manual accepts,
/// and merge confirmations — run only on settled paths (#56, decision 008):
/// a pass running concurrently with `reconcileFolderPaths` would derive its
/// occupied-path overlap set from the pre-reconcile folder list — stale
/// exactly after an iOS container move, when the reconcile is about to rebase
/// folders onto the very paths the accept would take.
///
/// Fail-closed by construction — a failure can only hold accepts, never
/// unlock them:
/// - `settled` requires a **completed** reconcile in the current generation;
///   an abandoned pass (engine died mid-run) leaves paths unsettled.
/// - `reset()` starts a new generation (engine stop/restart). Outcomes
///   reported by a previous generation's still-running detached task are
///   ignored via the generation token, so a stale completion cannot settle
///   paths the new engine start has not reconciled yet.
///
/// Pure value type (no bridge, no filesystem, no clock) so the lifecycle is
/// exhaustively unit-testable — `PathSettlementTests` (#56).
struct PathSettlement {
    /// Identifies a reconcile pass: the engine generation it belongs to and
    /// its place among the passes begun.
    struct Token: Equatable, Sendable {
        fileprivate let generation: Int
        fileprivate let pass: Int
    }

    private var generation = 0
    private var inFlight = 0
    private var completedThisGeneration = false
    private var passesBegun = 0

    /// True when accept decisions may run: nothing is reconciling right now
    /// and at least one reconcile completed since the last `reset()`.
    var settled: Bool { inFlight == 0 && completedThisGeneration }

    /// A reconcile pass started; paths are unsettled until it reports back.
    mutating func reconcileBegan() -> Token {
        inFlight += 1
        passesBegun += 1
        return Token(generation: generation, pass: passesBegun)
    }

    /// The pass ran to completion — paths now reflect the current root.
    ///
    /// Returns whether what this pass found is the current picture: it
    /// belongs to this generation and no pass began after it. Passes overlap
    /// (a cold start, then a newly selected folder) and finish in any order;
    /// a pass that was overtaken judged the folders against an earlier
    /// selection, and what it found must not replace what the later one
    /// finds (#315).
    @discardableResult
    mutating func reconcileFinished(token: Token) -> Bool {
        guard token.generation == generation else { return false }
        inFlight -= 1
        completedThisGeneration = true
        return token.pass == passesBegun
    }

    /// The pass bailed without reconciling (engine died mid-run). Deliberately
    /// does NOT settle: nothing was verified, so accepts stay held until a
    /// fresh engine start's reconcile completes.
    mutating func reconcileAbandoned(token: Token) {
        guard token.generation == generation else { return }
        inFlight -= 1
    }

    /// New engine generation (stop/restart): unsettled until the next start's
    /// reconcile completes; earlier generations' outcomes no longer count.
    mutating func reset() {
        generation += 1
        inFlight = 0
        completedThisGeneration = false
    }
}

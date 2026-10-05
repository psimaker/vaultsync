import Foundation

/// One honest status per vault (#187) — the vault-level twin of
/// `SyncHeaderModel`. The vault row on the home screen, the vault screen's
/// status pill and the hero's vault chip all render this, so a vault can
/// never look green in one place and stuck in another.
///
/// The precedence mirrors the header cascade (decision 012): failures first,
/// then an active transfer (short-lived; it outranks waiting decisions so a
/// row does not flicker mid-transfer, exactly like the header), then
/// decisions waiting on the user, then the calm states. A vault that has
/// never completed a sync never claims "Up to Date" (#94), and a state the
/// engine has not reported is never rendered as a status at all.
///
/// Pure and value-typed so the cascade is exhaustively unit-testable.
enum VaultStatusModel {
    struct Inputs: Equatable {
        /// Raw engine folder state ("idle", "scanning", "syncing", "error",
        /// …); nil while no status has been read for the folder.
        var engineState: String?
        /// The folder is paused in the engine config (by the user's desktop,
        /// or by the overlap shield, #45).
        var folderPaused: Bool
        /// The folder sits on a dead path the launch reconcile could not
        /// heal (#25) — surfaced with its own recovery card.
        var unreachable: Bool
        /// Distinct conflicted files attributed to this vault.
        var conflictCount: Int
        /// A successful sync has been recorded for the folder (#94).
        var hasCompletedSync: Bool
        /// Sync completion (local vs. global index), when reported. Not scan
        /// progress — a scan shows no percentage.
        var completionPct: Double?
    }

    enum Label: Equatable {
        case unreachable
        case paused
        case error
        case scanning
        case syncing(percent: Int?)
        case conflicts(Int)
        case waitingForFirstSync
        case upToDate
        case unknown
    }

    struct State: Equatable {
        /// nil when the engine has not reported a state this model can
        /// vouch for — rendered neutral, never green.
        let status: SyncStatus?
        let label: Label
    }

    static func derive(_ inputs: Inputs) -> State {
        if inputs.unreachable {
            return State(status: .error, label: .unreachable)
        }
        if inputs.folderPaused {
            return State(status: .paused, label: .paused)
        }
        let state = inputs.engineState?.lowercased()
        if state == "error" {
            return State(status: .error, label: .error)
        }
        // Only the two states the header counts as "syncing" (decision 012)
        // render as a transfer here — anything wider would let a row say
        // "Syncing" under a header that says "All Synced".
        if state == "syncing" {
            return State(status: .syncing, label: .syncing(percent: inProgressPercent(inputs.completionPct)))
        }
        if state == "scanning" {
            // completionPct measures sync completion, not progress through
            // the scan — "Scanning (63%)" would read as scan progress.
            return State(status: .syncing, label: .scanning)
        }
        if inputs.conflictCount > 0 {
            return State(status: .attention, label: .conflicts(inputs.conflictCount))
        }
        if state == "idle" {
            return inputs.hasCompletedSync
                ? State(status: .synced, label: .upToDate)
                : State(status: .starting, label: .waitingForFirstSync)
        }
        return State(status: nil, label: .unknown)
    }

    /// A percentage worth showing: strictly between 0 and 100. At 0 the
    /// engine has not measured anything yet; at 100 the transfer is done and
    /// "Syncing (100%)" would read as a contradiction.
    static func inProgressPercent(_ completionPct: Double?) -> Int? {
        guard let completionPct, completionPct > 0, completionPct < 100 else { return nil }
        return Int(completionPct)
    }
}

extension VaultStatusModel.Label {
    /// Localized short label (English literal keys, decision 005).
    var text: String {
        switch self {
        case .unreachable:
            return L10n.tr("Folder Path Missing")
        case .paused:
            return L10n.tr("Paused")
        case .error:
            return L10n.tr("Error")
        case .scanning:
            return L10n.tr("Scanning")
        case .syncing(let percent):
            return Self.withPercent(L10n.tr("Syncing"), percent)
        case .conflicts(let count):
            return count == 1 ? L10n.tr("1 conflict") : L10n.fmt("%d conflicts", count)
        case .waitingForFirstSync:
            return L10n.tr("Waiting for first sync")
        case .upToDate:
            return L10n.tr("Up to Date")
        case .unknown:
            return L10n.tr("Unknown")
        }
    }

    private static func withPercent(_ base: String, _ percent: Int?) -> String {
        guard let percent else { return base }
        return base + " " + L10n.fmt("(%d%%)", percent)
    }
}

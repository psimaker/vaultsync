import Foundation

/// Pure derivation of the dashboard status header (#66, decision 012).
///
/// The header used to be computed from a disjoint input set (engine error,
/// running flag, folder errors, syncing) while the "Sync Issues" section
/// rendered `SyncthingManager.unresolvedIssues` — so the entire warning tier
/// (parked shares, disconnected required peers, conflicts, stale sync) never
/// reached the header and a green "All Synced" coexisted with visible issue
/// rows. The header now derives from the max severity of that same issue
/// list, and "Ready" is claimed only when the app is genuinely armed to
/// accept a share (Obsidian folder accessible and a vault exists).
///
/// Pure and value-typed so the precedence cascade is exhaustively
/// unit-testable without a manager or the bridge.
enum SyncHeaderModel {
    struct Inputs {
        /// Engine-level failure (`SyncthingManager.userError` / `.error`).
        var hasEngineError: Bool
        var engineRunning: Bool
        /// Severities of `SyncthingManager.unresolvedIssues` — the same list
        /// the "Sync Issues" section renders.
        var issueSeverities: [SyncthingManager.SyncIssueSeverity]
        /// Folders on dead paths (issue #25) — surfaced by their own section,
        /// not by `unresolvedIssues`, so they need their own input to keep the
        /// header from claiming "All Synced" above their recovery card.
        var hasUnreachableFolders: Bool
        var isSyncing: Bool
        var hasSyncFolders: Bool
        var vaultAccessible: Bool
        /// Distinguishes "access expired" (action needed) from "never
        /// connected" (finish setup) when the vault is not accessible.
        var vaultNeedsReconnect: Bool
        var hasDetectedVaults: Bool
    }

    struct State: Equatable {
        let status: SyncStatus
        /// English L10n key (decision 005) — the view resolves it via `L10n.tr`.
        let titleKey: String
    }

    /// Precedence: engine failure → engine starting → critical issues →
    /// active transfer → warning-tier issues → not armed → armed/synced.
    /// A transfer outranks warnings deliberately: syncing is short-lived and
    /// the warning surfaces the moment it settles, instead of the header
    /// flickering between the two mid-transfer.
    static func derive(_ inputs: Inputs) -> State {
        if inputs.hasEngineError {
            return State(status: .error, titleKey: "Error")
        }
        if !inputs.engineRunning {
            return State(status: .starting, titleKey: "Starting…")
        }
        if inputs.issueSeverities.contains(.critical) || inputs.hasUnreachableFolders {
            return State(status: .attention, titleKey: "Sync Issue")
        }
        if inputs.isSyncing {
            return State(status: .syncing, titleKey: "Syncing…")
        }
        if !inputs.issueSeverities.isEmpty {
            return State(status: .attention, titleKey: "Action Needed")
        }
        if !inputs.vaultAccessible {
            return State(
                status: .attention,
                titleKey: inputs.vaultNeedsReconnect ? "Action Needed" : "Finish Setup"
            )
        }
        if inputs.hasSyncFolders {
            return State(status: .synced, titleKey: "All Synced")
        }
        if inputs.hasDetectedVaults {
            // Genuinely armed: the auto-accept pass can act the moment a
            // share arrives — this is the only folder-less "Ready".
            return State(status: .synced, titleKey: "Ready")
        }
        // Accessible but no vault exists yet — waiting on the user to create
        // one in Obsidian. A calm waiting state, never a green check.
        return State(status: .starting, titleKey: "No Vaults Yet")
    }
}

import Foundation

/// The one place that attaches the app to the sync engine when it enters the
/// foreground path — scene activation, onboarding completion, and the
/// onboarding view's first appearance all route through here. Every caller
/// used to carry its own copy of the decision, and each copy was a separate
/// chance to guard on the manager alone and run `start()` into the Go
/// floor's "already running" rejection (#60 at scene activation, #61 at
/// onboarding completion and again at onboarding appear).
enum EngineAttach {
    /// Decide via `sceneActivationAction` and perform the cold-start or
    /// adoption, including the dead-engine fallback and the follow-up path
    /// reconcile (decision 008). Returns the decision so callers can handle
    /// `.alreadyAttached` themselves — that tail is the only part that
    /// legitimately differs per call site (rescan debounce at scene
    /// activation, #56 repeat reconcile at onboarding completion, nothing at
    /// onboarding appear).
    @MainActor
    @discardableResult
    static func onForeground(
        syncthingManager: SyncthingManager,
        vaultManager: VaultManager
    ) -> BackgroundSyncService.SceneActivationAction {
        let action = BackgroundSyncService.sceneActivationAction(
            bridgeRunning: SyncBridgeService.isRunning(),
            managerRunning: syncthingManager.isRunning
        )
        switch action {
        case .coldStart:
            // Syncthing may have been stopped by a BGTask expiration handler.
            // Reset Swift-side state so start() works.
            if syncthingManager.isRunning {
                syncthingManager.resetForRestart()
            }
            vaultManager.restoreAccess()
            Task {
                await syncthingManager.start()
                syncthingManager.reconcileFolderPaths(obsidianRoot: vaultManager.obsidianBasePath)
            }
        case .adoptRunningEngine:
            // A background handler started the engine in a background-launched
            // process and the manager never attached: without adoption neither
            // polling nor a path reconcile runs, and the background cleanup
            // would stop the engine under the active scene (#60). The
            // foreground needs its own security-scoped access — the background
            // handler releases its own when it finishes.
            vaultManager.restoreAccess()
            if syncthingManager.adoptRunningEngine() {
                syncthingManager.reconcileFolderPaths(obsidianRoot: vaultManager.obsidianBasePath)
            } else {
                // The engine stopped between the check and the claim —
                // cold-start instead.
                Task {
                    await syncthingManager.start()
                    syncthingManager.reconcileFolderPaths(obsidianRoot: vaultManager.obsidianBasePath)
                }
            }
        case .alreadyAttached:
            break
        }
        return action
    }
}

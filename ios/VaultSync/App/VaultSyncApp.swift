import SwiftUI
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "app")

@main
struct VaultSyncApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    @AppStorage("hasCompletedOnboarding") private var hasCompletedOnboarding = false
    @State private var syncthingManager = SyncthingManager()
    @State private var vaultManager = VaultManager()
    @State private var subscriptionManager = SubscriptionManager()
    @State private var lastBackgroundedAt: Date?
    @Environment(\.scenePhase) private var scenePhase

    private static let foregroundRescanThreshold: TimeInterval = 5

    init() {
        // Conflict banners default ON. Registered defaults are per-process and
        // not persisted, so the background handler still relies on its own
        // `?? true` fallback — this only keeps foreground `bool(forKey:)` reads
        // consistent before the user ever touches the toggle.
        UserDefaults.standard.register(
            defaults: [BackgroundSyncService.conflictNotificationsEnabledKey: true]
        )
        BackgroundSyncService.registerTasks()
        logger.info("VaultSync starting")
        Task.detached(priority: .utility) {
            logger.info("Go bridge ping: \(SyncBridgeService.ping())")
            logger.info("Go version: \(SyncBridgeService.goVersion())")
            logger.info("Syncthing version: \(SyncBridgeService.syncthingVersion())")
        }
    }

    var body: some Scene {
        WindowGroup {
            Group {
                if hasCompletedOnboarding {
                    ContentView(
                        syncthingManager: syncthingManager,
                        vaultManager: vaultManager,
                        subscriptionManager: subscriptionManager
                    )
                } else {
                    OnboardingView(
                        hasCompletedOnboarding: $hasCompletedOnboarding,
                        syncthingManager: syncthingManager,
                        vaultManager: vaultManager,
                        subscriptionManager: subscriptionManager
                    )
                }
            }
            .onOpenURL { url in
                handleIncomingURL(url)
            }
        }
        .onChange(of: scenePhase) { _, newPhase in
            switch newPhase {
            case .active:
                BackgroundSyncService.setSceneActive(true)
                BackgroundSyncService.endBackgroundAssertion()
                BackgroundSyncService.cancelContinuedProcessing()
                if !SyncBridgeService.isRunning() {
                    // Syncthing may have been stopped by a BGTask expiration handler.
                    // Reset Swift-side state so start() works.
                    if syncthingManager.isRunning {
                        syncthingManager.resetForRestart()
                    }
                    vaultManager.restoreAccess()
                    syncthingManager.start()
                } else if BackgroundSyncService.shouldRescanOnForeground(
                    now: Date(),
                    lastBackgroundedAt: lastBackgroundedAt,
                    threshold: Self.foregroundRescanThreshold
                ) {
                    // Bridge is still alive but the app spent enough time in
                    // the background that the user likely edited the vault
                    // from another app (e.g. Obsidian). The iOS FSWatcher
                    // doesn't reliably see cross-sandbox writes, so trigger
                    // a fresh scan to pick them up immediately.
                    syncthingManager.triggerForegroundSync()
                }
                lastBackgroundedAt = nil
            case .background:
                lastBackgroundedAt = Date()
                BackgroundSyncService.setSceneActive(false)

                // Release the foreground lifecycle lock so silent-push and
                // BGAppRefresh handlers can manage Syncthing when the process
                // is later resumed — without this, backgroundManaged stays
                // false and no reconnect happens.
                BackgroundSyncService.releaseForegroundLifecycleLock()

                // Request up to ~30s of continued execution so pending scans,
                // index updates, and clean socket shutdowns can complete.
                // Without this assertion iOS may suspend within ~5s.
                BackgroundSyncService.beginBackgroundAssertion()

                BackgroundSyncService.scheduleAppRefresh()
                if syncthingManager.isAnySyncing {
                    if #available(iOS 26.0, *) {
                        BackgroundSyncService.submitContinuedProcessing()
                    }
                }
            default:
                break
            }
        }
        .onChange(of: hasCompletedOnboarding) { _, completed in
            if completed {
                if !syncthingManager.isRunning {
                    vaultManager.restoreAccess()
                    syncthingManager.start()
                }
                Task {
                    await BackgroundSyncService.requestNotificationPermission()
                }
            }
        }
    }

    private func handleIncomingURL(_ url: URL) {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.scheme?.caseInsensitiveCompare("vaultsync") == .orderedSame,
              components.host?.caseInsensitiveCompare("sync") == .orderedSame else {
            logger.debug("Ignoring unsupported incoming URL: \(url.absoluteString, privacy: .public)")
            return
        }

        let folderID = components.queryItems?
            .first(where: { $0.name == "folder" })?
            .value?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let normalizedFolderID = folderID.flatMap { $0.isEmpty ? nil : $0 }

        logger.info("Handling sync URL request (folder=\(normalizedFolderID ?? "all", privacy: .public))")
        syncthingManager.triggerForegroundSync(folderID: normalizedFolderID)
    }
}

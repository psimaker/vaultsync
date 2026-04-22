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
    @Environment(\.scenePhase) private var scenePhase

    init() {
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
                }
            case .background:
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

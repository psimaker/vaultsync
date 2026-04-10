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
        .onChange(of: scenePhase) { _, newPhase in
            switch newPhase {
            case .active:
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
                BackgroundSyncService.scheduleAppRefresh()
                if syncthingManager.isAnySyncing {
                    BackgroundSyncService.submitContinuedProcessing()
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
}

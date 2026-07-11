import SwiftUI
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "app")

@main
struct VaultSyncApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    @AppStorage("hasCompletedOnboarding") private var hasCompletedOnboarding = false
    @State private var syncthingManager: SyncthingManager
    @State private var vaultManager: VaultManager
    @State private var subscriptionManager = SubscriptionManager()
    // ONE coordinator for both mount points (#92, decision 015): a failure or
    // parked merge recorded during onboarding must survive into the home
    // screen and keep blocking auto-retries there.
    @State private var shareAccept: ShareAcceptCoordinator
    @State private var lastBackgroundedAt: Date?
    @Environment(\.scenePhase) private var scenePhase

    private static let foregroundRescanThreshold: TimeInterval = 5

    init() {
        let syncthing = SyncthingManager()
        let vault = VaultManager()
        _syncthingManager = State(initialValue: syncthing)
        _vaultManager = State(initialValue: vault)
        _shareAccept = State(initialValue: ShareAcceptCoordinator(
            environment: .live(syncthingManager: syncthing, vaultManager: vault)
        ))
        // Conflict banners default ON. Registered defaults are per-process and
        // not persisted, so the background handler still relies on its own
        // `?? true` fallback — this only keeps foreground `bool(forKey:)` reads
        // consistent before the user ever touches the toggle.
        UserDefaults.standard.register(
            defaults: [BackgroundSyncService.conflictNotificationsEnabledKey: true]
        )
        BackgroundSyncService.registerTasks()
        logger.info("VaultSync starting")
        #if DEBUG
        // LAB: make the effective relay target visible/verifiable. Production
        // unless RELAY_BASE_URL_OVERRIDE is set (DEBUG only). Compiled out of release.
        logger.info("Relay base URL in use: \(RelayService.relayURL, privacy: .public)")
        #endif
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
                        subscriptionManager: subscriptionManager,
                        shareAccept: shareAccept
                    )
                } else {
                    OnboardingView(
                        hasCompletedOnboarding: $hasCompletedOnboarding,
                        syncthingManager: syncthingManager,
                        vaultManager: vaultManager,
                        subscriptionManager: subscriptionManager,
                        shareAccept: shareAccept
                    )
                }
            }
            .onOpenURL { url in
                handleIncomingURL(url)
            }
            .tint(.vaultAccent)
            .animation(.easeInOut(duration: 0.3), value: hasCompletedOnboarding)
        }
        .onChange(of: scenePhase) { _, newPhase in
            // The unit-test host must never manage the process-global engine
            // lifecycle — see TestHost.
            guard !TestHost.isActive else { return }
            #if DEBUG
            // A UI-audit fixture run seeds manager state directly; starting
            // the engine would overwrite it — see UIAuditFixture.
            guard !UIAuditFixture.isActive else { return }
            #endif
            switch newPhase {
            case .active:
                BackgroundSyncService.setSceneActive(true)
                BackgroundSyncService.endBackgroundAssertion()
                BackgroundSyncService.cancelContinuedProcessing()
                let action = EngineAttach.onForeground(
                    syncthingManager: syncthingManager,
                    vaultManager: vaultManager
                )
                if action == .alreadyAttached,
                   BackgroundSyncService.shouldRescanOnForeground(
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
                BackgroundSyncService.scheduleProcessing()
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
            guard !TestHost.isActive else { return }
            #if DEBUG
            guard !UIAuditFixture.isActive else { return }
            #endif
            if completed {
                // Second consumer of the #60 state: guarding on the manager
                // alone made this call start() against an engine a background
                // handler already runs, surfacing the Go floor's "already
                // running" as a user error in the first-run moment (#61).
                if EngineAttach.onForeground(
                    syncthingManager: syncthingManager,
                    vaultManager: vaultManager
                ) == .alreadyAttached {
                    // Onboarding may have started the engine itself (its own
                    // start can win against the scene-active start above) — in
                    // that case no reconcile ran this launch, and accept
                    // decisions stay held until one completes (#56). A repeat
                    // reconcile is a no-op when paths are already correct.
                    syncthingManager.reconcileFolderPaths(obsidianRoot: vaultManager.obsidianBasePath)
                }
                // Notification permission is deliberately NOT requested here:
                // firing the bare system prompt over the still-empty main
                // screen was un-primed and easy to reflex-deny (#69). The
                // dashboard's explainer card asks after the first completed
                // sync instead (ContentView.maybePresentNotificationPrimer).
            }
        }
    }

    private func handleIncomingURL(_ url: URL) {
        #if DEBUG
        // LAB: the iOS Simulator cannot deliver real silent pushes — `simctl push`
        // content-available is received as a scene action but never invokes
        // didReceiveRemoteNotification (confirmed empirically). So the mock relay
        // simulates ONLY the transport leg of a REAL delivery by opening
        // `vaultsync://relay-wake`; everything downstream (markReceived →
        // freshness → "active"/celebration) then runs for real. Compiled out of
        // release builds, so it can never affect shipping behaviour.
        if let comps = URLComponents(url: url, resolvingAgainstBaseURL: false),
           comps.scheme?.caseInsensitiveCompare("vaultsync") == .orderedSame,
           comps.host?.caseInsensitiveCompare("relay-wake") == .orderedSame {
            logger.info("DEBUG: simulated real relay delivery via deep link (lab)")
            RelayTriggerStore.markReceived()
            return
        }
        #endif

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

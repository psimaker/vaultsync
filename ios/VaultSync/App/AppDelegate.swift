import UIKit
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "appdelegate")

class AppDelegate: NSObject, UIApplicationDelegate {

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        application.registerForRemoteNotifications()
        logger.info("Registered for remote notifications")
        logger.debug("Custom URL routing is handled by VaultSyncApp.onOpenURL; AppDelegate remains dedicated to push and background delivery")
        // NOTE: We deliberately do NOT flag APNs/relay as failed based on alert
        // authorization. Silent (content-available) pushes — the relay's wake
        // mechanism — are delivered regardless of UNAuthorizationStatus. The
        // live alert-permission state is surfaced as informational in Relay
        // Diagnostics instead (see SubscriptionManager.alertBannerStatus).
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        let previousToken = KeychainService.getAPNsDeviceToken()
        _ = KeychainService.setAPNsDeviceToken(token)
        APNsRegistrationStore.markRegistered()
        logger.info("APNs device token received and stored")

        if previousToken != token {
            logger.info("APNs device token changed (first=\(previousToken == nil)), notifying for re-provisioning")
            APNsRegistrationStore.postTokenDidChange()
        }
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: any Error
    ) {
        let reason = apnsRegistrationFailureReason(error)
        logger.error("APNs registration failed: \(reason)")
        _ = KeychainService.clearAPNsDeviceToken()
        APNsRegistrationStore.markFailed(reason: reason)
    }

    func application(
        _ application: UIApplication,
        didReceiveRemoteNotification userInfo: [AnyHashable: Any],
        fetchCompletionHandler completionHandler: @escaping (UIBackgroundFetchResult) -> Void
    ) {
        logger.info("Silent push received")
        // Every silent push is a genuine relay delivery — the server helper
        // triggered it (a vault change, or the helper's startup-announce). The app
        // no longer sends any trigger of its own, so there is nothing to
        // disambiguate: record it as a real delivery, which drives
        // relayDeliveryConfirmed and clears the reactivation card.
        RelayTriggerStore.markReceived()

        Task {
            RelaySyncProofStore.markBackgroundSyncStarted()
            let result = await BackgroundSyncService.performBackgroundSync(
                reason: "silent-push"
            )
            logger.info("Silent push finished with result=\(result.rawValue, privacy: .public)")

            switch result {
            case .synced:
                // This records observed local sync progress only. It is not a
                // confirmed upload/download roundtrip and is never presented as one.
                RelaySyncProofStore.markSyncProgressObserved()
                completionHandler(.newData)
            case .alreadyIdle, .noFoldersConfigured, .settledWithFolderError:
                completionHandler(.noData)
            case .noBookmarkAccess, .bridgeStartFailed, .notIdleBeforeDeadline, .failed:
                completionHandler(.failed)
            }
        }
    }

    // MARK: - Private

    private func apnsRegistrationFailureReason(_ error: any Error) -> String {
        let nsError = error as NSError
        let base = "\(nsError.localizedDescription) (\(nsError.domain):\(nsError.code))"

        // Common in Simulator; APNs token retrieval is unavailable there.
        if nsError.domain == NSCocoaErrorDomain, nsError.code == 3010 {
            return L10n.tr("Push registration is unavailable in Simulator. Test APNs on a physical iPhone in Settings > Notifications for VaultSync.")
        }

        return base
    }
}

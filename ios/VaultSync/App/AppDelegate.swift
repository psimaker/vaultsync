import UIKit
import UserNotifications
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
        Task { await refreshNotificationAuthorizationState() }
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
        logger.info("APNs device token received and stored (\(token.prefix(8))...)")

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
        RelayTriggerStore.markReceived()

        Task {
            let result = await BackgroundSyncService.performBackgroundSync(
                reason: "silent-push"
            )
            logger.info("Silent push finished with result=\(result.rawValue, privacy: .public)")

            switch result {
            case .synced:
                completionHandler(.newData)
            case .alreadyIdle, .noFoldersConfigured:
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

    private func refreshNotificationAuthorizationState() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        guard settings.authorizationStatus == .denied else { return }
        APNsRegistrationStore.markFailed(
            reason: L10n.tr("Notifications are disabled for VaultSync. Enable them in iOS Settings > Notifications > VaultSync, then retry APNs registration.")
        )
    }
}

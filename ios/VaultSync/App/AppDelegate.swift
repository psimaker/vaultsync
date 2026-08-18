import UIKit
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "appdelegate")

/// Product-used APNs persistence decision with injectable effects. Log events
/// carry only fixed categories and a boolean, never the device token (#148).
@MainActor
enum APNsDeviceTokenRegistration {
    enum LogEvent: Equatable {
        case persistenceFailed
        case stored
        case changed(first: Bool)
    }

    struct Environment {
        var loadPreviousToken: () -> String?
        var persistToken: (String) -> Bool
        var markRegistered: () -> Void
        var markFailed: (String) -> Void
        var postTokenDidChange: () -> Void
        var log: (LogEvent) -> Void

        static var live: Self {
            Self(
                loadPreviousToken: KeychainService.getAPNsDeviceToken,
                persistToken: KeychainService.setAPNsDeviceToken,
                markRegistered: APNsRegistrationStore.markRegistered,
                markFailed: APNsRegistrationStore.markFailed,
                postTokenDidChange: APNsRegistrationStore.postTokenDidChange,
                log: { event in
                    switch event {
                    case .persistenceFailed:
                        logger.error("APNs device token could not be stored")
                    case .stored:
                        logger.info("APNs device token received and stored")
                    case .changed(let first):
                        logger.info("APNs device token changed (first=\(first, privacy: .public)), notifying for re-provisioning")
                    }
                }
            )
        }
    }

    @discardableResult
    static func handle(token: String, failureReason: String, environment: Environment) -> Bool {
        let previousToken = environment.loadPreviousToken()
        guard environment.persistToken(token) else {
            environment.log(.persistenceFailed)
            environment.markFailed(failureReason)
            return false
        }

        environment.markRegistered()
        environment.log(.stored)
        if previousToken != token {
            environment.log(.changed(first: previousToken == nil))
            environment.postTokenDidChange()
        }
        return true
    }
}

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
        APNsDeviceTokenRegistration.handle(
            token: token,
            failureReason: L10n.tr("Push registration is not ready yet."),
            environment: .live
        )
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: any Error
    ) {
        let reason = apnsRegistrationFailureReason(error)
        logger.error("APNs registration failed")
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
        // Common in Simulator; APNs token retrieval is unavailable there.
        if nsError.domain == NSCocoaErrorDomain, nsError.code == 3010 {
            return L10n.tr("Push registration is unavailable in Simulator. Test APNs on a physical iPhone in Settings > Notifications for VaultSync.")
        }

        return L10n.tr("Push registration is not ready yet.")
    }
}

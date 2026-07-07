import UserNotifications

/// Pure gate for the primed notification ask (#69).
///
/// Full alert authorization used to be requested the moment onboarding
/// completed — a bare system prompt over the still-empty main screen, before
/// the user had any reason to want notifications. It is now requested from an
/// explanatory dashboard card after the first completed sync (the first
/// moment a conflict alert can matter), and only a deliberate tap on the card
/// triggers the system prompt.
enum NotificationPrimerGate {
    /// Cheap synchronous pre-checks — is it even worth querying the
    /// notification settings? One ask at a time: while another dashboard ask
    /// (the relay upsell) is visible, the primer waits and re-checks when
    /// that card is dismissed.
    static func shouldCheck(
        alreadyHandled: Bool,
        hasSyncFolders: Bool,
        hasCompletedFirstSync: Bool,
        otherCardVisible: Bool
    ) -> Bool {
        !alreadyHandled && hasSyncFolders && hasCompletedFirstSync && !otherCardVisible
    }

    /// Present only while iOS would actually show the permission dialog. Any
    /// prior decision (granted, denied, provisional — e.g. from the pre-1.8.0
    /// onboarding prompt) retires the primer for good.
    static func shouldPresent(authorizationStatus: UNAuthorizationStatus) -> Bool {
        authorizationStatus == .notDetermined
    }
}

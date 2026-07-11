import Foundation

/// Pure gate for the one-time Cloud Relay offer (#94).
///
/// `hasCompletedFirstSync` became honest with the #94 detector fix, but a
/// pre-#94 install may carry a persisted last-sync date recorded from a bare
/// empty scan while the peer was offline. `hasAnySyncedContent` (any folder
/// whose global index is non-empty) keeps the pitch from firing on such a
/// device while not a single file has actually arrived — the upsell is built
/// around the first real "aha" moment, and burning it on a stall also masks
/// the stall.
enum RelayUpsellGate {
    static func shouldPresent(
        isSubscribed: Bool,
        hasSyncFolders: Bool,
        hasCompletedFirstSync: Bool,
        hasAnySyncedContent: Bool,
        alreadyShown: Bool
    ) -> Bool {
        !isSubscribed
            && hasSyncFolders
            && hasCompletedFirstSync
            && hasAnySyncedContent
            && !alreadyShown
    }
}

import Foundation

#if DEBUG
/// LAB: launch-argument fixtures for verifying safety-relevant UI empirically
/// on a simulator — consent dialogs and error rows that otherwise need a
/// paired peer or damaged on-disk state (#64/#65 audit evidence). Pass
/// `-uiaudit-fixture <name>` plus `-hasCompletedOnboarding YES` at launch to
/// seed the state. While a fixture is active the app skips all engine
/// management: the seeded folder/status state must not be overwritten by live
/// bridge polling (same reasoning as TestHost). Compiled out of release
/// builds, so it can never affect shipping behaviour.
enum UIAuditFixture {
    static let mergeConsent = "merge-consent"
    static let removalConsent = "removal-consent"
    static let markerError = "marker-error"
    static let deviceRemovalConsent = "device-removal-consent"
    static let conflictResolveConsent = "conflict-resolve-consent"

    /// The fixture named by `-uiaudit-fixture <name>`, read via the argument
    /// domain UserDefaults overlay; nil in any normal run.
    static var active: String? {
        UserDefaults.standard.string(forKey: "uiaudit-fixture")
    }

    static var isActive: Bool { active != nil }
}
#endif

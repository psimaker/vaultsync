import Foundation
import Observation
#if canImport(UIKit)
import UIKit
#endif

/// A pairing link opened from outside the app — a Hub's QR code scanned with
/// the Camera app (#174). The app owns it above the onboarding/home switch;
/// whichever screen is mounted takes it once it can present the Add Hub sheet,
/// never on top of another sheet or a consent dialog. A link only prefills
/// the sheet: pairing always takes the user's tap.
@MainActor
@Observable
final class HubLinkRouter {
    enum Request: Equatable {
        case pair(HubPairingLink)
        case unusable(HubPairingLink.ParseError)
    }

    private(set) var pending: Request?
    private let rules: HubPairingLink.Rules

    init(rules: HubPairingLink.Rules = .live) {
        self.rules = rules
    }

    /// Queues an opened `vaultsync://pair` URL. A newer link replaces one
    /// that is still waiting.
    func open(_ url: URL) {
        switch HubPairingLink.parse(url.absoluteString, rules: rules) {
        case .success(let link):
            pending = .pair(link)
        case .failure(let problem):
            pending = .unusable(problem)
        }
    }

    /// Hands the waiting request to the screen that presents it.
    func take() -> Request? {
        defer { pending = nil }
        return pending
    }
}

/// One presentation of the Add Hub sheet.
struct AddHubRequest: Identifiable {
    let id = UUID()
    var link: HubPairingLink?
    #if DEBUG
    /// DEBUG only: the design-preview fixture's seeded flow.
    var previewModel: HubPairingModel?
    #endif
}

/// When an opened pairing link may present the Add Hub sheet: only once the
/// screen has been free of every sheet and dialog — its own and those of
/// child screens such as a removal or resolve confirmation — for a short
/// grace period, so a dismissal's own follow-on presentation (a hint, a
/// checklist step) goes first and the link never replaces a consent dialog.
struct HubLinkGate {
    static let grace: TimeInterval = 0.8
    private var freeSince: Date?

    /// Call repeatedly while a link waits; true once it may present.
    mutating func shouldPresent(blocked: Bool, now: Date) -> Bool {
        guard !blocked else {
            freeSince = nil
            return false
        }
        guard let since = freeSince else {
            freeSince = now
            return false
        }
        return now.timeIntervalSince(since) >= Self.grace
    }

    /// Whether anything is presented over the app's key window — UIKit sees
    /// the presentations of child views that SwiftUI state up here does not.
    @MainActor
    static func somethingIsPresented() -> Bool {
        #if canImport(UIKit)
        UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
            .contains { $0.isKeyWindow && $0.rootViewController?.presentedViewController != nil }
        #else
        false
        #endif
    }
}

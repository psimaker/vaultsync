import Foundation
import Observation

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

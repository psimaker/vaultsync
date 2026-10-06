import Foundation

/// The pairing request a Hub prints as a QR code next to its code (#174,
/// decision 045): `vaultsync://pair?code=TULIP-ANCHOR-42&hub=192.168.1.20%3A8390`.
/// `hub` is optional — without it the app searches the local network. Scanned
/// in VaultSync or opened from the Camera app, a link only PREFILLS the Add Hub
/// sheet: it never pairs on its own.
struct HubPairingLink: Equatable, Sendable {
    /// The canonical code.
    let code: String
    /// ip:port of the Hub's pairing service, checked to be on the local
    /// network; nil means "search the network".
    let hubAddress: String?

    enum ParseError: Error, Equatable, Sendable {
        /// Some other QR code or URL.
        case notAPairingLink
        /// A Syncthing Device ID — that QR belongs to Add Device.
        case deviceID
        case invalidCode
        /// The Hub address is not a private, loopback or link-local IP: a
        /// link must never send the handshake off the local network.
        case hubNotLocal
        case hubMalformed
    }

    /// The validation rules, injected so the parser is testable. `live` asks
    /// the Go bridge — the one implementation of the code word list and of the
    /// local-network rule (`pairing.NormalizeCode`, `pairing.LocalHubAddress`),
    /// so the app can never disagree with the CLI or the bridge's own floor.
    struct Rules: Sendable {
        var normalizeCode: @Sendable (String) -> String?
        var checkAddress: @Sendable (String) -> Result<String, HubPairingFailure>

        static let live = Rules(
            normalizeCode: { SyncBridgeService.hubPairingNormalizeCode($0) },
            checkAddress: { SyncBridgeService.hubPairingCheckAddress($0) }
        )
    }

    static let scheme = "vaultsync"
    static let host = "pair"

    /// Whether an opened URL is meant for pairing (the scheme and host match);
    /// `parse` then decides whether it is a usable one.
    static func isPairingURL(_ url: URL) -> Bool {
        url.scheme?.caseInsensitiveCompare(scheme) == .orderedSame
            && url.host()?.caseInsensitiveCompare(host) == .orderedSame
    }

    /// Parses a scanned or opened pairing request. Strict on purpose — the
    /// input is untrusted: exactly one `code`, at most one `hub`, nothing in
    /// the path, no user, port or fragment. A bare code is accepted too.
    static func parse(_ raw: String, rules: Rules = .live) -> Result<HubPairingLink, ParseError> {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return .failure(.notAPairingLink) }
        if SyncthingDeviceID.canonicalize(text) != nil {
            return .failure(.deviceID)
        }
        guard let components = URLComponents(string: text),
              let linkScheme = components.scheme,
              linkScheme.caseInsensitiveCompare(scheme) == .orderedSame else {
            // Not a vaultsync:// URL: perhaps the code alone.
            if let code = rules.normalizeCode(text) {
                return .success(HubPairingLink(code: code, hubAddress: nil))
            }
            return .failure(.notAPairingLink)
        }
        guard components.host?.caseInsensitiveCompare(host) == .orderedSame,
              components.path.isEmpty || components.path == "/",
              components.user == nil, components.password == nil,
              components.port == nil, components.fragment == nil else {
            return .failure(.notAPairingLink)
        }
        let items = components.queryItems ?? []
        let codes = items.filter { $0.name == "code" }
        let hubs = items.filter { $0.name == "hub" }
        guard codes.count == 1, hubs.count <= 1 else {
            return .failure(.notAPairingLink)
        }
        guard let rawCode = codes[0].value, let code = rules.normalizeCode(rawCode) else {
            return .failure(.invalidCode)
        }
        guard let hub = hubs.first else {
            return .success(HubPairingLink(code: code, hubAddress: nil))
        }
        guard let rawHub = hub.value, !rawHub.isEmpty else {
            return .failure(.hubMalformed)
        }
        switch rules.checkAddress(rawHub) {
        case .success(let address):
            return .success(HubPairingLink(code: code, hubAddress: address))
        case .failure(let failure):
            return .failure(failure.kind == .notLocal ? .hubNotLocal : .hubMalformed)
        }
    }
}

/// The three fields of the Add Hub code entry (canvas: WORD – WORD – NN).
/// What the user typed stays as typed — a one-digit number is padded by the
/// bridge's normalizer on submit, not while typing.
struct HubCodeFields: Equatable, Sendable {
    var first = ""
    var second = ""
    var number = ""

    init(first: String = "", second: String = "", number: String = "") {
        self.first = first
        self.second = second
        self.number = number
    }

    /// The fields of a canonical code (TULIP-ANCHOR-42).
    init(canonical code: String) {
        let parts = code.split(separator: "-", omittingEmptySubsequences: false).map(String.init)
        self.init(
            first: parts.indices.contains(0) ? parts[0] : "",
            second: parts.indices.contains(1) ? parts[1] : "",
            number: parts.indices.contains(2) ? parts[2] : ""
        )
    }

    var isComplete: Bool { !first.isEmpty && !second.isEmpty && !number.isEmpty }

    /// The input for the normalizer.
    var joined: String { "\(first)-\(second)-\(number)" }

    /// The characters the CLI's normalizer splits on — nothing more, so the
    /// app never accepts input the Hub's own tools would refuse.
    static let separators: Set<Character> = ["-", " ", "_", ".", ","]

    /// A word field's new text: letters only, upper case. `advance` is true
    /// when the user typed a separator — the next field takes over.
    static func wordInput(_ raw: String) -> (word: String, advance: Bool) {
        var word = ""
        for character in raw {
            if separators.contains(character) {
                return (word, true)
            }
            if character.isASCII, character.isLetter {
                word.append(Character(character.uppercased()))
            }
        }
        return (word, false)
    }

    /// The number field's new text: up to two ASCII digits.
    static func numberInput(_ raw: String) -> String {
        String(raw.filter { $0.isASCII && $0.isNumber }.prefix(2))
    }
}

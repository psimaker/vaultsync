import Foundation
import Testing
@testable import VaultSync

/// The code and the QR link run through the Go bridge's own rules — the word
/// list and the local-network check of `hub/pairing` — so these tests use the
/// live bridge: the app can never normalize a code differently from the CLI
/// or accept an address the bridge's floor would refuse (decision 045).
@Suite("Hub pairing code and link (#174)")
struct HubPairingLinkTests {
    let rules = HubPairingLink.Rules.live

    // MARK: - Code normalization (same cases as hub/pairing/code_test.go)

    @Test("Human input normalizes exactly like the CLI")
    func normalizesLikeTheCLI() {
        let cases = [
            "TULIP-ANCHOR-07": "TULIP-ANCHOR-07",
            "tulip anchor 7": "TULIP-ANCHOR-07",
            "  Tulip_Anchor.42  ": "TULIP-ANCHOR-42",
            "tulip--anchor--00": "TULIP-ANCHOR-00",
            "otter,piano,9": "OTTER-PIANO-09",
        ]
        for (input, expected) in cases {
            #expect(rules.normalizeCode(input) == expected, "\(input)")
        }
    }

    @Test("Anything that cannot be a code is refused")
    func refusesGarbage() {
        for input in ["", "tulip", "tulip-anchor", "tulip-anchor-123", "tulip-anchor-x1",
                      "zzzz-anchor-01", "tulip-anchor-01-extra", "TULIP ANCHOR",
                      "TULIP–ANCHOR–42"] { // en dashes: not a separator for the CLI either
            #expect(rules.normalizeCode(input) == nil, "\(input)")
        }
    }

    @Test("Code fields keep what was typed; letters only, a separator moves on")
    func codeFields() {
        #expect(HubCodeFields.wordInput("tulip") == ("TULIP", false))
        #expect(HubCodeFields.wordInput("tul1p!") == ("TULP", false))
        #expect(HubCodeFields.wordInput("TULIP ") == ("TULIP", true))
        #expect(HubCodeFields.wordInput("TULIP-ANCHOR") == ("TULIP", true))
        #expect(HubCodeFields.wordInput("tülip") == ("TLIP", false))
        #expect(HubCodeFields.numberInput("4") == "4") // padded by the normalizer, not while typing
        #expect(HubCodeFields.numberInput("42") == "42")
        #expect(HubCodeFields.numberInput("4a2x9") == "42")
        #expect(HubCodeFields.numberInput("٤٢") == "") // ASCII digits only

        let fields = HubCodeFields(first: "tulip", second: "anchor", number: "7")
        #expect(fields.isComplete)
        #expect(rules.normalizeCode(fields.joined) == "TULIP-ANCHOR-07")
        #expect(HubCodeFields(canonical: "TULIP-ANCHOR-42") == HubCodeFields(first: "TULIP", second: "ANCHOR", number: "42"))
        #expect(!HubCodeFields(first: "TULIP", second: "", number: "42").isComplete)
    }

    // MARK: - Links

    /// The exact strings `pairing.Link` produces (hub/pairing/link_test.go):
    /// whatever the Hub prints, the app reads.
    @Test("Links built by the Hub parse back")
    func goGeneratedLinksParse() {
        let golden: [(String, String, String?)] = [
            ("vaultsync://pair?code=TULIP-ANCHOR-42", "TULIP-ANCHOR-42", nil),
            ("vaultsync://pair?code=TULIP-ANCHOR-42&hub=192.168.1.20%3A8390", "TULIP-ANCHOR-42", "192.168.1.20:8390"),
            ("vaultsync://pair?code=OTTER-PIANO-07&hub=%5Bfd00%3A%3A5%5D%3A8390", "OTTER-PIANO-07", "[fd00::5]:8390"),
        ]
        for (link, code, hub) in golden {
            #expect(HubPairingLink.parse(link, rules: rules) == .success(HubPairingLink(code: code, hubAddress: hub)), "\(link)")
        }
    }

    @Test("Scheme and host are case-insensitive; the code is normalized; a bare code works")
    func lenientWhereHarmless() {
        #expect(HubPairingLink.parse("VAULTSYNC://PAIR?code=tulip%20anchor%207", rules: rules)
            == .success(HubPairingLink(code: "TULIP-ANCHOR-07", hubAddress: nil)))
        #expect(HubPairingLink.parse("vaultsync://pair/?code=TULIP-ANCHOR-42", rules: rules)
            == .success(HubPairingLink(code: "TULIP-ANCHOR-42", hubAddress: nil)))
        #expect(HubPairingLink.parse("  TULIP-ANCHOR-42\n", rules: rules)
            == .success(HubPairingLink(code: "TULIP-ANCHOR-42", hubAddress: nil)))
        #expect(HubPairingLink.parse("vaultsync://pair?code=TULIP-ANCHOR-42&hub=10.0.0.7", rules: rules)
            == .success(HubPairingLink(code: "TULIP-ANCHOR-42", hubAddress: "10.0.0.7:8390")))
    }

    @Test("Untrusted links are parsed strictly")
    func strictWhereItMatters() {
        let notALink = [
            "vaultsync://pair?code=TULIP-ANCHOR-42&code=OTTER-PIANO-07",
            "vaultsync://pair?code=TULIP-ANCHOR-42&hub=10.0.0.7&hub=10.0.0.8",
            "vaultsync://pair/extra?code=TULIP-ANCHOR-42",
            "vaultsync://pair?code=TULIP-ANCHOR-42#fragment",
            "vaultsync://pair:8390?code=TULIP-ANCHOR-42",
            "vaultsync://user@pair?code=TULIP-ANCHOR-42",
            "vaultsync://sync?code=TULIP-ANCHOR-42",
            "https://vaultsync.eu/pair?code=TULIP-ANCHOR-42",
            "WIFI:T:WPA;S:HomeNet;P:secret;;",
            "",
        ]
        for link in notALink {
            #expect(HubPairingLink.parse(link, rules: rules) == .failure(.notAPairingLink), "\(link)")
        }
        #expect(HubPairingLink.parse("vaultsync://pair", rules: rules) == .failure(.notAPairingLink))
        #expect(HubPairingLink.parse("vaultsync://pair?code=", rules: rules) == .failure(.invalidCode))
        // A broken percent escape leaves no readable code: refused.
        #expect(HubPairingLink.parse("vaultsync://pair?code=TULIP%ZZ-ANCHOR-42", rules: rules) == .failure(.invalidCode))
        // Foundation hands an undecodable value through as typed; the bridge's
        // floor refuses it like any other non-address.
        if case .success = HubPairingLink.parse("vaultsync://pair?code=TULIP-ANCHOR-42&hub=10.0.0.%ZZ", rules: rules) {
            Issue.record("an undecodable hub address passed")
        }
        #expect(HubPairingLink.parse("vaultsync://pair?code=TULIP-NOTAWORD-42", rules: rules) == .failure(.invalidCode))
        #expect(HubPairingLink.parse("vaultsync://pair?code=TULIP-ANCHOR-42&hub=", rules: rules) == .failure(.hubMalformed))
        #expect(HubPairingLink.parse("P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2", rules: rules) == .failure(.deviceID))
    }

    /// Mirror of hub/pairing/link_test.go TestIssue174_LocalHubAddress: the
    /// address in a link passes exactly when the bridge's floor would let a
    /// handshake reach it.
    @Test("Only local-network Hub addresses pass")
    func hubAddressMirrorTable() {
        let accepted = [
            "192.168.1.20:8390": "192.168.1.20:8390",
            "192.168.1.20": "192.168.1.20:8390",
            "10.0.0.7:9000": "10.0.0.7:9000",
            "172.16.4.2:8390": "172.16.4.2:8390",
            "172.31.255.1:8390": "172.31.255.1:8390",
            "169.254.3.4:8390": "169.254.3.4:8390",
            "127.0.0.1:8390": "127.0.0.1:8390",
            "[fd00::5]:8390": "[fd00::5]:8390",
            "[fe80::1]": "[fe80::1]:8390",
            "[::1]:8390": "[::1]:8390",
            "192.168.1.20:65535": "192.168.1.20:65535",
            "192.168.1.20:1": "192.168.1.20:1",
            " 10.0.0.7:9000 ": "10.0.0.7:9000",
            "fd12:3456::1": "[fd12:3456::1]:8390",
            "::ffff:192.168.1.9": "192.168.1.9:8390",
        ]
        for (hub, expected) in accepted {
            let link = "vaultsync://pair?code=TULIP-ANCHOR-42&hub=" + Self.encoded(hub)
            #expect(HubPairingLink.parse(link, rules: rules) == .success(HubPairingLink(code: "TULIP-ANCHOR-42", hubAddress: expected)), "\(hub)")
        }
        let notLocal = ["8.8.8.8:8390", "203.0.113.9", "172.32.0.1:8390", "100.64.0.1:8390",
                        "0.0.0.0:8390", "255.255.255.255:8390", "224.0.0.1:8390",
                        "[2001:db8::1]:8390", "nas.local:8390", "hub", "example.com"]
        for hub in notLocal {
            let link = "vaultsync://pair?code=TULIP-ANCHOR-42&hub=" + Self.encoded(hub)
            #expect(HubPairingLink.parse(link, rules: rules) == .failure(.hubNotLocal), "\(hub)")
        }
        let malformed = ["192.168.1.20:", ":8390", "192.168.1.20:0", "192.168.1.20:65536",
                         "192.168.1.20:08390", "192.168.1.20:http", "http://192.168.1.20:8390",
                         "user@192.168.1.20", "192.168.1.20/24", "[fd00::5]:", "[]:8390"]
        for hub in malformed {
            let link = "vaultsync://pair?code=TULIP-ANCHOR-42&hub=" + Self.encoded(hub)
            #expect(HubPairingLink.parse(link, rules: rules) == .failure(.hubMalformed), "\(hub)")
        }
    }

    @Test("Only vaultsync://pair URLs are routed to pairing")
    func pairingURLRouting() {
        #expect(HubPairingLink.isPairingURL(URL(string: "vaultsync://pair?code=X")!))
        #expect(HubPairingLink.isPairingURL(URL(string: "VaultSync://PAIR")!))
        #expect(!HubPairingLink.isPairingURL(URL(string: "vaultsync://sync?folder=x")!))
        #expect(!HubPairingLink.isPairingURL(URL(string: "vaultsync://relay-wake")!))
        #expect(!HubPairingLink.isPairingURL(URL(string: "https://pair.example.com")!))
    }

    /// Form-encodes a query value like Go's url.Values.Encode does.
    private static func encoded(_ value: String) -> String {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
    }
}

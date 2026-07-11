import Foundation

/// Offline validator/normalizer for Syncthing device IDs (#93).
///
/// Mirrors the engine's parser (`lib/protocol/deviceid.go`, `UnmarshalText`):
/// strip `=` padding, uppercase, undo the common base32 misreadings the
/// engine tolerates (0→O, 1→I, 8→B), drop dashes and spaces, then accept
/// either the 56-character new style (4 × 13 data characters, each group
/// followed by one Luhn-mod-32 check character) or the 52-character legacy
/// style without check digits. Pure and side-effect free so QR payloads can
/// be rejected before they ever reach the bridge; the bridge re-validates
/// regardless (defense in depth).
enum SyncthingDeviceID {
    private static let alphabet: [Character] = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")

    /// Returns the canonical dash-chunked form (8 groups of 7 characters,
    /// check digits included), or nil when the input is not a Syncthing
    /// device ID. The empty string is nil here (not pairable), unlike the
    /// engine, which parses it as the empty device ID.
    static func canonicalize(_ raw: String) -> String? {
        var id = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        id = id.trimmingCharacters(in: CharacterSet(charactersIn: "="))
        id = id.uppercased()
        // untypeoify: characters outside the base32 alphabet that are common
        // misreadings of ones inside it.
        id = id.replacingOccurrences(of: "0", with: "O")
        id = id.replacingOccurrences(of: "1", with: "I")
        id = id.replacingOccurrences(of: "8", with: "B")
        // unchunkify
        id = id.replacingOccurrences(of: "-", with: "")
        id = id.replacingOccurrences(of: " ", with: "")

        let chars = Array(id)
        switch chars.count {
        case 56:
            // New style: verify each group's check character.
            for group in 0..<4 {
                let start = group * 14
                let payload = Array(chars[start..<(start + 13)])
                guard let check = luhn32(payload), chars[start + 13] == check else {
                    return nil
                }
            }
            return chunkify(chars)
        case 52:
            // Legacy style: no check digits; validate the alphabet (luhn32
            // rejects any character outside it) and append the check digits.
            var full: [Character] = []
            full.reserveCapacity(56)
            for group in 0..<4 {
                let payload = Array(chars[(group * 13)..<((group + 1) * 13)])
                guard let check = luhn32(payload) else { return nil }
                full.append(contentsOf: payload)
                full.append(check)
            }
            return chunkify(full)
        default:
            return nil
        }
    }

    // MARK: - Internals (mirror lib/protocol/luhn.go / deviceid.go)

    private static func codepoint(_ c: Character) -> Int? {
        guard let ascii = c.asciiValue else { return nil }
        switch ascii {
        case UInt8(ascii: "A")...UInt8(ascii: "Z"):
            return Int(ascii - UInt8(ascii: "A"))
        case UInt8(ascii: "2")...UInt8(ascii: "7"):
            return Int(ascii - UInt8(ascii: "2")) + 26
        default:
            return nil
        }
    }

    /// Syncthing's "Luhn mod 32" check character. Deliberately not the
    /// textbook Luhn algorithm — it must match the engine, not the spec
    /// (see lib/protocol/luhn.go and the linked forum thread there).
    private static func luhn32(_ payload: [Character]) -> Character? {
        var factor = 1
        var sum = 0
        for c in payload {
            guard let cp = codepoint(c) else { return nil }
            var addend = factor * cp
            factor = factor == 2 ? 1 : 2
            addend = (addend / 32) + (addend % 32)
            sum += addend
        }
        let checkCodepoint = (32 - (sum % 32)) % 32
        return alphabet[checkCodepoint]
    }

    private static func chunkify(_ chars: [Character]) -> String {
        var out: [Character] = []
        out.reserveCapacity(chars.count + chars.count / 7)
        for (i, c) in chars.enumerated() {
            if i > 0 && i % 7 == 0 { out.append("-") }
            out.append(c)
        }
        return String(out)
    }
}

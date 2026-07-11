import Testing
@testable import VaultSync

@Suite("Syncthing device ID pre-validation (#93)")
struct SyncthingDeviceIDTests {
    /// Well-known Syncthing test vector (lib/protocol/deviceid_test.go).
    static let canonical = "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2"
    /// The same ID in the legacy 52-character form without check digits.
    static let legacy = "P56IOI7MZJNU2IQGDREYDM2MGTMGL3BXNPQ6W5BTBBZ4TJXZWICQ"

    @Test("Canonical new-style ID passes and round-trips unchanged")
    func acceptsCanonical() {
        #expect(SyncthingDeviceID.canonicalize(Self.canonical) == Self.canonical)
    }

    @Test("Lowercase, spaces, and missing dashes normalize to the canonical form")
    func normalizesMessyInput() {
        #expect(SyncthingDeviceID.canonicalize(Self.canonical.lowercased()) == Self.canonical)
        #expect(SyncthingDeviceID.canonicalize(Self.canonical.replacingOccurrences(of: "-", with: " ")) == Self.canonical)
        #expect(SyncthingDeviceID.canonicalize(Self.canonical.replacingOccurrences(of: "-", with: "")) == Self.canonical)
        #expect(SyncthingDeviceID.canonicalize("  \(Self.canonical)\n") == Self.canonical)
    }

    @Test("Common base32 misreadings 0/1/8 are corrected like the engine does")
    func fixesTypos() {
        let typoed = Self.canonical
            .replacingOccurrences(of: "O", with: "0")
            .replacingOccurrences(of: "I", with: "1")
            .replacingOccurrences(of: "B", with: "8")
        #expect(SyncthingDeviceID.canonicalize(typoed) == Self.canonical)
    }

    @Test("Legacy 52-char ID without check digits is accepted and upgraded to canonical")
    func acceptsLegacyID() {
        #expect(SyncthingDeviceID.canonicalize(Self.legacy) == Self.canonical)
        // Dash-chunked legacy variant from syncthing's own validate cases.
        #expect(SyncthingDeviceID.canonicalize("P56IOI7-MZJNU2-IQGDREY-DM2MGT-MGL3BXN-PQ6W5B-TBBZ4TJ-XZWICQ") == Self.canonical)
    }

    @Test("A tampered check digit is rejected")
    func rejectsWrongCheckDigit() {
        let tampered = String(Self.canonical.dropLast()) + "3" // canonical ends in "2"
        #expect(SyncthingDeviceID.canonicalize(tampered) == nil)
    }

    @Test("Arbitrary QR payloads are rejected")
    func rejectsNonDevicePayloads() {
        #expect(SyncthingDeviceID.canonicalize("https://example.com/some/link") == nil)
        #expect(SyncthingDeviceID.canonicalize("WIFI:T:WPA;S:HomeNet;P:secret;;") == nil)
        #expect(SyncthingDeviceID.canonicalize("hello world") == nil)
        #expect(SyncthingDeviceID.canonicalize("") == nil)
        #expect(SyncthingDeviceID.canonicalize("P56IOI7") == nil)
        // Right length, garbage content (syncthing validate case, 56 chars, bad checks).
        #expect(SyncthingDeviceID.canonicalize("P56IOI7MZJNU2IQGDREYDM2MGTMGL3BXNPQ6W5BTBBZ4TJXZWICQCCCC") == nil)
    }
}

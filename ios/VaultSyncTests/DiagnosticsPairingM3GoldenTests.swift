import CryptoKit
import Foundation
import Testing

@Suite("Diagnostics helper pairing cross-language foundation (M3)")
struct DiagnosticsPairingM3GoldenTests {
    @Test("Decision 022 message types 0 through 24 are byte-exact and signed by the declared key")
    func allMessageTypes() throws {
        let fixture = try M3PairingFixtureLoader.load()
        let contract = try M1ContractFixtureLoader.load()
        let expectedNames = [
            "00_qr", "01_app_request", "02_helper_accept", "03_finalize", "04_finalize_ack",
            "05_receipt", "06_ready_ack", "07_activate", "08_active_ack", "09_abort", "10_abort_ack",
            "11_app_key_rotation_request", "12_app_key_rotation_new_proof", "13_app_key_rotation_accept",
            "14_helper_key_rotation_propose", "15_helper_key_rotation_new_proof", "16_helper_key_rotation_confirm",
            "17_tls_pin_rotation_propose", "18_tls_pin_rotation_confirm", "19_revocation_request",
            "20_revocation_record", "21_lifecycle_finalize", "22_lifecycle_active_ack",
            "23_lifecycle_abort", "24_lifecycle_abort_ack",
        ]
        #expect(fixture.keys.sorted() == expectedNames)

        for (expectedType, name) in expectedNames.enumerated() {
            let encoded = try Data(m1Hex: #require(fixture[name]))
            let decoded = try M1DeterministicCBOR.decode(encoded)
            #expect(try M1DeterministicCBOR.encode(decoded) == encoded)

            let fields = try m3PairingFields(decoded)
            #expect(try m3PairingUnsigned(fields, label: 4) == UInt64(expectedType))
            if expectedType == 0 {
                #expect(fields.first(where: { $0.label == 255 }) == nil)
                continue
            }

            let signature = try m3PairingBytes(fields, label: 255, count: 64)
            let body = try M1DeterministicCBOR.encode(.map(fields.filter { $0.label != 255 }))
            let domainName = "pairing." + name.dropFirst(3)
            let domain = try #require(contract.domains[domainName])
            var signedInput = Data(domain.utf8)
            signedInput.append(body)

            let keyLabel = try m3PairingSignerLabel(messageType: expectedType, fields: fields)
            let publicKeyBytes = try m3PairingBytes(fields, label: keyLabel, count: 32)
            let publicKey = try Curve25519.Signing.PublicKey(rawRepresentation: publicKeyBytes)
            #expect(publicKey.isValidSignature(signature, for: signedInput))

            var mutatedBody = body
            mutatedBody[mutatedBody.startIndex] ^= 0x01
            var mutatedInput = Data(domain.utf8)
            mutatedInput.append(mutatedBody)
            #expect(!publicKey.isValidSignature(signature, for: mutatedInput))
        }
    }
}

private final class M3PairingFixtureBundleToken {}

private enum M3PairingFixtureLoader {
    static func load(filePath: StaticString = #filePath) throws -> [String: String] {
        let bundle = Bundle(for: M3PairingFixtureBundleToken.self)
        let bundled = bundle.url(forResource: "diagnostics-pairing-m3", withExtension: "json")
        let sourceFallback = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures", isDirectory: true)
            .appendingPathComponent("diagnostics-pairing-m3.json")
        return try JSONDecoder().decode([String: String].self, from: Data(contentsOf: bundled ?? sourceFallback))
    }
}

private func m3PairingFields(_ value: M1CBORValue) throws -> [M1CBORField] {
    guard case .map(let fields) = value else {
        throw M1ContractTestError.invalid("pairing message is not a map")
    }
    return fields
}

private func m3PairingUnsigned(_ fields: [M1CBORField], label: UInt64) throws -> UInt64 {
    guard case .unsigned(let value) = fields.first(where: { $0.label == label })?.value else {
        throw M1ContractTestError.invalid("pairing uint field is missing")
    }
    return value
}

private func m3PairingBytes(_ fields: [M1CBORField], label: UInt64, count: Int) throws -> Data {
    guard case .bytes(let value) = fields.first(where: { $0.label == label })?.value,
          value.count == count else {
        throw M1ContractTestError.invalid("pairing byte-string field is missing")
    }
    return value
}

private func m3PairingSignerLabel(messageType: Int, fields: [M1CBORField]) throws -> UInt64 {
    switch messageType {
    case 1, 3, 5, 7, 9:
        return 18
    case 2, 4, 6, 8, 10:
        return 9
    case 11, 16, 18, 19, 23:
        return 7
    case 12:
        return 9
    case 13, 14, 17, 20, 24:
        return 11
    case 15:
        return 13
    case 21:
        return try m3PairingUnsigned(fields, label: 29) == 1 ? 9 : 7
    case 22:
        return try m3PairingUnsigned(fields, label: 29) == 2 ? 13 : 11
    default:
        throw M1ContractTestError.invalid("pairing message type has no signer")
    }
}

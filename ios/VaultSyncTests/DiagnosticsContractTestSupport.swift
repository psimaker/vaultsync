import CryptoKit
import Foundation

enum M1ContractTestError: Error, Equatable {
    case invalid(String)
}

struct M1ContractFixture: Decodable {
    let fixtureVersion: Int
    let sourceDecisions: [String]
    let limits: Limits
    let capabilities: [String: String]
    let registries: [String: [String: String]]
    let domains: [String: String]
    let digestChains: [String: String]
    let vectors: Vectors

    struct Limits: Decodable, Equatable {
        let maximumMessageBytes: Int
        let maximumMapEntries: Int
        let maximumArrayEntries: Int
        let maximumNestingDepth: Int
    }

    struct Vectors: Decodable {
        let rfc8032: RFC8032
        let rfc8032Additional: [RFC8032]
        let bootstrapHmac: BootstrapHMAC
        let derivations: Derivations
        let contractQuery: ContractQuery
        let privacy: Privacy
        let relayV1: RelayV1
    }

    struct RFC8032: Decodable {
        let seedHex: String
        let publicKeyHex: String
        let messageHex: String
        let signatureHex: String
    }

    struct BootstrapHMAC: Decodable {
        let secretHex: String
        let canonicalBodyHex: String
        let expectedHmacHex: String
    }

    struct Derivations: Decodable {
        let rawPublicKeyHex: String
        let rawDeviceIdHex: String
        let folderId: String
        let tlsSpkiDerHex: String
        let homeserverBindingHex: String
        let folderBindingHex: String
        let appRequestDigestHex: String
        let helperAcceptDigestHex: String
        let operationIdHex: String
        let expectedKeyIdHex: String
        let expectedDeviceIdDigestHex: String
        let expectedFolderIdDigestHex: String
        let expectedTlsSpkiPinHex: String
        let expectedInstallationBindingHex: String
        let expectedTranscriptFingerprint: String
        let expectedInstallationComponent: String
        let expectedOperationComponent: String
        let expectedRequestFilename: String
        let expectedAttestationFilename: String
        let expectedResponseFilename: String
        let expectedHelperEpochFilename: String
        let expectedAuthorizationEpochFilename: String
        let expectedPayloadDigestHex: String
    }

    struct ContractQuery: Decodable {
        let issuedAt: UInt64
        let expiresAt: UInt64
        let homeserverByte: UInt8
        let folderByte: UInt8
        let helperPublicKeyByte: UInt8
        let queryNonceByte: UInt8
        let expectedCanonicalBodyHex: String
        let expectedDigestHex: String
        let expectedSignatureHex: String
    }

    struct Privacy: Decodable {
        let sentinels: [String]
        let expectedLogSnapshot: String
        let expectedPersistenceSnapshot: String
    }

    struct RelayV1: Decodable {
        let triggerPath: String
        let triggerBody: String
        let statusPath: String
        let statusRequestFields: [String]
        let forbiddenContractFields: [String]
    }

}

private final class M1ContractFixtureBundleToken {}

enum M1ContractFixtureLoader {
    static func load(filePath: StaticString = #filePath) throws -> M1ContractFixture {
        let bundle = Bundle(for: M1ContractFixtureBundleToken.self)
        let bundled = bundle.url(forResource: "diagnostics-contract-v1", withExtension: "json")
        let sourceFallback = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures", isDirectory: true)
            .appendingPathComponent("diagnostics-contract-v1.json")
        let url = bundled ?? sourceFallback
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(M1ContractFixture.self, from: Data(contentsOf: url))
    }
}

indirect enum M1CBORValue: Equatable {
    case unsigned(UInt64)
    case bytes(Data)
    case text(String)
    case array([M1CBORValue])
    case map([M1CBORField])
}

struct M1CBORField: Equatable {
    let label: UInt64
    var value: M1CBORValue
}

enum M1DeterministicCBOR {
    static let maximumMessageBytes = 16 * 1024
    static let maximumMapEntries = 32
    static let maximumArrayEntries = 8
    static let maximumNestingDepth = 4

    static func encode(_ value: M1CBORValue) throws -> Data {
        var data = Data()
        try append(value, to: &data, depth: 0)
        guard data.count <= maximumMessageBytes else {
            throw M1ContractTestError.invalid("encoded message is oversized")
        }
        return data
    }

    static func decode(_ data: Data) throws -> M1CBORValue {
        guard !data.isEmpty else { throw M1ContractTestError.invalid("empty input") }
        guard data.count <= maximumMessageBytes else {
            throw M1ContractTestError.invalid("input is oversized")
        }
        var decoder = Decoder(data: data)
        let value = try decoder.decode(depth: 0)
        guard decoder.index == data.count else {
            throw M1ContractTestError.invalid("trailing input")
        }
        guard try encode(value) == data else {
            throw M1ContractTestError.invalid("non-deterministic input")
        }
        return value
    }

    private static func append(_ value: M1CBORValue, to data: inout Data, depth: Int) throws {
        guard depth <= maximumNestingDepth else {
            throw M1ContractTestError.invalid("nesting is too deep")
        }
        switch value {
        case .unsigned(let number):
            appendHead(major: 0, value: number, to: &data)
        case .bytes(let bytes):
            appendHead(major: 2, value: UInt64(bytes.count), to: &data)
            data.append(bytes)
        case .text(let text):
            let bytes = Data(text.utf8)
            appendHead(major: 3, value: UInt64(bytes.count), to: &data)
            data.append(bytes)
        case .array(let values):
            guard values.count <= maximumArrayEntries else {
                throw M1ContractTestError.invalid("too many array entries")
            }
            appendHead(major: 4, value: UInt64(values.count), to: &data)
            for child in values {
                try append(child, to: &data, depth: depth + 1)
            }
        case .map(let inputFields):
            guard inputFields.count <= maximumMapEntries else {
                throw M1ContractTestError.invalid("too many map entries")
            }
            let fields = inputFields.sorted { $0.label < $1.label }
            for pair in zip(fields, fields.dropFirst()) where pair.0.label == pair.1.label {
                throw M1ContractTestError.invalid("duplicate map key")
            }
            appendHead(major: 5, value: UInt64(fields.count), to: &data)
            for field in fields {
                appendHead(major: 0, value: field.label, to: &data)
                try append(field.value, to: &data, depth: depth + 1)
            }
        }
    }

    private static func appendHead(major: UInt8, value: UInt64, to data: inout Data) {
        switch value {
        case 0..<24:
            data.append(major << 5 | UInt8(value))
        case 24...UInt64(UInt8.max):
            data.append(major << 5 | 24)
            data.append(UInt8(value))
        case 0x100...UInt64(UInt16.max):
            data.append(major << 5 | 25)
            appendBigEndian(value, bytes: 2, to: &data)
        case 0x1_0000...UInt64(UInt32.max):
            data.append(major << 5 | 26)
            appendBigEndian(value, bytes: 4, to: &data)
        default:
            data.append(major << 5 | 27)
            appendBigEndian(value, bytes: 8, to: &data)
        }
    }

    private static func appendBigEndian(_ value: UInt64, bytes: Int, to data: inout Data) {
        for shift in stride(from: (bytes - 1) * 8, through: 0, by: -8) {
            data.append(UInt8(truncatingIfNeeded: value >> UInt64(shift)))
        }
    }

    private struct Decoder {
        let data: Data
        var index = 0

        mutating func decode(depth: Int) throws -> M1CBORValue {
            guard depth <= M1DeterministicCBOR.maximumNestingDepth else {
                throw M1ContractTestError.invalid("nesting is too deep")
            }
            let initial = try readByte()
            let major = initial >> 5
            let argument = try readArgument(additional: initial & 0x1f)
            switch major {
            case 0:
                return .unsigned(argument)
            case 2:
                return .bytes(try readBody(length: argument))
            case 3:
                let body = try readBody(length: argument)
                guard let text = String(data: body, encoding: .utf8) else {
                    throw M1ContractTestError.invalid("invalid UTF-8")
                }
                return .text(text)
            case 4:
                guard argument <= M1DeterministicCBOR.maximumArrayEntries else {
                    throw M1ContractTestError.invalid("too many array entries")
                }
                var values: [M1CBORValue] = []
                values.reserveCapacity(Int(argument))
                for _ in 0..<argument {
                    values.append(try decode(depth: depth + 1))
                }
                return .array(values)
            case 5:
                guard argument <= M1DeterministicCBOR.maximumMapEntries else {
                    throw M1ContractTestError.invalid("too many map entries")
                }
                var fields: [M1CBORField] = []
                fields.reserveCapacity(Int(argument))
                var previous: UInt64?
                for _ in 0..<argument {
                    guard case .unsigned(let label) = try decode(depth: depth + 1) else {
                        throw M1ContractTestError.invalid("map key is not uint")
                    }
                    if let previous, label <= previous {
                        throw M1ContractTestError.invalid("duplicate or reordered key")
                    }
                    previous = label
                    fields.append(M1CBORField(label: label, value: try decode(depth: depth + 1)))
                }
                return .map(fields)
            default:
                throw M1ContractTestError.invalid("forbidden major type")
            }
        }

        private mutating func readArgument(additional: UInt8) throws -> UInt64 {
            switch additional {
            case 0..<24:
                return UInt64(additional)
            case 24:
                let value = UInt64(try readByte())
                guard value >= 24 else { throw M1ContractTestError.invalid("non-shortest argument") }
                return value
            case 25:
                let value = try readBigEndian(bytes: 2)
                guard value > UInt8.max else { throw M1ContractTestError.invalid("non-shortest argument") }
                return value
            case 26:
                let value = try readBigEndian(bytes: 4)
                guard value > UInt16.max else { throw M1ContractTestError.invalid("non-shortest argument") }
                return value
            case 27:
                let value = try readBigEndian(bytes: 8)
                guard value > UInt32.max else { throw M1ContractTestError.invalid("non-shortest argument") }
                return value
            default:
                throw M1ContractTestError.invalid("reserved or indefinite argument")
            }
        }

        private mutating func readBigEndian(bytes: Int) throws -> UInt64 {
            var value: UInt64 = 0
            for _ in 0..<bytes {
                value = value << 8 | UInt64(try readByte())
            }
            return value
        }

        private mutating func readBody(length: UInt64) throws -> Data {
            guard length <= M1DeterministicCBOR.maximumMessageBytes,
                  length <= UInt64(data.count - index) else {
                throw M1ContractTestError.invalid("truncated or oversized body")
            }
            let start = index
            index += Int(length)
            return data.subdata(in: start..<index)
        }

        private mutating func readByte() throws -> UInt8 {
            guard index < data.count else { throw M1ContractTestError.invalid("truncated input") }
            defer { index += 1 }
            return data[index]
        }
    }
}

enum M1ContractCrypto {
    static func sha256(domain: String, body: Data) -> Data {
        var input = Data(domain.utf8)
        input.append(body)
        return Data(SHA256.hash(data: input))
    }

    static func sha256(_ body: Data) -> Data {
        Data(SHA256.hash(data: body))
    }

    static func hmacSHA256(key: Data, domain: String, body: Data) -> Data {
        var input = Data(domain.utf8)
        input.append(body)
        let authentication = HMAC<SHA256>.authenticationCode(for: input, using: SymmetricKey(data: key))
        return Data(authentication)
    }

    static func keyID(publicKey: Data) -> Data {
        sha256(domain: "eu.vaultsync.key-id/ed25519/v1\0", body: publicKey)
    }

    static func base32LowerNoPadding(_ data: Data) -> String {
        let alphabet = Array("abcdefghijklmnopqrstuvwxyz234567".utf8)
        var result: [UInt8] = []
        var buffer: UInt32 = 0
        var bits = 0
        for byte in data {
            buffer = buffer << 8 | UInt32(byte)
            bits += 8
            while bits >= 5 {
                bits -= 5
                result.append(alphabet[Int((buffer >> UInt32(bits)) & 0x1f)])
            }
            if bits == 0 {
                buffer = 0
            } else {
                buffer &= (1 << UInt32(bits)) - 1
            }
        }
        if bits > 0 {
            result.append(alphabet[Int((buffer << UInt32(5 - bits)) & 0x1f)])
        }
        return String(decoding: result, as: UTF8.self)
    }
}

struct M1ContractQueryGolden {
    let body: Data
    let digest: Data
    let signature: Data

    static func make(fixture: M1ContractFixture) throws -> M1ContractQueryGolden {
        let vector = fixture.vectors.contractQuery
        let rfc = fixture.vectors.rfc8032
        let publicKey = try Data(m1Hex: rfc.publicKeyHex)
        let appKeyID = M1ContractCrypto.keyID(publicKey: publicKey)
        let helperKeyID = M1ContractCrypto.keyID(publicKey: Data(repeating: vector.helperPublicKeyByte, count: 32))
        let bodyValue = M1CBORValue.map([
            M1CBORField(label: 1, value: .text(try required(fixture.capabilities["roundtrip"]))),
            M1CBORField(label: 2, value: .unsigned(1)),
            M1CBORField(label: 3, value: .unsigned(1)),
            M1CBORField(label: 4, value: .unsigned(1)),
            M1CBORField(label: 5, value: .bytes(Data(repeating: vector.homeserverByte, count: 32))),
            M1CBORField(label: 6, value: .bytes(Data(repeating: vector.folderByte, count: 32))),
            M1CBORField(label: 7, value: .bytes(appKeyID)),
            M1CBORField(label: 8, value: .bytes(helperKeyID)),
            M1CBORField(label: 9, value: .unsigned(1)),
            M1CBORField(label: 10, value: .unsigned(1)),
            M1CBORField(label: 12, value: .unsigned(vector.issuedAt)),
            M1CBORField(label: 13, value: .unsigned(vector.expiresAt)),
            M1CBORField(label: 30, value: .bytes(Data(repeating: vector.queryNonceByte, count: 32))),
        ])
        let body = try M1DeterministicCBOR.encode(bodyValue)
        let domain = try required(fixture.domains["roundtrip.capability_query"])
        let digest = M1ContractCrypto.sha256(domain: domain, body: body)
        var signedInput = Data(domain.utf8)
        signedInput.append(body)
        let privateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(m1Hex: rfc.seedHex))
        return M1ContractQueryGolden(body: body, digest: digest, signature: try privateKey.signature(for: signedInput))
    }
}

func m1ValidateCapabilityQuery(_ value: M1CBORValue, fixture: M1ContractFixture) throws {
    guard case .map(let fields) = value else { throw M1ContractTestError.invalid("not a map") }
    let expectedLabels: [UInt64] = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 30]
    guard fields.map(\.label) == expectedLabels else {
        throw M1ContractTestError.invalid("unknown or missing field")
    }
    let values = Dictionary(uniqueKeysWithValues: fields.map { ($0.label, $0.value) })
    guard values[1] == .text(try required(fixture.capabilities["roundtrip"])) else {
        throw M1ContractTestError.invalid("wrong capability")
    }
    for label in [2, 3, 4, 9, 10] where values[UInt64(label)] != .unsigned(1) {
        throw M1ContractTestError.invalid("wrong uint field")
    }
    for label in [5, 6, 7, 8, 30] {
        guard case .bytes(let bytes) = values[UInt64(label)], bytes.count == 32 else {
            throw M1ContractTestError.invalid("wrong byte-string field")
        }
    }
    guard case .unsigned(let issued) = values[12],
          case .unsigned(let expires) = values[13],
          expires > issued, expires - issued <= 120 else {
        throw M1ContractTestError.invalid("wrong time bounds")
    }
}

private func required<T>(_ value: T?) throws -> T {
    guard let value else { throw M1ContractTestError.invalid("missing fixture field") }
    return value
}

extension Data {
    init(m1Hex: String) throws {
        guard m1Hex.count.isMultiple(of: 2) else {
            throw M1ContractTestError.invalid("odd hex length")
        }
        var bytes: [UInt8] = []
        bytes.reserveCapacity(m1Hex.count / 2)
        var index = m1Hex.startIndex
        while index < m1Hex.endIndex {
            let next = m1Hex.index(index, offsetBy: 2)
            guard let byte = UInt8(m1Hex[index..<next], radix: 16) else {
                throw M1ContractTestError.invalid("invalid hex")
            }
            bytes.append(byte)
            index = next
        }
        self.init(bytes)
    }

    var m1Hex: String {
        map { String(format: "%02x", $0) }.joined()
    }
}

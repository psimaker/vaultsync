import CryptoKit
import Foundation
import Security

enum DiagnosticsProtocolError: Error, Equatable, Sendable {
    case invalidMessage
    case expired
    case unavailable
    case unsupported
    case conflict
    case rateLimited
    case protectedDataUnavailable
    case recoveryRequired
}

indirect enum DiagnosticsCBORValue: Equatable, Sendable {
    case unsigned(UInt64)
    case bytes(Data)
    case text(String)
    case array([DiagnosticsCBORValue])
    case map([DiagnosticsCBORField])
}

struct DiagnosticsCBORField: Equatable, Sendable {
    let label: UInt64
    var value: DiagnosticsCBORValue
}

enum DiagnosticsDeterministicCBOR {
    static let maximumMessageBytes = 16 * 1024
    static let maximumMapEntries = 32
    static let maximumArrayEntries = 8
    static let maximumNestingDepth = 4

    static func encode(_ value: DiagnosticsCBORValue) throws -> Data {
        var data = Data()
        try append(value, to: &data, depth: 0)
        guard data.count <= maximumMessageBytes else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return data
    }

    static func decode(_ data: Data) throws -> DiagnosticsCBORValue {
        guard !data.isEmpty, data.count <= maximumMessageBytes else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var decoder = Decoder(data: data)
        let value = try decoder.decode(depth: 0)
        guard decoder.index == data.count, try encode(value) == data else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return value
    }

    private static func append(_ value: DiagnosticsCBORValue, to data: inout Data, depth: Int) throws {
        guard depth <= maximumNestingDepth else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        switch value {
        case .unsigned(let number):
            appendHead(major: 0, value: number, to: &data)
        case .bytes(let bytes):
            appendHead(major: 2, value: UInt64(bytes.count), to: &data)
            data.append(bytes)
        case .text(let text):
            guard text.unicodeScalars.allSatisfy({ $0.value <= 0x7f }) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let bytes = Data(text.utf8)
            appendHead(major: 3, value: UInt64(bytes.count), to: &data)
            data.append(bytes)
        case .array(let values):
            guard values.count <= maximumArrayEntries else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            appendHead(major: 4, value: UInt64(values.count), to: &data)
            for child in values {
                try append(child, to: &data, depth: depth + 1)
            }
        case .map(let inputFields):
            guard inputFields.count <= maximumMapEntries else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let fields = inputFields.sorted { $0.label < $1.label }
            for pair in zip(fields, fields.dropFirst()) where pair.0.label == pair.1.label {
                throw DiagnosticsProtocolError.invalidMessage
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

        mutating func decode(depth: Int) throws -> DiagnosticsCBORValue {
            guard depth <= DiagnosticsDeterministicCBOR.maximumNestingDepth else {
                throw DiagnosticsProtocolError.invalidMessage
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
                guard let text = String(data: body, encoding: .utf8),
                      text.unicodeScalars.allSatisfy({ $0.value <= 0x7f }) else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                return .text(text)
            case 4:
                guard argument <= DiagnosticsDeterministicCBOR.maximumArrayEntries else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                var values: [DiagnosticsCBORValue] = []
                values.reserveCapacity(Int(argument))
                for _ in 0..<argument {
                    values.append(try decode(depth: depth + 1))
                }
                return .array(values)
            case 5:
                guard argument <= DiagnosticsDeterministicCBOR.maximumMapEntries else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                var fields: [DiagnosticsCBORField] = []
                fields.reserveCapacity(Int(argument))
                var previous: UInt64?
                for _ in 0..<argument {
                    guard case .unsigned(let label) = try decode(depth: depth + 1),
                          previous == nil || label > previous! else {
                        throw DiagnosticsProtocolError.invalidMessage
                    }
                    previous = label
                    fields.append(DiagnosticsCBORField(label: label, value: try decode(depth: depth + 1)))
                }
                return .map(fields)
            default:
                throw DiagnosticsProtocolError.invalidMessage
            }
        }

        private mutating func readArgument(additional: UInt8) throws -> UInt64 {
            switch additional {
            case 0..<24:
                return UInt64(additional)
            case 24:
                let value = UInt64(try readByte())
                guard value >= 24 else { throw DiagnosticsProtocolError.invalidMessage }
                return value
            case 25:
                let value = try readBigEndian(bytes: 2)
                guard value > UInt8.max else { throw DiagnosticsProtocolError.invalidMessage }
                return value
            case 26:
                let value = try readBigEndian(bytes: 4)
                guard value > UInt16.max else { throw DiagnosticsProtocolError.invalidMessage }
                return value
            case 27:
                let value = try readBigEndian(bytes: 8)
                guard value > UInt32.max else { throw DiagnosticsProtocolError.invalidMessage }
                return value
            default:
                throw DiagnosticsProtocolError.invalidMessage
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
            guard length <= DiagnosticsDeterministicCBOR.maximumMessageBytes,
                  length <= UInt64(data.count - index) else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            let start = index
            index += Int(length)
            return data.subdata(in: start..<index)
        }

        private mutating func readByte() throws -> UInt8 {
            guard index < data.count else { throw DiagnosticsProtocolError.invalidMessage }
            defer { index += 1 }
            return data[index]
        }
    }
}

extension DiagnosticsCBORValue {
    var fields: [DiagnosticsCBORField]? {
        guard case .map(let fields) = self else { return nil }
        return fields
    }

    func value(for label: UInt64) -> DiagnosticsCBORValue? {
        fields?.first(where: { $0.label == label })?.value
    }

    func unsigned(for label: UInt64) -> UInt64? {
        guard case .unsigned(let value) = value(for: label) else { return nil }
        return value
    }

    func bytes(for label: UInt64, count: Int? = nil) -> Data? {
        guard case .bytes(let value) = value(for: label), count == nil || value.count == count else { return nil }
        return value
    }

    func text(for label: UInt64) -> String? {
        guard case .text(let value) = value(for: label) else { return nil }
        return value
    }

    func removing(labels: Set<UInt64>) -> DiagnosticsCBORValue {
        guard case .map(let fields) = self else { return self }
        return .map(fields.filter { !labels.contains($0.label) })
    }
}

enum DiagnosticsCrypto {
    static func randomBytes(count: Int) throws -> Data {
        guard count > 0 else { throw DiagnosticsProtocolError.invalidMessage }
        var data = Data(repeating: 0, count: count)
        let status = data.withUnsafeMutableBytes { bytes in
            SecRandomCopyBytes(kSecRandomDefault, count, bytes.baseAddress!)
        }
        guard status == errSecSuccess, data.contains(where: { $0 != 0 }) else {
            throw DiagnosticsProtocolError.unavailable
        }
        return data
    }

    static func sha256(_ data: Data) -> Data {
        Data(SHA256.hash(data: data))
    }

    static func sha256(domain: String, body: Data) -> Data {
        var input = Data(domain.utf8)
        input.append(body)
        return sha256(input)
    }

    static func hmacSHA256(key: Data, domain: String, body: Data) -> Data {
        var input = Data(domain.utf8)
        input.append(body)
        return Data(HMAC<SHA256>.authenticationCode(for: input, using: SymmetricKey(data: key)))
    }

    static func keyID(publicKey: Data) -> Data {
        sha256(domain: "eu.vaultsync.key-id/ed25519/v1\0", body: publicKey)
    }

    static func signedMessageDigest(domain: String, value: DiagnosticsCBORValue) throws -> Data {
        sha256(domain: domain, body: try DiagnosticsDeterministicCBOR.encode(value.removing(labels: [255])))
    }

    static func base64URLDecode(_ encoded: String) throws -> Data {
        let maximumEncodedBytes = (DiagnosticsDeterministicCBOR.maximumMessageBytes * 4 + 2) / 3
        guard !encoded.isEmpty,
              encoded.utf8.count <= maximumEncodedBytes,
              !encoded.contains("="),
              encoded.utf8.allSatisfy({
                  ($0 >= 0x41 && $0 <= 0x5a) || ($0 >= 0x61 && $0 <= 0x7a) ||
                      ($0 >= 0x30 && $0 <= 0x39) || $0 == 0x2d || $0 == 0x5f
              }) else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var standard = encoded.replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        standard.append(String(repeating: "=", count: (4 - standard.count % 4) % 4))
        guard let data = Data(base64Encoded: standard), base64URLEncode(data) == encoded else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        return data
    }

    static func base64URLEncode(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    static func fingerprint(appRequestDigest: Data, helperAcceptDigest: Data) throws -> String {
        guard appRequestDigest.count == 32, helperAcceptDigest.count == 32 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var body = appRequestDigest
        body.append(helperAcceptDigest)
        return sha256(domain: "eu.vaultsync.helper-pairing/v1/transcript-fingerprint\0", body: body)
            .prefix(6)
            .map { String(format: "%02X", $0) }
            .joined()
    }
}

enum DiagnosticsSyncthingBinding {
    private static let base32Alphabet = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567".utf8)

    static func rawDeviceID(_ display: String) throws -> Data {
        let compact = display.replacingOccurrences(of: "-", with: "")
        guard compact.count == 56,
              compact.utf8.allSatisfy({ base32Alphabet.contains($0) }) else {
            throw DiagnosticsProtocolError.unsupported
        }
        let bytes = Array(compact.utf8)
        let canonical = stride(from: 0, to: bytes.count, by: 7)
            .map { String(decoding: bytes[$0..<($0 + 7)], as: UTF8.self) }
            .joined(separator: "-")
        guard display == canonical else { throw DiagnosticsProtocolError.unsupported }
        var encoded: [UInt8] = []
        encoded.reserveCapacity(52)
        for block in 0..<4 {
            let start = block * 14
            let body = Array(bytes[start..<(start + 13)])
            guard bytes[start + 13] == luhn32(body) else {
                throw DiagnosticsProtocolError.unsupported
            }
            encoded.append(contentsOf: body)
        }
        let decoded = try decodeBase32(encoded)
        guard decoded.count == 32 else { throw DiagnosticsProtocolError.unsupported }
        return decoded
    }

    static func deviceDigest(_ display: String) throws -> Data {
        DiagnosticsCrypto.sha256(
            domain: "eu.vaultsync.binding/syncthing-device/v1\0",
            body: try rawDeviceID(display)
        )
    }

    static func folderDigest(_ folderID: String) throws -> Data {
        let identifier = Data(folderID.utf8)
        guard !identifier.isEmpty, identifier.count <= 255,
              String(data: identifier, encoding: .utf8) == folderID else {
            throw DiagnosticsProtocolError.unsupported
        }
        var body = Data()
        let length = UInt32(identifier.count).bigEndian
        withUnsafeBytes(of: length) { body.append(contentsOf: $0) }
        body.append(identifier)
        return DiagnosticsCrypto.sha256(domain: "eu.vaultsync.binding/syncthing-folder/v1\0", body: body)
    }

    private static func luhn32(_ input: [UInt8]) -> UInt8 {
        var factor = 1
        var sum = 0
        for character in input {
            let codepoint = base32Alphabet.firstIndex(of: character)!
            let multiplied = factor * codepoint
            factor = factor == 2 ? 1 : 2
            sum += multiplied / 32 + multiplied % 32
        }
        return base32Alphabet[(32 - sum % 32) % 32]
    }

    private static func decodeBase32(_ input: [UInt8]) throws -> Data {
        var output = Data()
        var buffer: UInt64 = 0
        var bits = 0
        for character in input {
            guard let index = base32Alphabet.firstIndex(of: character) else {
                throw DiagnosticsProtocolError.unsupported
            }
            buffer = (buffer << 5) | UInt64(index)
            bits += 5
            while bits >= 8 {
                bits -= 8
                output.append(UInt8((buffer >> UInt64(bits)) & 0xff))
                buffer &= bits == 0 ? 0 : (1 << UInt64(bits)) - 1
            }
        }
        guard bits == 0 || buffer == 0 else { throw DiagnosticsProtocolError.unsupported }
        return output
    }
}

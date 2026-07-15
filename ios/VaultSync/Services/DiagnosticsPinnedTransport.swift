import Darwin
import Foundation
import Security

protocol DiagnosticsTransporting: Sendable {
    func post(path: String, body: Data, responseBody: Bool) async throws -> Data?
}

final class DiagnosticsPinnedTransport: DiagnosticsTransporting, @unchecked Sendable {
    private let endpoint: URL
    private let host: String
    private let pin: Data

    init(host: String, port: UInt16, pin: Data) throws {
        guard pin.count == 32,
              !host.isEmpty,
              port > 0 else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var components = URLComponents()
        components.scheme = "https"
        components.host = host
        components.port = Int(port)
        components.path = "/"
        guard let endpoint = components.url,
              endpoint.scheme == "https",
              endpoint.host == host,
              endpoint.port == Int(port),
              endpoint.user == nil,
              endpoint.password == nil,
              endpoint.query == nil,
              endpoint.fragment == nil else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        self.endpoint = endpoint
        self.host = host
        self.pin = pin
    }

    func post(path: String, body: Data, responseBody: Bool) async throws -> Data? {
        let allowedPaths = [
            DiagnosticsPairingProtocol.path,
            DiagnosticsCapabilityProtocol.path,
            DiagnosticsNamespaceProtocol.enablementPath,
            DiagnosticsNamespaceProtocol.authorizationPath,
            DiagnosticsUploadProtocol.path,
        ]
        guard allowedPaths.contains(path),
              !body.isEmpty, body.count <= DiagnosticsDeterministicCBOR.maximumMessageBytes else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        let url = endpoint.appending(path: String(path.dropFirst()), directoryHint: .notDirectory)
        guard url.host == host, url.query == nil, url.fragment == nil else {
            throw DiagnosticsProtocolError.invalidMessage
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.httpBody = body
        request.timeoutInterval = 15
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        request.setValue("application/cbor", forHTTPHeaderField: "Content-Type")
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        request.setValue("identity", forHTTPHeaderField: "Accept-Encoding")

        let configuration = URLSessionConfiguration.ephemeral
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        configuration.httpShouldSetCookies = false
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpAdditionalHeaders = nil
        configuration.waitsForConnectivity = false
        configuration.tlsMinimumSupportedProtocolVersion = .TLSv13
        configuration.tlsMaximumSupportedProtocolVersion = .TLSv13
        let delegate = PinnedSessionDelegate(expectedHost: host, expectedPin: pin)
        let session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }

        var data = Data()
        let response: URLResponse
        do {
            let (bytes, receivedResponse) = try await session.bytes(for: request)
            response = receivedResponse
            for try await byte in bytes {
                guard data.count < DiagnosticsDeterministicCBOR.maximumMessageBytes else {
                    throw DiagnosticsProtocolError.invalidMessage
                }
                data.append(byte)
            }
        } catch let error as DiagnosticsProtocolError {
            throw error
        } catch {
            throw DiagnosticsProtocolError.unavailable
        }
        guard delegate.didAuthenticateServer,
              let http = response as? HTTPURLResponse,
              http.url?.host == host,
              http.value(forHTTPHeaderField: "Content-Encoding") == nil else {
            throw DiagnosticsProtocolError.unavailable
        }
        switch http.statusCode {
        case 200:
            guard responseBody,
                  !data.isEmpty,
                  data.count <= DiagnosticsDeterministicCBOR.maximumMessageBytes,
                  http.value(forHTTPHeaderField: "Content-Type") == "application/cbor",
                  Self.contentLength(http) == data.count else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            return data
        case 202:
            guard data.isEmpty, Self.contentLength(http) == 0,
                  http.value(forHTTPHeaderField: "Content-Type") == nil else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            return nil
        case 400:
            throw DiagnosticsProtocolError.invalidMessage
        case 404:
            throw DiagnosticsProtocolError.unavailable
        case 409:
            throw DiagnosticsProtocolError.conflict
        case 429:
            throw DiagnosticsProtocolError.rateLimited
        default:
            throw DiagnosticsProtocolError.unavailable
        }
    }

    static func isCanonicalIPAddress(_ host: String) -> Bool {
        var ipv4 = in_addr()
        if host.withCString({ inet_pton(AF_INET, $0, &ipv4) }) == 1 {
            return canonicalString(family: AF_INET, address: &ipv4) == host
        }
        var ipv6 = in6_addr()
        if host.withCString({ inet_pton(AF_INET6, $0, &ipv6) }) == 1 {
            return canonicalString(family: AF_INET6, address: &ipv6) == host
        }
        return false
    }

    private static func canonicalString<T>(family: Int32, address: inout T) -> String? {
        var buffer = [CChar](repeating: 0, count: Int(INET6_ADDRSTRLEN))
        let result = withUnsafePointer(to: &address) { pointer in
            inet_ntop(family, UnsafeRawPointer(pointer), &buffer, socklen_t(buffer.count))
        }
        guard result != nil else { return nil }
        let end = buffer.firstIndex(of: 0) ?? buffer.endIndex
        return String(decoding: buffer[..<end].map(UInt8.init(bitPattern:)), as: UTF8.self)
    }

    private static func contentLength(_ response: HTTPURLResponse) -> Int? {
        guard let raw = response.value(forHTTPHeaderField: "Content-Length"),
              let value = Int(raw), value >= 0 else { return nil }
        return value
    }
}

private final class PinnedSessionDelegate: NSObject, URLSessionDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    private let expectedHost: String
    private let expectedPin: Data
    private let lock = NSLock()
    private var authenticated = false

    init(expectedHost: String, expectedPin: Data) {
        self.expectedHost = expectedHost
        self.expectedPin = expectedPin
    }

    var didAuthenticateServer: Bool {
        lock.lock()
        defer { lock.unlock() }
        return authenticated
    }

    func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              challenge.protectionSpace.host == expectedHost,
              let trust = challenge.protectionSpace.serverTrust,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let certificate = chain.first,
              let key = SecCertificateCopyKey(certificate),
              let attributes = SecKeyCopyAttributes(key) as? [String: Any],
              attributes[kSecAttrKeyType as String] as? String == kSecAttrKeyTypeECSECPrimeRandom as String,
              attributes[kSecAttrKeySizeInBits as String] as? Int == 256,
              let external = SecKeyCopyExternalRepresentation(key, nil) as Data?,
              external.count == 65,
              external.first == 0x04 else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        // DER SubjectPublicKeyInfo prefix for id-ecPublicKey + prime256v1,
        // followed by the 65-byte uncompressed P-256 public point.
        var spki = Data([
            0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce,
            0x3d, 0x02, 0x01, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d,
            0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
        ])
        spki.append(external)
        let actualPin = DiagnosticsCrypto.sha256(spki)
        guard Self.constantTimeEqual(actualPin, expectedPin) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        guard DiagnosticsPinnedTrustEvaluator.evaluate(trust: trust, leaf: certificate) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        lock.lock()
        authenticated = true
        lock.unlock()
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) {
        completionHandler(nil)
    }

    private static func constantTimeEqual(_ lhs: Data, _ rhs: Data) -> Bool {
        guard lhs.count == rhs.count else { return false }
        var difference: UInt8 = 0
        for (left, right) in zip(lhs, rhs) {
            difference |= left ^ right
        }
        return difference == 0
    }
}

private enum DiagnosticsPinnedTrustEvaluator {
    static func evaluate(trust: SecTrust, leaf: SecCertificate) -> Bool {
        // The exact out-of-band pinned leaf is the sole challenge-scoped anchor.
        // URLSession's SSL policy continues to enforce the requested host and validity.
        guard SecTrustSetAnchorCertificates(trust, [leaf] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
              SecTrustSetNetworkFetchAllowed(trust, false) == errSecSuccess else {
            return false
        }
        var error: CFError?
        return SecTrustEvaluateWithError(trust, &error)
    }
}

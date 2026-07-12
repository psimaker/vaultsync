import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "relay")

/// API client for the VaultSync Central Relay (relay.vaultsync.eu).
/// Authentication is based on Syncthing Device IDs — no API keys or user accounts.
enum RelayService {

    /// Canonical production relay. NEVER experiment against this — it serves real
    /// paying users (lab golden rules #3/#4).
    static let productionRelayURL = "https://relay.vaultsync.eu"

    /// Base URL for all relay API calls. Production by default. In **DEBUG builds
    /// only** it can be redirected to a local mock relay via the
    /// `RELAY_BASE_URL_OVERRIDE` UserDefault — e.g. an Xcode scheme launch
    /// argument `-RELAY_BASE_URL_OVERRIDE http://127.0.0.1:8787`. The override is
    /// compiled out of release builds entirely, so a shipped app can NEVER point
    /// anywhere but production: trigger/provision experiments cannot reach real
    /// users.
    static var relayURL: String {
        #if DEBUG
        if let override = UserDefaults.standard.string(forKey: relayBaseURLOverrideKey),
           !override.isEmpty {
            return override
        }
        #endif
        return productionRelayURL
    }

    #if DEBUG
    static let relayBaseURLOverrideKey = "RELAY_BASE_URL_OVERRIDE"
    /// True when relay API calls are pointed at a mock instead of production.
    /// Surfaced in Relay Diagnostics so it is never silently the case.
    static var isUsingRelayOverride: Bool { relayURL != productionRelayURL }
    #endif

    private static let diagnosticsErrorStorageKey = "relay-diagnostics-last-error-v2"
    private static let legacyDiagnosticsErrorStorageKey = "relay-diagnostics-last-error"
    static let diagnosticsDidChangeNotification = Notification.Name("RelayDiagnosticsDidChange")

    enum RelayError: Error, LocalizedError {
        case rateLimited
        case serverError(statusCode: Int)
        case networkError(underlying: any Error)
        case provisionFailed(reason: String)
        case verifiedEntitlementRequired

        var errorDescription: String? {
            switch self {
            case .rateLimited:
                return L10n.tr("Relay request was rate limited.")
            case .serverError(let statusCode):
                return L10n.fmt("Relay server returned HTTP %d.", statusCode)
            case .networkError:
                return L10n.tr("Relay request could not reach the server.")
            case .provisionFailed(let reason):
                return reason.isEmpty ? L10n.tr("Relay provisioning failed.") : reason
            case .verifiedEntitlementRequired:
                return L10n.tr("StoreKit verification is required before Relay can be updated.")
            }
        }
    }

    struct HealthCheckResult: Equatable, Sendable {
        enum State: String, Sendable {
            case healthy
            case unhealthy
            case unreachable
            case timedOut
        }

        let state: State
        let checkedAt: Date
        let latencyMs: Int?
        let statusCode: Int?
        let message: String?

        var isHealthy: Bool {
            state == .healthy
        }

        var summary: String {
            switch state {
            case .healthy:
                return L10n.tr("Healthy")
            case .unhealthy:
                return L10n.tr("Unhealthy")
            case .unreachable:
                return L10n.tr("Unreachable")
            case .timedOut:
                return L10n.tr("Timed out")
            }
        }
    }

    enum RecordedRelayFailure: String, Codable, Equatable, Sendable {
        case nonHTTP
        case rateLimited
        case serverResponse
        case unauthorized
        case networkUnavailable
        case timedOut
        case unreadableResponse
    }

    struct RecordedRelayError: Codable, Equatable, Sendable {
        let context: String
        let failure: RecordedRelayFailure
        let statusCode: Int?
        let date: Date

        var message: String {
            switch failure {
            case .nonHTTP:
                return L10n.tr("Relay returned an unreadable response.")
            case .rateLimited:
                return L10n.tr("Relay request is temporarily rate limited.")
            case .serverResponse:
                return L10n.fmt("Relay request failed with HTTP %d.", statusCode ?? 0)
            case .unauthorized:
                return L10n.tr("Relay request requires purchase confirmation.")
            case .networkUnavailable:
                return L10n.tr("Relay request could not reach the server.")
            case .timedOut:
                return L10n.tr("Relay request timed out.")
            case .unreadableResponse:
                return L10n.tr("Relay response could not be read.")
            }
        }
    }

    // MARK: - Provision

    /// Provision a device for push notifications.
    /// Called after a successful StoreKit purchase to register the APNs token
    /// with the homeserver's Syncthing Device ID.
    static func provision(
        deviceID: String,
        apnsToken: String,
        signedTransaction: String
    ) async throws {
        let request = try makeProvisionRequest(
            baseURL: relayURL,
            deviceID: deviceID,
            apnsToken: apnsToken,
            signedTransaction: signedTransaction
        )

        let (_, response) = try await perform(request, action: "provision")

        guard let http = response as? HTTPURLResponse else {
            recordLastError(context: "provision", failure: .nonHTTP)
            throw RelayError.serverError(statusCode: 0)
        }

        switch http.statusCode {
        case let status where isSuccessfulProvisionStatusCode(status):
            clearLastError()
            logger.info("Provisioned relay registration")
        case 429:
            logger.warning("Relay provision: rate limited")
            recordLastError(context: "provision", failure: .rateLimited, statusCode: 429)
            throw RelayError.rateLimited
        case 500...599:
            logger.error("Relay provision server error: HTTP \(http.statusCode)")
            recordLastError(context: "provision", failure: .serverResponse, statusCode: http.statusCode)
            throw RelayError.serverError(statusCode: http.statusCode)
        default:
            recordLastError(context: "provision", failure: .serverResponse, statusCode: http.statusCode)
            throw RelayError.provisionFailed(
                reason: L10n.fmt("Relay request failed with HTTP %d.", http.statusCode)
            )
        }
    }

    static func isSuccessfulProvisionStatusCode(_ statusCode: Int) -> Bool {
        statusCode == 200
    }

    /// Builds the unchanged v1 wire request only for a compact signed
    /// transaction. Kept internal so regression tests can prove invalid local
    /// placeholders fail before URLSession is reached.
    static func makeProvisionRequest(
        baseURL: String,
        deviceID: String,
        apnsToken: String,
        signedTransaction: String
    ) throws -> URLRequest {
        guard RelayVerifiedEntitlement(signedTransaction: signedTransaction) != nil else {
            throw RelayError.verifiedEntitlementRequired
        }
        guard let url = URL(string: "\(baseURL)/api/v1/provision") else {
            throw RelayError.serverError(statusCode: 0)
        }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode([
            "device_id": deviceID,
            "apns_token": apnsToken,
            "transaction_id": signedTransaction,
        ])
        return request
    }

    // MARK: - Observation Status

    static func fetchStatus(
        deviceID: String,
        signedTransaction: String
    ) async throws -> RelayServerObservation {
        let request = try makeStatusRequest(
            baseURL: relayURL,
            deviceID: deviceID,
            signedTransaction: signedTransaction
        )
        let (data, response) = try await perform(request, action: "status")
        guard let http = response as? HTTPURLResponse else {
            throw RelayError.serverError(statusCode: 0)
        }
        switch http.statusCode {
        case 200:
            do {
                let observation = try decodeStatusResponse(data)
                clearLastError()
                return observation
            } catch {
                recordLastError(context: "status", failure: .unreadableResponse, statusCode: 200)
                throw RelayError.serverError(statusCode: 200)
            }
        case 401, 403:
            recordLastError(context: "status", failure: .unauthorized, statusCode: http.statusCode)
            throw RelayError.verifiedEntitlementRequired
        case 429:
            recordLastError(context: "status", failure: .rateLimited, statusCode: 429)
            throw RelayError.rateLimited
        default:
            recordLastError(context: "status", failure: .serverResponse, statusCode: http.statusCode)
            throw RelayError.serverError(statusCode: http.statusCode)
        }
    }

    static func makeStatusRequest(
        baseURL: String,
        deviceID: String,
        signedTransaction: String
    ) throws -> URLRequest {
        guard RelayVerifiedEntitlement(signedTransaction: signedTransaction) != nil else {
            throw RelayError.verifiedEntitlementRequired
        }
        guard let url = URL(string: "\(baseURL)/api/v1/status") else {
            throw RelayError.serverError(statusCode: 0)
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode([
            "device_id": deviceID,
            "signed_transaction": signedTransaction,
        ])
        return request
    }

    static func decodeStatusResponse(_ data: Data) throws -> RelayServerObservation {
        struct Response: Decodable {
            let triggerObserved: Bool
            let lastTriggerObservedAt: String?
            let checkedAt: String

            enum CodingKeys: String, CodingKey {
                case triggerObserved = "v1_trigger_observed"
                case lastTriggerObservedAt = "last_trigger_observed_at"
                case checkedAt = "checked_at"
            }
        }

        let response = try JSONDecoder().decode(Response.self, from: data)
        let formatter = ISO8601DateFormatter()
        guard let checkedAt = formatter.date(from: response.checkedAt) else {
            throw RelayError.serverError(statusCode: 200)
        }
        let lastObservedAt: Date?
        if let raw = response.lastTriggerObservedAt {
            guard let parsed = formatter.date(from: raw) else {
                throw RelayError.serverError(statusCode: 200)
            }
            lastObservedAt = parsed
        } else {
            lastObservedAt = nil
        }
        guard response.triggerObserved == (lastObservedAt != nil) else {
            throw RelayError.serverError(statusCode: 200)
        }
        return RelayServerObservation(
            triggerObserved: response.triggerObserved,
            lastTriggerObservedAt: lastObservedAt,
            checkedAt: checkedAt
        )
    }

    /// Deregister a device from push notifications.
    static func deprovision(
        deviceID: String,
        apnsToken: String
    ) async throws {
        let url = URL(string: "\(relayURL)/api/v1/provision")!

        var request = URLRequest(url: url)
        request.httpMethod = "DELETE"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")

        let body: [String: String] = [
            "device_id": deviceID,
            "apns_token": apnsToken,
        ]
        request.httpBody = try JSONEncoder().encode(body)

        try await performVoid(request, action: "deprovision")
        logger.info("Deprovisioned relay registration")
    }

    // MARK: - Health

    /// Run a relay health check with an explicit timeout policy.
    static func checkHealthResult(timeout: TimeInterval = 6) async -> HealthCheckResult {
        let url = URL(string: "\(relayURL)/api/v1/health")!
        var request = URLRequest(url: url)
        request.timeoutInterval = timeout
        let startedAt = Date()

        do {
            let (_, response) = try await session.data(for: request)
            let latencyMs = Int(Date().timeIntervalSince(startedAt) * 1000)

            guard let http = response as? HTTPURLResponse else {
                let message = L10n.tr("Relay health check returned a non-HTTP response.")
                recordLastError(context: "health", failure: .nonHTTP)
                return HealthCheckResult(
                    state: .unhealthy,
                    checkedAt: Date(),
                    latencyMs: latencyMs,
                    statusCode: nil,
                    message: message
                )
            }

            if http.statusCode == 200 {
                clearLastError()
                return HealthCheckResult(
                    state: .healthy,
                    checkedAt: Date(),
                    latencyMs: latencyMs,
                    statusCode: http.statusCode,
                    message: nil
                )
            }

            let message = L10n.fmt("Relay health endpoint returned HTTP %d.", http.statusCode)
            recordLastError(context: "health", failure: .serverResponse, statusCode: http.statusCode)
            return HealthCheckResult(
                state: .unhealthy,
                checkedAt: Date(),
                latencyMs: latencyMs,
                statusCode: http.statusCode,
                message: message
            )
        } catch {
            logger.error("Relay health check failed")
            let latencyMs = Int(Date().timeIntervalSince(startedAt) * 1000)
            let nsError = error as NSError
            let isTimeout = nsError.domain == NSURLErrorDomain && nsError.code == NSURLErrorTimedOut
            let state: HealthCheckResult.State = isTimeout ? .timedOut : .unreachable
            let message: String
            if isTimeout {
                message = L10n.fmt("Relay health check timed out after %ds.", Int(timeout))
            } else {
                message = L10n.tr("Relay request could not reach the server.")
            }
            recordLastError(
                context: "health",
                failure: isTimeout ? .timedOut : .networkUnavailable
            )
            return HealthCheckResult(
                state: state,
                checkedAt: Date(),
                latencyMs: latencyMs,
                statusCode: nil,
                message: message
            )
        }
    }

    static func lastRecordedError(defaults: UserDefaults = .standard) -> RecordedRelayError? {
        // Older entries stored free-form response bodies or network errors.
        // Remove them instead of surfacing or copying potentially sensitive data.
        defaults.removeObject(forKey: legacyDiagnosticsErrorStorageKey)
        guard let data = defaults.data(forKey: diagnosticsErrorStorageKey) else {
            return nil
        }
        guard let decoded = try? JSONDecoder().decode(RecordedRelayError.self, from: data) else {
            defaults.removeObject(forKey: diagnosticsErrorStorageKey)
            return nil
        }
        return decoded
    }

    // MARK: - Private

    private static let session: URLSession = {
        let config = URLSessionConfiguration.default
        config.timeoutIntervalForRequest = 10
        config.timeoutIntervalForResource = 10
        return URLSession(configuration: config)
    }()

    private static func perform(
        _ request: URLRequest,
        action: String
    ) async throws -> (Data, URLResponse) {
        do {
            return try await session.data(for: request)
        } catch {
            logger.error("Relay \(action) network request failed")
            recordLastError(context: action, failure: .networkUnavailable)
            throw RelayError.networkError(underlying: error)
        }
    }

    private static func performVoid(_ request: URLRequest, action: String) async throws {
        let response: URLResponse
        do {
            (_, response) = try await session.data(for: request)
        } catch {
            logger.error("Relay \(action) network request failed")
            recordLastError(context: action, failure: .networkUnavailable)
            throw RelayError.networkError(underlying: error)
        }

        guard let http = response as? HTTPURLResponse else {
            recordLastError(context: action, failure: .nonHTTP)
            throw RelayError.serverError(statusCode: 0)
        }

        switch http.statusCode {
        case 200...299:
            clearLastError()
            return
        case 429:
            logger.warning("Relay \(action): rate limited")
            recordLastError(context: action, failure: .rateLimited, statusCode: 429)
            throw RelayError.rateLimited
        case 401, 403:
            logger.error("Relay \(action) unauthorized: HTTP \(http.statusCode)")
            recordLastError(context: action, failure: .unauthorized, statusCode: http.statusCode)
            throw RelayError.provisionFailed(reason: L10n.tr("Unauthorized request."))
        default:
            logger.error("Relay \(action) failed: HTTP \(http.statusCode)")
            recordLastError(context: action, failure: .serverResponse, statusCode: http.statusCode)
            throw RelayError.serverError(statusCode: http.statusCode)
        }
    }

    private static func recordLastError(
        context: String,
        failure: RecordedRelayFailure,
        statusCode: Int? = nil
    ) {
        let allowedContexts = ["provision", "deprovision", "status", "health"]
        let entry = RecordedRelayError(
            context: allowedContexts.contains(context) ? context : "relay",
            failure: failure,
            statusCode: statusCode,
            date: Date()
        )
        guard let data = try? JSONEncoder().encode(entry) else { return }
        UserDefaults.standard.set(data, forKey: diagnosticsErrorStorageKey)
        NotificationCenter.default.post(name: diagnosticsDidChangeNotification, object: nil)
    }

    private static func clearLastError() {
        UserDefaults.standard.removeObject(forKey: legacyDiagnosticsErrorStorageKey)
        if UserDefaults.standard.data(forKey: diagnosticsErrorStorageKey) == nil {
            return
        }
        UserDefaults.standard.removeObject(forKey: diagnosticsErrorStorageKey)
        NotificationCenter.default.post(name: diagnosticsDidChangeNotification, object: nil)
    }

    static func userError(from error: any Error) -> SyncUserError {
        guard let relayError = error as? RelayError else {
            return SyncUserError.from(error: error, fallbackTitle: L10n.tr("Relay Error"))
        }

        switch relayError {
        case .networkError:
            return SyncUserError.relayProvisionFailed(
                reason: L10n.tr("Relay request could not reach the server.")
            )
        case .rateLimited:
            return SyncUserError.relayProvisionFailed(reason: L10n.tr("Relay provisioning is currently rate limited (429)."))
        case .serverError(let statusCode):
            return SyncUserError.relayProvisionFailed(reason: L10n.fmt("Relay server error (HTTP %d).", statusCode))
        case .provisionFailed(let reason):
            return SyncUserError.relayProvisionFailed(reason: reason)
        case .verifiedEntitlementRequired:
            return SyncUserError.relayProvisionFailed(
                reason: L10n.tr("StoreKit verification is required before Relay can be updated.")
            )
        }
    }
}

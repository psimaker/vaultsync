import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "relay")

/// API client for the VaultSync Central Relay (relay.vaultsync.eu).
/// Authentication is based on Syncthing Device IDs — no API keys or user accounts.
enum RelayService {

    private static let relayURL = "https://relay.vaultsync.eu"
    private static let diagnosticsErrorStorageKey = "relay-diagnostics-last-error"
    static let diagnosticsDidChangeNotification = Notification.Name("RelayDiagnosticsDidChange")

    enum RelayError: Error, LocalizedError {
        case rateLimited
        case serverError(statusCode: Int)
        case networkError(underlying: any Error)
        case provisionFailed(reason: String)

        var errorDescription: String? {
            switch self {
            case .rateLimited:
                return "Relay request was rate limited."
            case .serverError(let statusCode):
                return "Relay server returned HTTP \(statusCode)."
            case .networkError(let underlying):
                return "Relay network error: \(underlying.localizedDescription)"
            case .provisionFailed(let reason):
                return reason.isEmpty ? "Relay provisioning failed." : reason
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
                return "Healthy"
            case .unhealthy:
                return "Unhealthy"
            case .unreachable:
                return "Unreachable"
            case .timedOut:
                return "Timed out"
            }
        }
    }

    struct RecordedRelayError: Codable, Equatable, Sendable {
        let context: String
        let message: String
        let date: Date
    }

    // MARK: - Provision

    /// Provision a device for push notifications.
    /// Called after a successful StoreKit purchase to register the APNs token
    /// with the homeserver's Syncthing Device ID.
    static func provision(
        deviceID: String,
        apnsToken: String,
        transactionID: String
    ) async throws {
        let url = URL(string: "\(relayURL)/api/v1/provision")!

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")

        let body: [String: String] = [
            "device_id": deviceID,
            "apns_token": apnsToken,
            "transaction_id": transactionID,
        ]
        request.httpBody = try JSONEncoder().encode(body)

        let (data, response) = try await perform(request, action: "provision")

        guard let http = response as? HTTPURLResponse else {
            recordLastError(context: "provision", message: "Relay provision returned a non-HTTP response.")
            throw RelayError.serverError(statusCode: 0)
        }

        switch http.statusCode {
        case 200:
            logger.info("Provisioned relay for device \(deviceID.prefix(8))...")
        case 409:
            logger.info("Device already provisioned, token updated")
        case 429:
            logger.warning("Relay provision: rate limited")
            recordLastError(context: "provision", message: "Relay provision is rate limited (HTTP 429).")
            throw RelayError.rateLimited
        case 500...599:
            let body = String(data: data, encoding: .utf8) ?? ""
            logger.error("Relay provision server error: \(http.statusCode) \(body)")
            recordLastError(
                context: "provision",
                message: body.isEmpty ? "Relay provision failed with HTTP \(http.statusCode)." : body
            )
            throw RelayError.serverError(statusCode: http.statusCode)
        default:
            let body = String(data: data, encoding: .utf8) ?? ""
            recordLastError(
                context: "provision",
                message: body.isEmpty ? "Relay provision failed with HTTP \(http.statusCode)." : body
            )
            throw RelayError.provisionFailed(reason: body)
        }
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
        logger.info("Deprovisioned relay for device \(deviceID.prefix(8))...")
    }

    // MARK: - Health

    /// Check if the relay server is reachable and healthy.
    static func checkHealth() async -> Bool {
        let result = await checkHealthResult()
        return result.isHealthy
    }

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
                let message = "Relay health check returned a non-HTTP response."
                recordLastError(context: "health", message: message)
                return HealthCheckResult(
                    state: .unhealthy,
                    checkedAt: Date(),
                    latencyMs: latencyMs,
                    statusCode: nil,
                    message: message
                )
            }

            if http.statusCode == 200 {
                return HealthCheckResult(
                    state: .healthy,
                    checkedAt: Date(),
                    latencyMs: latencyMs,
                    statusCode: http.statusCode,
                    message: nil
                )
            }

            let message = "Relay health endpoint returned HTTP \(http.statusCode)."
            recordLastError(context: "health", message: message)
            return HealthCheckResult(
                state: .unhealthy,
                checkedAt: Date(),
                latencyMs: latencyMs,
                statusCode: http.statusCode,
                message: message
            )
        } catch {
            logger.error("Relay health check failed: \(error)")
            let latencyMs = Int(Date().timeIntervalSince(startedAt) * 1000)
            let nsError = error as NSError
            let isTimeout = nsError.domain == NSURLErrorDomain && nsError.code == NSURLErrorTimedOut
            let state: HealthCheckResult.State = isTimeout ? .timedOut : .unreachable
            let message: String
            if isTimeout {
                message = "Relay health check timed out after \(Int(timeout))s."
            } else {
                message = "Relay health check failed: \(error.localizedDescription)"
            }
            recordLastError(context: "health", message: message)
            return HealthCheckResult(
                state: state,
                checkedAt: Date(),
                latencyMs: latencyMs,
                statusCode: nil,
                message: message
            )
        }
    }

    static func lastRecordedError() -> RecordedRelayError? {
        guard let data = UserDefaults.standard.data(forKey: diagnosticsErrorStorageKey),
              let decoded = try? JSONDecoder().decode(RecordedRelayError.self, from: data) else {
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
            logger.error("Relay \(action) network error: \(error)")
            recordLastError(context: action, message: "Relay \(action) network error: \(error.localizedDescription)")
            throw RelayError.networkError(underlying: error)
        }
    }

    private static func performVoid(_ request: URLRequest, action: String) async throws {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            logger.error("Relay \(action) network error: \(error)")
            recordLastError(context: action, message: "Relay \(action) network error: \(error.localizedDescription)")
            throw RelayError.networkError(underlying: error)
        }

        guard let http = response as? HTTPURLResponse else {
            recordLastError(context: action, message: "Relay \(action) returned a non-HTTP response.")
            throw RelayError.serverError(statusCode: 0)
        }

        switch http.statusCode {
        case 200...299:
            return
        case 429:
            logger.warning("Relay \(action): rate limited")
            recordLastError(context: action, message: "Relay \(action) is rate limited (HTTP 429).")
            throw RelayError.rateLimited
        case 401, 403:
            let body = String(data: data, encoding: .utf8) ?? ""
            logger.error("Relay \(action) unauthorized: \(http.statusCode) \(body)")
            recordLastError(
                context: action,
                message: body.isEmpty ? "Relay \(action) unauthorized (HTTP \(http.statusCode))." : body
            )
            throw RelayError.provisionFailed(reason: body.isEmpty ? "Unauthorized request." : body)
        default:
            let body = String(data: data, encoding: .utf8) ?? ""
            logger.error("Relay \(action) failed: \(http.statusCode) \(body)")
            recordLastError(
                context: action,
                message: body.isEmpty ? "Relay \(action) failed with HTTP \(http.statusCode)." : body
            )
            throw RelayError.serverError(statusCode: http.statusCode)
        }
    }

    private static func recordLastError(context: String, message: String) {
        let entry = RecordedRelayError(
            context: context,
            message: message,
            date: Date()
        )
        guard let data = try? JSONEncoder().encode(entry) else { return }
        UserDefaults.standard.set(data, forKey: diagnosticsErrorStorageKey)
        NotificationCenter.default.post(name: diagnosticsDidChangeNotification, object: nil)
    }

    static func userError(from error: any Error) -> SyncUserError {
        guard let relayError = error as? RelayError else {
            return SyncUserError.from(error: error, fallbackTitle: "Relay Error")
        }

        switch relayError {
        case .networkError(let underlying):
            return SyncUserError.from(
                rawMessage: "relay network: \(underlying.localizedDescription)",
                fallbackTitle: "Relay Unreachable"
            )
        case .rateLimited:
            return SyncUserError.relayProvisionFailed(reason: "Relay provisioning is currently rate limited (429).")
        case .serverError(let statusCode):
            return SyncUserError.relayProvisionFailed(reason: "Relay server error (HTTP \(statusCode)).")
        case .provisionFailed(let reason):
            return SyncUserError.relayProvisionFailed(reason: reason)
        }
    }
}

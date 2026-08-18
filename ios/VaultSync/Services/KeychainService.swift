import Foundation
import Security
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "keychain")

/// Minimal Keychain wrapper for storing String values.
enum KeychainService {

    private static let service = "eu.vaultsync.app"
    private static let apnsDeviceTokenKey = "apns-device-token"

    enum ReadResult: Equatable {
        case value(String)
        case notFound
        case corrupt
        case failed(OSStatus)
    }

    /// Synchronous Keychain effects used by the production decision logic.
    /// A computed live value keeps the non-Sendable closures stack-local while
    /// tests can inject exact Security statuses and observe call ordering.
    struct Environment {
        var encodeUTF8: (String) -> Data?
        var copyMatching: (CFDictionary, inout AnyObject?) -> OSStatus
        var update: (CFDictionary, CFDictionary) -> OSStatus
        var add: (CFDictionary) -> OSStatus
        var delete: (CFDictionary) -> OSStatus

        static var live: Self {
            Self(
                encodeUTF8: { $0.data(using: .utf8) },
                copyMatching: { query, result in
                    SecItemCopyMatching(query, &result)
                },
                update: { query, attributes in
                    SecItemUpdate(query, attributes)
                },
                add: { attributes in
                    SecItemAdd(attributes, nil)
                },
                delete: { query in
                    SecItemDelete(query)
                }
            )
        }
    }

    /// Store a string value in the Keychain.
    @discardableResult
    static func set(key: String, value: String) -> Bool {
        set(key: key, value: value, environment: .live)
    }

    /// Update first so a transient write failure can never erase the last valid
    /// credential. Add is reserved for a confirmed missing item; if another
    /// writer wins that race, retry Update once without ever deleting either
    /// value (#148).
    @discardableResult
    static func set(key: String, value: String, environment: Environment) -> Bool {
        guard let data = environment.encodeUTF8(value) else {
            logger.error("Keychain value encoding failed")
            return false
        }

        let identity: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
        let valueAttributes: [String: Any] = [kSecValueData as String: data]

        let updateStatus = environment.update(
            identity as CFDictionary,
            valueAttributes as CFDictionary
        )
        if updateStatus == errSecSuccess {
            return true
        }
        guard updateStatus == errSecItemNotFound else {
            logger.error("Keychain update failed with status category \(updateStatus, privacy: .public)")
            return false
        }

        var insertion = identity
        insertion[kSecValueData as String] = data
        insertion[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock

        let addStatus = environment.add(insertion as CFDictionary)
        if addStatus == errSecSuccess {
            return true
        }
        guard addStatus == errSecDuplicateItem else {
            logger.error("Keychain add failed with status category \(addStatus, privacy: .public)")
            return false
        }

        let retryStatus = environment.update(
            identity as CFDictionary,
            valueAttributes as CFDictionary
        )
        if retryStatus != errSecSuccess {
            logger.error("Keychain duplicate retry failed with status category \(retryStatus, privacy: .public)")
        }
        return retryStatus == errSecSuccess
    }

    /// Retrieve a string value from the Keychain.
    static func get(key: String) -> String? {
        get(key: key, environment: .live)
    }

    static func get(key: String, environment: Environment) -> String? {
        guard case .value(let value) = read(key: key, environment: environment) else {
            return nil
        }
        return value
    }

    static func read(key: String, environment: Environment = .live) -> ReadResult {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]

        var result: AnyObject?
        let status = environment.copyMatching(query as CFDictionary, &result)

        if status == errSecItemNotFound { return .notFound }
        guard status == errSecSuccess else { return .failed(status) }
        guard let data = result as? Data,
              let value = String(data: data, encoding: .utf8) else { return .corrupt }
        return .value(value)
    }

    /// Delete an item from the Keychain.
    @discardableResult
    static func delete(key: String) -> Bool {
        delete(key: key, environment: .live)
    }

    @discardableResult
    static func delete(key: String, environment: Environment) -> Bool {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]

        let status = environment.delete(query as CFDictionary)
        return status == errSecSuccess || status == errSecItemNotFound
    }

    // MARK: - APNs helpers

    @discardableResult
    static func setAPNsDeviceToken(_ token: String) -> Bool {
        set(key: apnsDeviceTokenKey, value: token)
    }

    static func getAPNsDeviceToken() -> String? {
        get(key: apnsDeviceTokenKey)
    }

    static func hasAPNsDeviceToken() -> Bool {
        getAPNsDeviceToken() != nil
    }

    @discardableResult
    static func clearAPNsDeviceToken() -> Bool {
        delete(key: apnsDeviceTokenKey)
    }
}

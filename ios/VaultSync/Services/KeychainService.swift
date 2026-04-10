import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "keychain")

/// Minimal Keychain wrapper for storing String values.
enum KeychainService {

    private static let service = "eu.vaultsync.app"
    private static let apnsDeviceTokenKey = "apns-device-token"

    /// Store a string value in the Keychain.
    @discardableResult
    static func set(key: String, value: String) -> Bool {
        guard let data = value.data(using: .utf8) else { return false }

        // Delete existing item first to avoid errSecDuplicateItem
        delete(key: key)

        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlock,
        ]

        let status = SecItemAdd(query as CFDictionary, nil)
        if status != errSecSuccess {
            logger.error("Keychain set failed for \(key): \(status)")
        }
        return status == errSecSuccess
    }

    /// Retrieve a string value from the Keychain.
    static func get(key: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]

        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)

        guard status == errSecSuccess, let data = result as? Data else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    /// Delete an item from the Keychain.
    @discardableResult
    static func delete(key: String) -> Bool {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]

        let status = SecItemDelete(query as CFDictionary)
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

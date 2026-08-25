import Foundation
import Security

/// Where the device token lives.
///
/// The Keychain rather than UserDefaults or a file: this is a bearer token that
/// can read and change every record on the account, and on a Mac the Keychain
/// is the one place the system already protects. The CLI writes a mode-0600
/// file because a terminal has no Keychain to reach for; an app has no such
/// excuse.
///
/// The app's token is its own, separate from the CLI's. Two device tokens mean
/// either can be revoked without disturbing the other.
enum Keychain {
    static let defaultService = "run.cobanov.punchcard.bar"
    private static let account = "device-token"

    static func save(token: String, service: String = defaultService) {
        // Delete-then-add rather than the SecItemUpdate dance: one record, less
        // code, no branch that only runs on a machine that has run this before.
        delete(service: service)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecValueData as String: Data(token.utf8),
        ]
        let status = SecItemAdd(query as CFDictionary, nil)
        if status != errSecSuccess {
            NSLog("punchcard: could not write the keychain item: \(status)")
        }
    }

    static func token(service: String = defaultService) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else { return nil }
        return String(decoding: data, as: UTF8.self)
    }

    static func delete(service: String = defaultService) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
    }
}

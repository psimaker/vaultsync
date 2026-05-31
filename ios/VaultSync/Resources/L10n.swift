import Foundation

enum L10n {
    /// `value: key` means a missing translation falls back to the (English) key
    /// text instead of rendering the bare key — matching the widget's helper.
    static func tr(_ key: String) -> String {
        NSLocalizedString(key, tableName: nil, bundle: .main, value: key, comment: "")
    }

    static func fmt(_ key: String, _ args: CVarArg...) -> String {
        String(format: tr(key), locale: Locale.current, arguments: args)
    }
}

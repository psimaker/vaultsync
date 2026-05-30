import SwiftUI

/// Shared brand palette. Single source of truth so the app and the widget
/// render the same accent colors instead of redefining the RGB per file.
extension Color {
    /// Brand teal — active / in-progress sync accent.
    static let vaultTeal = Color(red: 0 / 255, green: 137 / 255, blue: 123 / 255)
    /// Brand slate — muted/inactive accent.
    static let vaultSlate = Color(red: 38 / 255, green: 50 / 255, blue: 56 / 255)
}

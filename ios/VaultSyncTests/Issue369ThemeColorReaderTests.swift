import SwiftUI
import Testing
import UIKit
@testable import VaultSync

/// The contrast suites measure theme tokens through `ThemeColorReader`. On
/// iOS 18 the reader they used before gave every token's light value in every
/// appearance, so a dark or Increase Contrast pair was measured in light (#369).
/// This canary is built the way `vaultColor` builds a token — a Display-P3
/// provider with four values and its own alpha in light and dark — and the
/// reader must give back exactly those values in each appearance.
@Suite("Theme colors are read per appearance on every iOS the app supports (#369)")
struct Issue369ThemeColorReaderTests {
    struct Appearance: CustomStringConvertible, Sendable {
        let dark: Bool
        let increasedContrast: Bool
        /// Display-P3 channels, 0–255, and the alpha this appearance draws with.
        let channels: (CGFloat, CGFloat, CGFloat)
        let alpha: CGFloat

        var description: String {
            (dark ? "dark" : "light") + (increasedContrast ? " + Increase Contrast" : "")
        }

        var expected: UIColor {
            UIColor(displayP3Red: channels.0 / 255, green: channels.1 / 255, blue: channels.2 / 255, alpha: alpha)
        }
    }

    static let appearances = [
        Appearance(dark: false, increasedContrast: false, channels: (10, 120, 200), alpha: 1),
        Appearance(dark: true, increasedContrast: false, channels: (200, 60, 30), alpha: 0.6),
        Appearance(dark: false, increasedContrast: true, channels: (0, 70, 140), alpha: 1),
        Appearance(dark: true, increasedContrast: true, channels: (250, 200, 0), alpha: 0.6),
    ]

    static let canary = Color(uiColor: UIColor { traits in
        let dark = traits.userInterfaceStyle == .dark
        let increasedContrast = traits.accessibilityContrast == .high
        let appearance = appearances.first { $0.dark == dark && $0.increasedContrast == increasedContrast }!
        return appearance.expected
    })

    static func channels(_ color: UIColor) -> [Double] {
        var red: CGFloat = 0, green: CGFloat = 0, blue: CGFloat = 0, alpha: CGFloat = 0
        _ = color.getRed(&red, green: &green, blue: &blue, alpha: &alpha)
        return [red, green, blue, alpha].map(Double.init)
    }

    @Test("The reader gives the color's own value in each appearance", arguments: appearances)
    func readerResolvesEachAppearance(appearance: Appearance) {
        let read = Self.channels(ThemeColorReader.uiColor(
            Self.canary, dark: appearance.dark, increasedContrast: appearance.increasedContrast
        ))
        let expected = Self.channels(appearance.expected)
        for (readValue, expectedValue) in zip(read, expected) {
            #expect(abs(readValue - expectedValue) < 0.005, "\(appearance): read \(read), expected \(expected)")
        }
    }
}

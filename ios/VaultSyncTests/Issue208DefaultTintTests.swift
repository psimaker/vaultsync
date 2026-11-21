import SwiftUI
import Testing
import UIKit
@testable import VaultSync

/// The app's inherited tint colors text, so the `AccentColor` asset — which
/// system UI the `.tint` modifier does not reach (alert buttons) reads — must
/// be the text token, not the brand teal (#208).
@Suite("The accent color asset is the accessible text tint (#208)")
struct Issue208DefaultTintTests {
    struct Appearance: CustomStringConvertible, Sendable {
        let dark: Bool
        let increasedContrast: Bool

        var traits: UITraitCollection {
            UITraitCollection { traits in
                traits.userInterfaceStyle = dark ? .dark : .light
                traits.accessibilityContrast = increasedContrast ? .high : .normal
            }
        }

        var description: String {
            (dark ? "dark" : "light") + (increasedContrast ? " + Increase Contrast" : "")
        }
    }

    static let appearances = [
        Appearance(dark: false, increasedContrast: false),
        Appearance(dark: true, increasedContrast: false),
        Appearance(dark: false, increasedContrast: true),
        Appearance(dark: true, increasedContrast: true),
    ]

    static func channels(_ color: UIColor, _ appearance: Appearance) -> [Double] {
        let resolved = color.resolvedColor(with: appearance.traits)
        var red: CGFloat = 0, green: CGFloat = 0, blue: CGFloat = 0, alpha: CGFloat = 0
        _ = resolved.getRed(&red, green: &green, blue: &blue, alpha: &alpha)
        return [red, green, blue, alpha].map(Double.init)
    }

    @Test("AccentColor resolves to vaultAccentText", arguments: appearances)
    func assetMatchesTextToken(appearance: Appearance) throws {
        let asset = try #require(UIColor(named: "AccentColor", in: .main, compatibleWith: nil))
        let assetChannels = Self.channels(asset, appearance)
        // Read per appearance: converted to `UIColor` once, the token resolves
        // light in every appearance on iOS 18 (#369).
        let token = ThemeColorReader.uiColor(
            .vaultAccentText, dark: appearance.dark, increasedContrast: appearance.increasedContrast
        )
        let tokenChannels = Self.channels(token, appearance)
        for (assetValue, tokenValue) in zip(assetChannels, tokenChannels) {
            #expect(abs(assetValue - tokenValue) < 0.005, "\(appearance): asset \(assetChannels), token \(tokenChannels)")
        }
    }
}

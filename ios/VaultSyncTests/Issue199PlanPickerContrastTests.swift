import SwiftUI
import Testing
import UIKit
@testable import VaultSync

/// WCAG 2 contrast of two colors resolved for one appearance. Channels are
/// read in extended sRGB, so the Display-P3 tokens are compared in the same
/// space as the system colors.
enum WCAGContrast {
    struct Appearance: CustomStringConvertible, Sendable {
        let dark: Bool
        let increasedContrast: Bool

        static let all = [
            Appearance(dark: false, increasedContrast: false),
            Appearance(dark: true, increasedContrast: false),
            Appearance(dark: false, increasedContrast: true),
            Appearance(dark: true, increasedContrast: true),
        ]

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

    static func luminance(_ color: UIColor, in appearance: Appearance) -> Double {
        let resolved = color.resolvedColor(with: appearance.traits)
        var red: CGFloat = 0, green: CGFloat = 0, blue: CGFloat = 0, alpha: CGFloat = 0
        _ = resolved.getRed(&red, green: &green, blue: &blue, alpha: &alpha)
        precondition(alpha == 1, "contrast is only defined here for opaque colors")
        func linear(_ channel: CGFloat) -> Double {
            let value = Double(channel)
            let magnitude = abs(value)
            let mapped = magnitude <= 0.04045 ? magnitude / 12.92 : pow((magnitude + 0.055) / 1.055, 2.4)
            return value < 0 ? -mapped : mapped
        }
        return 0.2126 * linear(red) + 0.7152 * linear(green) + 0.0722 * linear(blue)
    }

    static func ratio(_ foreground: UIColor, on background: UIColor, in appearance: Appearance) -> Double {
        let first = luminance(foreground, in: appearance)
        let second = luminance(background, in: appearance)
        return (max(first, second) + 0.05) / (min(first, second) + 0.05)
    }
}

@Suite("Plan picker text clears WCAG AA in every appearance (#199)")
struct Issue199PlanPickerContrastTests {
    struct Pair: CustomStringConvertible, Sendable {
        let name: String
        let foreground: Color
        let background: Color
        var description: String { name }
    }

    /// Every text/background pair the picker draws. The plan cards sit on
    /// `secondarySystemBackground` inside a `vaultSurface` card.
    static let pairs: [Pair] = [
        Pair(name: "Subscribe label on its fill", foreground: .vaultOnAccent, background: .vaultAccentProminent),
        Pair(name: "Restore Purchases on the card", foreground: .vaultAccentText, background: .vaultSurface),
        Pair(name: "Dismiss on the plan-card surface", foreground: .vaultAccentText, background: Color(uiColor: .secondarySystemBackground)),
        Pair(name: "Price on a plan card", foreground: .vaultSecondaryLabel, background: Color(uiColor: .secondarySystemBackground)),
        Pair(name: "Disclosure and notices on the card", foreground: .vaultSecondaryLabel, background: .vaultSurface),
        Pair(name: "Storage error on the card", foreground: .statusErrorText, background: .vaultSurface),
        Pair(name: "Plan title on a plan card", foreground: .vaultLabel, background: Color(uiColor: .secondarySystemBackground)),
    ]

    @Test("Each pair reaches 4.5:1", arguments: pairs, WCAGContrast.Appearance.all)
    func pairClearsAA(pair: Pair, appearance: WCAGContrast.Appearance) {
        let ratio = WCAGContrast.ratio(UIColor(pair.foreground), on: UIColor(pair.background), in: appearance)
        #expect(ratio >= 4.5, "\(pair.name) in \(appearance): \(ratio)")
    }

    @Test("The savings tag picks the text color with more contrast, Increase Contrast included", arguments: WCAGContrast.Appearance.all)
    func savingsTagTextClearsAA(appearance: WCAGContrast.Appearance) {
        let black = StatusTag.prefersBlackText(
            on: .vaultAccent,
            dark: appearance.dark,
            increasedContrast: appearance.increasedContrast
        )
        let text: UIColor = black ? .black : .white
        let ratio = WCAGContrast.ratio(text, on: UIColor(Color.vaultAccent), in: appearance)
        #expect(ratio >= 4.5, "savings tag in \(appearance): \(ratio)")
    }

    @Test("Under Increase Contrast the deeper light teal takes white text")
    func increasedContrastLightTealTakesWhite() {
        // Resolved without the contrast trait, the tag chose black for the
        // normal teal and kept it on the deeper teal: 3.4:1.
        #expect(StatusTag.prefersBlackText(on: .vaultAccent, dark: false, increasedContrast: true) == false)
        #expect(StatusTag.prefersBlackText(on: .vaultAccent, dark: false, increasedContrast: false) == true)
    }
}

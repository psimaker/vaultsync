import SwiftUI
import Testing
import UIKit
@testable import VaultSync

/// WCAG 2 contrast of colors resolved for one appearance. Channels are read
/// in extended sRGB, so the Display-P3 tokens are compared in the same space
/// as the system colors; a translucent color is composited onto what lies
/// beneath it first, the way it is drawn.
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

    typealias RGBA = (red: Double, green: Double, blue: Double, alpha: Double)

    static func resolve(_ color: UIColor, in appearance: Appearance) -> RGBA {
        let resolved = color.resolvedColor(with: appearance.traits)
        var red: CGFloat = 0, green: CGFloat = 0, blue: CGFloat = 0, alpha: CGFloat = 0
        _ = resolved.getRed(&red, green: &green, blue: &blue, alpha: &alpha)
        return (Double(red), Double(green), Double(blue), Double(alpha))
    }

    /// Source-over compositing of `top` onto an opaque `bottom`.
    static func composite(_ top: RGBA, over bottom: RGBA) -> RGBA {
        let alpha = top.alpha
        return (
            top.red * alpha + bottom.red * (1 - alpha),
            top.green * alpha + bottom.green * (1 - alpha),
            top.blue * alpha + bottom.blue * (1 - alpha),
            1
        )
    }

    static func luminance(_ color: RGBA) -> Double {
        func linear(_ value: Double) -> Double {
            let magnitude = abs(value)
            let mapped = magnitude <= 0.04045 ? magnitude / 12.92 : pow((magnitude + 0.055) / 1.055, 2.4)
            return value < 0 ? -mapped : mapped
        }
        return 0.2126 * linear(color.red) + 0.7152 * linear(color.green) + 0.0722 * linear(color.blue)
    }

    /// Contrast of `foreground` drawn on `background`, which is drawn on the
    /// opaque `base` (only consulted when `background` is translucent).
    static func ratio(
        _ foreground: UIColor,
        on background: UIColor,
        over base: UIColor = .white,
        in appearance: Appearance
    ) -> Double {
        let ground = composite(resolve(background, in: appearance), over: resolve(base, in: appearance))
        let ink = composite(resolve(foreground, in: appearance), over: ground)
        let first = luminance(ink)
        let second = luminance(ground)
        return (max(first, second) + 0.05) / (min(first, second) + 0.05)
    }
}

@Suite("Plan picker text clears WCAG AA in every appearance (#199)")
struct Issue199PlanPickerContrastTests {
    struct Pair: CustomStringConvertible, Sendable {
        let name: String
        let foreground: UIColor
        let background: UIColor
        /// What lies beneath a translucent background.
        var base: UIColor = UIColor(Color.vaultSurface)
        var description: String { name }
    }

    /// The picker sits in a `vaultSurface` card; the plan cards and the
    /// Ask to Buy notice on `secondarySystemBackground` inside it.
    static let surface = UIColor(Color.vaultSurface)
    static let planCard = UIColor.secondarySystemBackground

    /// Every text/background pair the picker draws.
    static let pairs: [Pair] = [
        Pair(name: "Subscribe on its fill", foreground: UIColor(Color.vaultOnAccent), background: UIColor(Color.vaultAccentProminent)),
        Pair(name: "Savings tag on its fill", foreground: UIColor(Color.vaultOnAccent), background: UIColor(Color.vaultAccentProminent)),
        Pair(name: "Try Again on its wash", foreground: UIColor(Color.vaultAccentText), background: UIColor(Color.vaultAccentFill)),
        Pair(name: "Restore Purchases on the card", foreground: UIColor(Color.vaultAccentText), background: surface),
        Pair(name: "Dismiss on the notice", foreground: UIColor(Color.vaultAccentText), background: planCard),
        Pair(name: "Plan title on a plan card", foreground: .label, background: planCard),
        Pair(name: "Price on a plan card", foreground: UIColor(Color.vaultSecondaryLabel), background: planCard),
        Pair(name: "Notice headline on the card", foreground: .label, background: surface),
        Pair(name: "Notice copy on the notice", foreground: UIColor(Color.vaultSecondaryLabel), background: planCard),
        Pair(name: "Disclosure and loading text on the card", foreground: UIColor(Color.vaultSecondaryLabel), background: surface),
        Pair(name: "Storage error on the card", foreground: UIColor(Color.statusErrorText), background: surface),
        Pair(name: "Terms and Privacy links on the card", foreground: UIColor(Color.vaultAccentText), background: surface),
    ]

    @Test("Each text pair reaches 4.5:1", arguments: pairs, WCAGContrast.Appearance.all)
    func pairClearsAA(pair: Pair, appearance: WCAGContrast.Appearance) {
        let ratio = WCAGContrast.ratio(pair.foreground, on: pair.background, over: pair.base, in: appearance)
        #expect(ratio >= 4.5, "\(pair.name) in \(appearance): \(ratio)")
    }

    @Test("The purchase spinner stays visible on the dimmed Subscribe button", arguments: WCAGContrast.Appearance.all)
    func spinnerVisibleWhileDimmed(appearance: WCAGContrast.Appearance) {
        // VaultButtonStyle draws a disabled button at 40 % opacity; the
        // spinner sits above it, undimmed. A graphic needs 3:1 (WCAG 1.4.11).
        var fill = WCAGContrast.resolve(UIColor(Color.vaultAccentProminent), in: appearance)
        fill.alpha = 0.4
        let ground = WCAGContrast.composite(fill, over: WCAGContrast.resolve(Self.surface, in: appearance))
        let spinner = WCAGContrast.resolve(UIColor(Color.vaultLabel), in: appearance)
        let (first, second) = (WCAGContrast.luminance(spinner), WCAGContrast.luminance(ground))
        let ratio = (max(first, second) + 0.05) / (min(first, second) + 0.05)
        #expect(ratio >= 3, "spinner in \(appearance): \(ratio)")
    }
}

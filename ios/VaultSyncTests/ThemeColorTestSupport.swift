import SwiftUI
import UIKit

/// Reads a SwiftUI color as it resolves in one appearance, for tests that
/// measure theme tokens (#369).
///
/// A token is `Color(uiColor: UIColor { traits in … })` (`Resources/Theme.swift`).
/// From iOS 26 the round trip through `UIColor.init(_: Color)` keeps that
/// provider, so the result resolves per trait collection. On iOS 18 it does
/// not: the round trip gives the light value in every appearance, also when
/// the conversion runs inside `performAsCurrent`. There `Color.resolve(in:)`
/// is the way, with the appearance set on the environment — but on iOS 26
/// that path ignores the contrast setting, so neither path serves both
/// systems. The canary in `Issue369ThemeColorReaderTests` holds both branches
/// against a color whose values the test knows; the iOS 18 branch goes when
/// the deployment floor leaves iOS 18.
enum ThemeColorReader {
    static func traits(dark: Bool, increasedContrast: Bool) -> UITraitCollection {
        UITraitCollection { traits in
            traits.userInterfaceStyle = dark ? .dark : .light
            traits.accessibilityContrast = increasedContrast ? .high : .normal
        }
    }

    /// The color as drawn in that appearance, as a static `UIColor` whose
    /// channels read in extended sRGB.
    static func uiColor(_ color: Color, dark: Bool, increasedContrast: Bool) -> UIColor {
        let traits = traits(dark: dark, increasedContrast: increasedContrast)
        if #available(iOS 26, *) {
            return UIColor(color).resolvedColor(with: traits)
        }
        var environment = EnvironmentValues()
        environment.colorScheme = dark ? .dark : .light
        // Underscored, but the only way to set the contrast of a resolve on
        // iOS 18; a later SDK that drops it breaks the build, not the result.
        environment._colorSchemeContrast = increasedContrast ? .increased : .standard
        let resolved = color.resolve(in: environment)
        return UIColor(
            red: CGFloat(resolved.red),
            green: CGFloat(resolved.green),
            blue: CGFloat(resolved.blue),
            alpha: CGFloat(resolved.opacity)
        )
    }
}

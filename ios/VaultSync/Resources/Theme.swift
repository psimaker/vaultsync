import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// MARK: - Design Tokens
//
// Single source of truth for the VaultSync visual language, compiled into BOTH
// the app and the widget target (see project.yml). Because it is shared with the
// widget extension it must NOT reference app-only symbols such as `L10n`; use
// `String(localized:)` for any user-facing text so each target resolves strings
// from its own bundle.
//
// Colors are built as dynamic Display-P3 `UIColor`s so light/dark (and the
// Increase-Contrast accessibility setting) resolve automatically — this retires
// the hand-rolled `colorScheme == .dark ? … : …` opacity math that used to live
// in the views.

#if canImport(UIKit)
/// A Display-P3 color that resolves light/dark and optional increased-contrast
/// variants from the active trait collection. Channels are 0–255 for legibility.
/// The optional alphas produce muted *fills* (icon chips, decorative washes)
/// without per-call-site `colorScheme == .dark ? … : …` opacity math.
private func vaultColor(
    light: (CGFloat, CGFloat, CGFloat),
    dark: (CGFloat, CGFloat, CGFloat),
    lightHC: (CGFloat, CGFloat, CGFloat)? = nil,
    darkHC: (CGFloat, CGFloat, CGFloat)? = nil,
    lightAlpha: CGFloat = 1,
    darkAlpha: CGFloat = 1
) -> Color {
    Color(uiColor: UIColor { traits in
        let highContrast = traits.accessibilityContrast == .high
        let channels: (CGFloat, CGFloat, CGFloat)
        switch (traits.userInterfaceStyle, highContrast) {
        case (.dark, true): channels = darkHC ?? dark
        case (.dark, false): channels = dark
        case (_, true): channels = lightHC ?? light
        default: channels = light
        }
        return UIColor(
            displayP3Red: channels.0 / 255,
            green: channels.1 / 255,
            blue: channels.2 / 255,
            alpha: traits.userInterfaceStyle == .dark ? darkAlpha : lightAlpha
        )
    })
}
#else
private func vaultColor(
    light: (CGFloat, CGFloat, CGFloat),
    dark: (CGFloat, CGFloat, CGFloat),
    lightHC: (CGFloat, CGFloat, CGFloat)? = nil,
    darkHC: (CGFloat, CGFloat, CGFloat)? = nil,
    lightAlpha: CGFloat = 1,
    darkAlpha: CGFloat = 1
) -> Color {
    Color(red: light.0 / 255, green: light.1 / 255, blue: light.2 / 255)
        .opacity(lightAlpha)
}
#endif

// MARK: - Brand palette

extension Color {
    /// Primary interactive / affirmative-active brand accent. This is the single
    /// app-wide tint (also mirrored in `AccentColor` so the asset-catalog global
    /// accent matches). Used for links, selection, primary buttons, "syncing".
    static let vaultAccent = vaultColor(
        light: (0, 137, 123),       // #00897B — the established brand teal, P3-tuned
        dark: (38, 196, 176),       // lifted so it stays vivid on a dark canvas
        lightHC: (0, 110, 99),
        darkHC: (74, 222, 202)
    )

    /// Brand teal — kept as the historical name so existing call sites keep
    /// working, now dark-aware. Identical to `vaultAccent`.
    static let vaultTeal = Color.vaultAccent

    /// Deep neutral slate, used for muted fills/surfaces. Dark-aware so fills no
    /// longer need per-call-site opacity math.
    static let vaultSlate = vaultColor(
        light: (38, 50, 56),        // #263238
        dark: (176, 190, 197)       // #B0BEC5 — readable as a muted accent in dark
    )

    // Muted decorative fills. These retire the last of the hand-rolled
    // `colorScheme == .dark ? 0.22 : 0.14` opacity math (onboarding chips, page
    // dots, blurred background circles). Two emphasis levels per hue are enough.

    /// Brand-accent wash for icon chips and selected decorations.
    static let vaultAccentFill = vaultColor(
        light: (0, 137, 123), dark: (38, 196, 176),
        lightAlpha: 0.14, darkAlpha: 0.22
    )
    /// Fainter accent wash for large background decorations.
    static let vaultAccentFillSubtle = vaultColor(
        light: (0, 137, 123), dark: (38, 196, 176),
        lightAlpha: 0.08, darkAlpha: 0.14
    )
    /// Neutral slate wash for inactive decorations (page dots, accent bars).
    static let vaultSlateFill = vaultColor(
        light: (38, 50, 56), dark: (176, 190, 197),
        lightAlpha: 0.20, darkAlpha: 0.34
    )
    /// Fainter slate wash for large background decorations.
    static let vaultSlateFillSubtle = vaultColor(
        light: (38, 50, 56), dark: (176, 190, 197),
        lightAlpha: 0.05, darkAlpha: 0.10
    )

    /// Hairline stroke for card borders — the system separator, tuned per scheme
    /// so it reads on elevated cards in dark without dominating in light.
    static let vaultHairline: Color = {
        #if canImport(UIKit)
        Color(uiColor: UIColor { traits in
            UIColor.separator.withAlphaComponent(
                traits.userInterfaceStyle == .dark ? 0.45 : 0.18
            )
        })
        #else
        Color.gray.opacity(0.18)
        #endif
    }()
}

// MARK: - Typography

extension Font {
    /// Monospaced rendering for genuine machine strings — Device IDs, paths,
    /// `.stignore` globs, file contents. One call site to change instead of the
    /// `.system(_, design: .monospaced)` literals that were scattered per view.
    static func vaultMono(_ style: Font.TextStyle = .footnote, weight: Font.Weight = .regular) -> Font {
        .system(style, design: .monospaced).weight(weight)
    }
}

// MARK: - Semantic status palette
//
// Six pinned meanings, each ALWAYS paired with a symbol + text label by the
// `SyncStatus` registry so status is never conveyed by color alone.

extension Color {
    /// Idle / all-synced / connected.
    static let statusSuccess = vaultColor(light: (46, 158, 107), dark: (52, 199, 127))
    /// Active transfer in progress (alias of the brand accent).
    static let statusSyncing = Color.vaultAccent
    /// Transient "starting/preparing" — a calm blue so it is never mistaken for
    /// an error (today it is wrongly conflated with attention/orange).
    static let statusStarting = vaultColor(light: (78, 124, 168), dark: (127, 168, 208))
    /// Warning / action-needed (conflicts, pending shares, setup gaps).
    static let statusAttention = vaultColor(light: (224, 146, 47), dark: (242, 169, 59))
    /// Error / unreachable — reserved for genuine failures.
    static let statusError = vaultColor(light: (210, 69, 59), dark: (232, 92, 82))
    /// Informational / shared-with — replaces the off-brand system blue used for
    /// "Shared With" checkmarks.
    static let statusInfo = vaultColor(light: (78, 111, 181), dark: (110, 143, 216))
    /// Paused / offline / inactive.
    static let statusInactive = Color.secondary
}

// MARK: - Spacing & radius scale

/// 8pt soft grid. Replaces the 14-value padding literal soup.
enum VaultSpacing {
    static let xxs: CGFloat = 2
    static let xs: CGFloat = 4
    static let s: CGFloat = 8
    static let m: CGFloat = 12
    static let l: CGFloat = 16
    static let xl: CGFloat = 24
}

/// Continuous corner radii. Replaces the 8/10/11/12/14/22/24/28 spread.
enum VaultRadius {
    static let control: CGFloat = 12
    static let card: CGFloat = 16
    static let hero: CGFloat = 28
}

// MARK: - Sync status registry
//
// One canonical status type keyed by genuine sync state. Maps to a symbol, a
// semantic color, and a localized label. The widget decodes its stringly-typed
// snapshot through `fromWire(_:)` so an unknown value maps to `.attention`
// (NEVER silently to "all good"), closing the documented widget-lies bug.

enum SyncStatus: String, Sendable, CaseIterable {
    case synced
    case syncing
    case starting
    case attention
    case error
    case paused

    /// Decode the app↔widget wire-format status string. Unknown → `.attention`.
    static func fromWire(_ raw: String) -> SyncStatus {
        switch raw.lowercased() {
        case "idle", "synced", "ok": return .synced
        case "syncing", "scanning": return .syncing
        case "starting", "preparing": return .starting
        case "attention", "warning", "warn": return .attention
        case "error", "failed": return .error
        case "paused", "inactive", "offline": return .paused
        default: return .attention
        }
    }

    /// Stable wire string for persisting into the shared snapshot.
    var wireValue: String { rawValue }

    var symbolName: String {
        switch self {
        case .synced: return "checkmark.circle.fill"
        case .syncing: return "arrow.triangle.2.circlepath"
        case .starting: return "hourglass"
        case .attention: return "exclamationmark.triangle.fill"
        case .error: return "xmark.octagon.fill"
        case .paused: return "pause.circle.fill"
        }
    }

    var tint: Color {
        switch self {
        case .synced: return .statusSuccess
        case .syncing: return .statusSyncing
        case .starting: return .statusStarting
        case .attention: return .statusAttention
        case .error: return .statusError
        case .paused: return .statusInactive
        }
    }

    /// Localized one-word/short label. Resolved from each target's own bundle.
    var label: String {
        switch self {
        case .synced: return String(localized: "All Synced")
        case .syncing: return String(localized: "Syncing")
        case .starting: return String(localized: "Starting")
        case .attention: return String(localized: "Needs Attention")
        case .error: return String(localized: "Sync Error")
        case .paused: return String(localized: "Paused")
        }
    }

    /// True for states that should draw the user's attention (used for ordering
    /// and for animating the symbol).
    var isUrgent: Bool { self == .attention || self == .error }
}

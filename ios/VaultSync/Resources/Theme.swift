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

    // Muted fill: retires the hand-rolled `colorScheme == .dark ? 0.22 : 0.14`
    // opacity math the views used to carry.

    /// Brand-accent wash for icon wells, tinted buttons and selected
    /// decorations.
    static let vaultAccentFill = vaultColor(
        light: (0, 137, 123), dark: (38, 196, 176),
        lightAlpha: 0.12, darkAlpha: 0.20
    )

    /// Hairline stroke for card borders: slate at low alpha, so the border
    /// carries the palette's cool tint instead of the neutral system separator.
    /// The alphas follow the approved #187 canvas (Direction A).
    static let vaultHairline = vaultColor(
        light: (38, 50, 56), dark: (176, 190, 197),
        lightAlpha: 0.12, darkAlpha: 0.16
    )
}

// MARK: - Surfaces & text (#187)
//
// The fresh design's canvas: a cool off-white page, white cards, slate text.
// Screens built on `VaultPage` use these instead of the system grouped
// backgrounds so light and dark keep the same slate tint everywhere.

extension Color {
    /// Page background behind cards.
    static let vaultBackground = vaultColor(
        light: (245, 247, 247),     // #F5F7F7
        dark: (15, 20, 22)          // #0F1416
    )
    /// Card / row surface.
    static let vaultSurface = vaultColor(
        light: (255, 255, 255),
        dark: (23, 29, 32)          // #171D20
    )
    /// Primary text on cards.
    static let vaultLabel = vaultColor(
        light: (27, 35, 38),        // #1B2326
        dark: (232, 237, 239),      // #E8EDEF
        lightHC: (0, 0, 0),
        darkHC: (255, 255, 255)
    )
    /// Secondary text (subtitles, captions, section headers). 5.5:1 on white
    /// and 5.1:1 on the page background — the system secondary label measures
    /// 3.4:1 on white, which is why the cards do not use it.
    static let vaultSecondaryLabel = vaultColor(
        light: (91, 107, 114),      // #5B6B72
        dark: (152, 169, 176),      // #98A9B0
        lightHC: (60, 72, 78),
        darkHC: (190, 203, 209)
    )
    /// Neutral wash for secondary buttons and icon wells.
    static let vaultNeutralFill = vaultColor(
        light: (38, 50, 56), dark: (176, 190, 197),
        lightAlpha: 0.06, darkAlpha: 0.10
    )
    /// Accent-colored TEXT (links, tinted buttons, row actions). The brand
    /// teal itself measures 4.32:1 on white — fine for glyphs and fills, short
    /// of WCAG AA for 15–17pt text — so text uses this deeper light variant
    /// (6.2:1 on white, 5.3:1 on `vaultAccentFill`).
    static let vaultAccentText = vaultColor(
        light: (0, 110, 99),
        dark: (38, 196, 176),
        lightHC: (0, 88, 79),
        darkHC: (74, 222, 202)
    )
    /// Fill of the primary button. One step deeper than the brand teal in
    /// light so white label text clears 4.5:1 (4.84:1; the brand teal gives
    /// 4.32:1); dark keeps the lifted teal under `vaultOnAccent` text (7.7:1).
    static let vaultAccentProminent = vaultColor(
        light: (0, 128, 115),
        dark: (38, 196, 176),
        lightHC: (0, 110, 99),
        darkHC: (74, 222, 202)
    )
    /// Text and glyphs on a `vaultAccentProminent` fill.
    static let vaultOnAccent = vaultColor(
        light: (255, 255, 255),
        dark: (7, 33, 29)           // #07211D
    )
}

// MARK: - Typography

extension Font {
    /// Monospaced rendering for genuine machine strings — Device IDs, paths,
    /// `.stignore` globs, file contents. One call site to change instead of the
    /// `.system(_, design: .monospaced)` literals that were scattered per view.
    static func vaultMono(_ style: Font.TextStyle = .footnote, weight: Font.Weight = .regular) -> Font {
        .system(style, design: .monospaced).weight(weight)
    }

    // The #187 type scale. All Dynamic Type text styles, so every size grows
    // with the user's setting: 34 large title (navigation), 22 hero, 17 row
    // title and button, 15 body copy, 13 subtitles / chips / section headers.

    /// Title of the status hero card.
    static let vaultHeroTitle = Font.title2.weight(.bold)
    /// Uppercase header above a card group.
    static let vaultSectionHeader = Font.footnote.weight(.semibold)
    /// Chip and pill text.
    static let vaultChip = Font.footnote.weight(.semibold)
    /// Button labels.
    static let vaultButton = Font.body.weight(.semibold)
}

// MARK: - Semantic status palette
//
// Six pinned meanings, each ALWAYS paired with a symbol + text label by the
// `SyncStatus` registry so status is never conveyed by color alone.
//
// These tokens color body/caption TEXT (not just glyphs), so the light values
// must clear WCAG 4.5:1 on white; each token also carries lightHC/darkHC
// variants so the system Increase Contrast setting actually increases
// contrast (#68). statusSyncing aliases vaultAccent (which has its own HC
// pair); statusInactive is the system secondary color, which adapts on its own.

extension Color {
    /// Idle / all-synced / connected.
    /// Light green is #1A784E (5.47:1 on white) — the previous (46, 158, 107)
    /// measured 3.38:1 and colors the device-count text (#74, same class as
    /// #68's amber). The HC variant deepens to 7.70:1 so Increase Contrast
    /// still increases it.
    static let statusSuccess = vaultColor(
        light: (26, 120, 78), dark: (52, 199, 127),
        lightHC: (17, 95, 60), darkHC: (94, 222, 158)
    )
    /// Active transfer in progress (alias of the brand accent).
    static let statusSyncing = Color.vaultAccent
    /// Transient "starting/preparing" — a calm blue so it is never mistaken for
    /// an error (today it is wrongly conflated with attention/orange).
    static let statusStarting = vaultColor(
        light: (78, 124, 168), dark: (127, 168, 208),
        lightHC: (50, 92, 133), darkHC: (162, 197, 235)
    )
    /// Warning / action-needed (conflicts, pending shares, setup gaps).
    /// Light amber is #A06400 (4.86:1 on white) — the previous (224, 146, 47)
    /// measured 2.52:1 and was used for failure-message text (#68).
    static let statusAttention = vaultColor(
        light: (160, 100, 0), dark: (242, 169, 59),
        lightHC: (122, 76, 0), darkHC: (255, 193, 101)
    )
    /// Error / unreachable — reserved for genuine failures.
    static let statusError = vaultColor(
        light: (210, 69, 59), dark: (232, 92, 82),
        lightHC: (166, 42, 34), darkHC: (255, 128, 118)
    )
    /// Informational / shared-with — replaces the off-brand system blue used for
    /// "Shared With" checkmarks.
    static let statusInfo = vaultColor(
        light: (78, 111, 181), dark: (110, 143, 216),
        lightHC: (52, 82, 148), darkHC: (150, 178, 240)
    )
    /// Paused / offline / inactive.
    static let statusInactive = Color.secondary
}

// MARK: - Status washes and text on washes (#187)
//
// Chips, tinted cards and the destructive button put status TEXT on that
// status's own 12–18 % wash. The plain status colors drop to 3.8–4.2:1 there
// in light mode (and error/info in dark), so text on a wash uses these
// deeper (light) / brighter (dark) variants; every pair clears 4.8:1, and
// the HC variants go further for Increase Contrast.

extension Color {
    static let statusSuccessFill = vaultColor(
        light: (26, 120, 78), dark: (52, 199, 127),
        lightAlpha: 0.12, darkAlpha: 0.18
    )
    static let statusStartingFill = vaultColor(
        light: (78, 124, 168), dark: (127, 168, 208),
        lightAlpha: 0.12, darkAlpha: 0.18
    )
    static let statusAttentionFill = vaultColor(
        light: (160, 100, 0), dark: (242, 169, 59),
        lightAlpha: 0.12, darkAlpha: 0.18
    )
    static let statusErrorFill = vaultColor(
        light: (210, 69, 59), dark: (232, 92, 82),
        lightAlpha: 0.12, darkAlpha: 0.18
    )
    static let statusInfoFill = vaultColor(
        light: (78, 111, 181), dark: (110, 143, 216),
        lightAlpha: 0.12, darkAlpha: 0.18
    )

    static let statusSuccessText = vaultColor(
        light: (17, 95, 60), dark: (52, 199, 127),
        lightHC: (12, 78, 48), darkHC: (94, 222, 158)
    )
    static let statusStartingText = vaultColor(
        light: (50, 92, 133), dark: (127, 168, 208),
        lightHC: (38, 74, 110), darkHC: (162, 197, 235)
    )
    static let statusAttentionText = vaultColor(
        light: (122, 76, 0), dark: (242, 169, 59),
        lightHC: (100, 62, 0), darkHC: (255, 193, 101)
    )
    static let statusErrorText = vaultColor(
        light: (166, 42, 34), dark: (255, 128, 118),
        lightHC: (140, 32, 26), darkHC: (255, 152, 143)
    )
    static let statusInfoText = vaultColor(
        light: (52, 82, 148), dark: (150, 178, 240),
        lightHC: (40, 66, 128), darkHC: (178, 200, 248)
    )
}

// MARK: - Tones (#187)

/// The color role of a chip, tinted card, icon well or button: which glyph
/// color, which wash, and which text-on-wash color belong together, so no
/// call site can pair a wash with a text color that fails contrast on it.
/// Status views map through `SyncStatus.tone`; everything else (an accent
/// "Cloud Relay active" chip, a neutral vault count) picks a tone directly.
enum VaultTone: Sendable {
    case neutral
    case accent
    case success
    case starting
    case attention
    case error
    case info

    /// Glyphs and dots.
    var tint: Color {
        switch self {
        case .neutral: return .vaultSecondaryLabel
        case .accent: return .vaultAccent
        case .success: return .statusSuccess
        case .starting: return .statusStarting
        case .attention: return .statusAttention
        case .error: return .statusError
        case .info: return .statusInfo
        }
    }

    /// The tone's wash.
    var fill: Color {
        switch self {
        case .neutral: return .vaultNeutralFill
        case .accent: return .vaultAccentFill
        case .success: return .statusSuccessFill
        case .starting: return .statusStartingFill
        case .attention: return .statusAttentionFill
        case .error: return .statusErrorFill
        case .info: return .statusInfoFill
        }
    }

    /// Text placed on `fill`.
    var text: Color {
        switch self {
        case .neutral: return .vaultLabel
        case .accent: return .vaultAccentText
        case .success: return .statusSuccessText
        case .starting: return .statusStartingText
        case .attention: return .statusAttentionText
        case .error: return .statusErrorText
        case .info: return .statusInfoText
        }
    }
}

// MARK: - Spacing & radius scale

/// 8pt soft grid. Replaces the 14-value padding literal soup.
enum VaultSpacing {
    static let xxs: CGFloat = 2
    static let xs: CGFloat = 4
    static let s: CGFloat = 8
    static let m: CGFloat = 12
    static let l: CGFloat = 16
    /// Page side margin and hero-card padding (#187 canvas).
    static let gutter: CGFloat = 20
    static let xl: CGFloat = 24
}

/// Continuous corner radii. Replaces the 8/10/11/12/14/22/24/28 spread.
enum VaultRadius {
    static let control: CGFloat = 12
    static let button: CGFloat = 14
    static let card: CGFloat = 16
    static let hero: CGFloat = 28
}

/// Minimum sizes of the #187 components. Minimums, not fixed heights: every
/// one of them grows with Dynamic Type.
enum VaultMetrics {
    /// List-row minimum height inside a card.
    static let rowMinHeight: CGFloat = 60
    /// Full-width primary / secondary button.
    static let buttonHeight: CGFloat = 50
    /// Compact button (inline actions) — still the 44pt touch minimum.
    static let compactButtonHeight: CGFloat = 44
    /// Round status well in the hero card.
    static let heroIconSize: CGFloat = 44
    /// Status dot at the end of a row.
    static let statusDotSize: CGFloat = 10
    /// Readable column width on iPad; cards never stretch past it.
    static let readableWidth: CGFloat = 640
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

    /// Color role of this status for chips, wells and tinted cards.
    var tone: VaultTone {
        switch self {
        case .synced: return .success
        case .syncing: return .accent
        case .starting: return .starting
        case .attention: return .attention
        case .error: return .error
        case .paused: return .neutral
        }
    }

    /// The status's own wash — hero icon well, chips, tinted cards.
    var fill: Color { tone.fill }

    /// Text color for the status ON its `fill` (chips, pills) — see the
    /// "text on washes" tokens for why this is not `tint`.
    var textOnFill: Color { tone.text }

    /// Bare glyph for a round status well (the hero card), where the
    /// `.circle.fill` variants of `symbolName` would draw a circle in a circle.
    var wellSymbolName: String {
        switch self {
        case .synced: return "checkmark"
        case .syncing: return "arrow.triangle.2.circlepath"
        case .starting: return "hourglass"
        case .attention: return "exclamationmark"
        case .error: return "xmark"
        case .paused: return "pause.fill"
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

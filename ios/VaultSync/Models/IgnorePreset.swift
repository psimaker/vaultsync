import Foundation

/// A named group of `.stignore` patterns surfaced as a single toggle in the
/// Sync Filters UI. The label/description are L10n keys (English-sentence form,
/// matching the existing Localizable.strings convention).
struct IgnorePreset: Identifiable, Sendable, Hashable {
    let id: String
    let label: String
    let description: String
    let patterns: [String]
}

extension IgnorePreset {
    static let workspace = IgnorePreset(
        id: "workspace",
        label: "Workspace state",
        description: "Prevents sync conflicts on which notes were open.",
        patterns: [".obsidian/workspace.json", ".obsidian/workspace-mobile.json"]
    )

    static let trash = IgnorePreset(
        id: "trash",
        label: "Trash",
        description: "Files already deleted on other devices.",
        patterns: [".Trash"]
    )

    static let git = IgnorePreset(
        id: "git",
        label: "Git repository",
        description: "Version history — rarely useful on iPhone.",
        patterns: [".git"]
    )

    static let macos = IgnorePreset(
        id: "macos",
        label: "macOS metadata",
        description: "Finder metadata files like .DS_Store.",
        patterns: [".DS_Store", "._*"]
    )

    static let copilot = IgnorePreset(
        id: "copilot",
        label: "Copilot index",
        description: "Cache of Obsidian Copilot, regenerated automatically.",
        patterns: [".copilot-index"]
    )

    static let obsidianCache = IgnorePreset(
        id: "obsidianCache",
        label: "Obsidian app cache",
        description: "Auto-regenerated Obsidian internal cache.",
        patterns: [".obsidian/cache"]
    )

    /// Pre-checked in the first-run sheet for new vaults.
    static let recommended: [IgnorePreset] = [.workspace, .trash]

    /// Order shown in `IgnorePatternsView` under "Other presets".
    static let all: [IgnorePreset] = [
        .workspace, .trash, .git, .macos, .copilot, .obsidianCache,
    ]

    /// Map a detected scan pattern (e.g. ".git") back to its preset, if any.
    static func preset(forDetectedPattern pattern: String) -> IgnorePreset? {
        all.first { $0.patterns.contains(pattern) }
    }
}

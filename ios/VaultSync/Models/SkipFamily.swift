import Foundation

// MARK: - Skip Family grouping

/// A logical entry in the Sync Filters "Custom patterns" list. Each entry is
/// either a singleton pattern or a paired `<X>` + `<X>.sync-conflict-*`.
struct SkipFamilyEntry: Hashable {
    /// The "presented" line — for paired entries this is the original-file
    /// pattern, for singletons it is the pattern itself.
    let original: String
    /// True iff a matching `<original-stem>.sync-conflict-*` glob also exists
    /// in `.stignore` and should be removed together with the original.
    let hasConflictGlob: Bool

    /// All `.stignore` lines this entry represents — one line for singletons,
    /// two for paired entries.
    var underlyingLines: [String] {
        if hasConflictGlob {
            return [original, SyncthingManager.conflictGlob(forOriginalPath: original)]
        }
        return [original]
    }
}

enum SkipFamilyGrouping {
    /// Returns true iff `line` looks like a concrete file path that the Skip
    /// flow could plausibly have written. Excludes wildcard patterns (`*`,
    /// `?`, `[…]`, `{…}`, `!`) and directory rules (trailing `/`). Used to
    /// avoid spurious pairing of custom user patterns whose derived conflict
    /// glob happens to match another entry by coincidence.
    private static func isConcreteFilePathPattern(_ line: String) -> Bool {
        if line.hasSuffix("/") { return false }
        let glob = CharacterSet(charactersIn: "*?[]{}!")
        return line.rangeOfCharacter(from: glob) == nil
    }

    /// Group raw `.stignore` custom-section lines into Skip-Family entries.
    /// A line `X` is paired with `<dir>/<stem>.sync-conflict-*` if both exist
    /// in the input AND `X` is a concrete file path (no wildcards, no
    /// trailing slash). Orphan conflict globs render as their own singleton
    /// row.
    static func group(customLines: [String]) -> [SkipFamilyEntry] {
        let lineSet = Set(customLines)
        // Pre-pass: any line that is the conflict-glob of some OTHER concrete
        // file path in the input is "owned" by that original — skip it
        // during emission so the pair is detected regardless of order.
        var pairedGlobs = Set<String>()
        for line in customLines {
            guard isConcreteFilePathPattern(line) else { continue }
            let glob = SyncthingManager.conflictGlob(forOriginalPath: line)
            if line != glob, lineSet.contains(glob) {
                pairedGlobs.insert(glob)
            }
        }
        var consumed = Set<String>()
        var result: [SkipFamilyEntry] = []

        for line in customLines {
            if consumed.contains(line) { continue }
            // A glob with a matching original elsewhere in the input is
            // emitted via that original — never as its own row.
            if pairedGlobs.contains(line) { continue }
            let glob = SyncthingManager.conflictGlob(forOriginalPath: line)
            if isConcreteFilePathPattern(line),
               line != glob,
               lineSet.contains(glob),
               !consumed.contains(glob) {
                result.append(SkipFamilyEntry(original: line, hasConflictGlob: true))
                consumed.insert(line)
                consumed.insert(glob)
            } else {
                result.append(SkipFamilyEntry(original: line, hasConflictGlob: false))
                consumed.insert(line)
            }
        }
        return result
    }
}

enum IgnorePatternInput {
    /// Parse a (possibly multi-line) "Add pattern" field value into individual
    /// `.stignore` patterns: one per line, trimmed. Splitting is on newlines
    /// only — never on spaces — so a pattern that legitimately contains a space
    /// (e.g. `My Notes/`) stays whole. Blank lines and `//` comment lines —
    /// common when a whole `.stignore` block is pasted — are dropped, so a paste
    /// yields the real patterns instead of one unusable single-line blob (#43).
    static func parse(_ raw: String) -> [String] {
        raw.split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && !$0.hasPrefix("//") }
    }
}

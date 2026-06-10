import SwiftUI

struct LineDiffView: View {
    let original: String
    let conflict: String

    struct DiffLine: Identifiable, Sendable {
        let id = UUID()
        let type: LineType
        let text: String
        
        enum LineType: Sendable {
            case unchanged
            case added
            case removed
        }
    }

    @State private var diffLines: [DiffLine] = []
    @State private var isComputing = true

    var body: some View {
        Group {
            if isComputing {
                ProgressView("Computing diff…")
                    .padding()
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(diffLines) { line in
                            HStack(alignment: .top, spacing: 6) {
                                Text(linePrefix(for: line.type))
                                    .font(.vaultMono(.caption, weight: .semibold))
                                    .foregroundStyle(foregroundColor(for: line.type))
                                    .accessibilityHidden(true)
                                Text(line.text.isEmpty ? " " : line.text)
                                    .font(.vaultMono(.caption))
                                    .foregroundStyle(foregroundColor(for: line.type))
                            }
                            .padding(.horizontal, VaultSpacing.xs)
                            .padding(.vertical, VaultSpacing.xxs)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(backgroundColor(for: line.type))
                            .accessibilityElement(children: .ignore)
                            .accessibilityLabel(accessibilityDescription(for: line))
                        }
                    }
                    .padding()
                    // Size the column to its widest line so every row's
                    // highlight fills the same width (inside a horizontal
                    // ScrollView, maxWidth: .infinity alone clamps to each
                    // line's own width, leaving ragged backgrounds).
                    .fixedSize(horizontal: true, vertical: false)
                }
            }
        }
        .task(id: original + conflict) {
            await computeDiff()
        }
    }

    private func computeDiff() async {
        isComputing = true
        let orig = self.original
        let conf = self.conflict
        
        let lines = await Task.detached { () -> [DiffLine] in
            let oldLines = orig.components(separatedBy: .newlines)
            let newLines = conf.components(separatedBy: .newlines)
            
            let diff = newLines.difference(from: oldLines)
            var merged: [DiffLine] = []
            
            var oldIdx = 0
            var newIdx = 0
            
            // To build a nice diff, we can do a simple LCS traversal, but Swift's diff gives us removals and insertions.
            // Let's map removals by their offset in the old array, and insertions by their offset in the new array.
            var removals = [Int: String]()
            var insertions = [Int: String]()
            
            for change in diff {
                switch change {
                case let .remove(offset, element, _):
                    removals[offset] = element
                case let .insert(offset, element, _):
                    insertions[offset] = element
                }
            }
            
            while oldIdx < oldLines.count || newIdx < newLines.count {
                if let removed = removals[oldIdx] {
                    merged.append(DiffLine(type: .removed, text: removed))
                    oldIdx += 1
                } else if let inserted = insertions[newIdx] {
                    merged.append(DiffLine(type: .added, text: inserted))
                    newIdx += 1
                } else if oldIdx < oldLines.count && newIdx < newLines.count {
                    merged.append(DiffLine(type: .unchanged, text: oldLines[oldIdx]))
                    oldIdx += 1
                    newIdx += 1
                } else {
                    // Safety break
                    break
                }
            }
            return merged
        }.value
        
        diffLines = lines
        isComputing = false
    }

    private func backgroundColor(for type: DiffLine.LineType) -> Color {
        switch type {
        case .unchanged: return Color.clear
        case .added: return Color.statusSuccess.opacity(0.18)
        case .removed: return Color.statusError.opacity(0.18)
        }
    }

    private func foregroundColor(for type: DiffLine.LineType) -> Color {
        switch type {
        case .unchanged: return Color.primary
        case .added: return Color.statusSuccess
        case .removed: return Color.statusError
        }
    }

    private func linePrefix(for type: DiffLine.LineType) -> String {
        switch type {
        case .unchanged: return " "
        case .added: return "+"
        case .removed: return "-"
        }
    }

    private func accessibilityDescription(for line: DiffLine) -> String {
        let text = line.text.isEmpty ? L10n.tr("Empty line") : line.text
        switch line.type {
        case .unchanged:
            return L10n.fmt("Unchanged line. %@", text)
        case .added:
            return L10n.fmt("Added line. %@", text)
        case .removed:
            return L10n.fmt("Removed line. %@", text)
        }
    }
}

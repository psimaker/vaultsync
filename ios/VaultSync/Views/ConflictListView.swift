import SwiftUI

struct ConflictListView: View {
    let folderID: String
    /// When this folder syncs the whole Obsidian directory, scope the list to a
    /// single vault's subdirectory (e.g. "brain"); nil shows the folder's
    /// conflicts as a whole.
    var pathPrefix: String? = nil
    let syncthingManager: SyncthingManager

    /// Read live from the manager so a conflict resolved in the detail view
    /// disappears immediately. The view previously held a by-value snapshot
    /// captured at push time, which left resolved files as tappable dead rows.
    private var conflicts: [SyncthingManager.ConflictInfo] {
        let all = syncthingManager.conflictFiles[folderID] ?? []
        guard let prefix = pathPrefix else { return all }
        return all.filter { $0.belongs(toVault: prefix) }
    }

    var body: some View {
        VaultPage {
            if !conflicts.isEmpty {
                StatusChip(
                    text: VaultStatusModel.Label.conflicts(Set(conflicts.map(\.originalPath)).count).text,
                    tone: .attention,
                    systemImage: "exclamationmark.triangle.fill"
                )
            }

            // The doctrine in one sentence (decisions 027/028): no automatic
            // choice, and Keep Both never replaces a file.
            Text(L10n.tr("Two devices changed the same note while apart. Nothing is deleted until you choose, and “Keep Both” never overwrites a copy."))
                .font(.subheadline)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, VaultSpacing.xs)

            if conflicts.isEmpty {
                VaultEmptyState(
                    systemImage: "checkmark",
                    title: L10n.tr("All conflicts resolved")
                )
            } else {
                VaultSectionHeader(L10n.tr("Conflicted Files"))
                VaultCardGroup {
                    ForEach(conflicts) { conflict in
                        NavigationLink(value: SyncRoute.conflict(folderID: folderID, conflict: conflict)) {
                            VaultRow(
                                conflict.originalPath,
                                subtitle: conflict.formattedConflictDate + " · " + conflict.deviceShortID,
                                systemImage: "doc.text",
                                iconTint: .statusAttention
                            ) {
                                VaultChevron()
                            }
                        }
                        .buttonStyle(.vaultRow)
                    }
                }
            }
        }
        .navigationTitle(L10n.tr("Conflicts"))
        .navigationBarTitleDisplayMode(.large)
    }

}

extension SyncthingManager.ConflictInfo {
    /// True when this conflict's folder-relative path lives inside the named vault
    /// subdirectory (exact `vault/…` match, tolerating a stray leading slash).
    /// Used to attribute conflicts to a single vault when one Syncthing folder
    /// covers the whole Obsidian directory.
    func belongs(toVault vault: String) -> Bool {
        let path = originalPath.hasPrefix("/") ? String(originalPath.dropFirst()) : originalPath
        return path == vault || path.hasPrefix(vault + "/")
    }

    private static let conflictDateParser: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyyMMdd-HHmmss"
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()

    private static let conflictDateDisplay: DateFormatter = {
        let f = DateFormatter()
        // Localized styles (not a fixed pattern) so the displayed date follows
        // the user's locale and 12/24-hour preference.
        f.locale = .autoupdatingCurrent
        f.dateStyle = .medium
        f.timeStyle = .short
        return f
    }()

    /// Parses the Syncthing conflict-filename timestamp (e.g. "20260530-143000")
    /// into a locale-aware display string, shared by the list and the diff view.
    var formattedConflictDate: String {
        guard let date = Self.conflictDateParser.date(from: conflictDate) else { return conflictDate }
        return Self.conflictDateDisplay.string(from: date)
    }
}

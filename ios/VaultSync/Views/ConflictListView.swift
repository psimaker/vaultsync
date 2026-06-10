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
        List {
            Section {
                VStack(alignment: .leading, spacing: VaultSpacing.s) {
                    Text("What is a conflict?")
                        .font(.headline)
                    Text("A conflict happens when a file is edited on two devices at the same time. Syncthing saves both versions to prevent data loss.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                .padding(.vertical, VaultSpacing.xs)
            }
            
            Section {
                if conflicts.isEmpty {
                    Label("All conflicts resolved", systemImage: "checkmark.circle")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(conflicts) { conflict in
                        NavigationLink {
                            ConflictDiffView(
                                folderID: folderID,
                                conflict: conflict,
                                syncthingManager: syncthingManager
                            )
                        } label: {
                            VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                                Text(conflict.originalPath)
                                    .font(.body)
                                HStack(spacing: VaultSpacing.s) {
                                    Label(conflict.formattedConflictDate, systemImage: "clock")
                                    Label(conflict.deviceShortID, systemImage: "laptopcomputer")
                                }
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            }
                            .padding(.vertical, VaultSpacing.xxs)
                            .accessibilityElement(children: .combine)
                        }
                    }
                }
            } header: {
                Text("Conflicted Files")
            }
        }
        .navigationTitle("Conflicts")
        .navigationBarTitleDisplayMode(.inline)
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

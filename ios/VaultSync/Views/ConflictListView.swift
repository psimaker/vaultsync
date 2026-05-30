import SwiftUI

struct ConflictListView: View {
    let folderID: String
    let syncthingManager: SyncthingManager

    /// Read live from the manager so a conflict resolved in the detail view
    /// disappears immediately. The view previously held a by-value snapshot
    /// captured at push time, which left resolved files as tappable dead rows.
    private var conflicts: [SyncthingManager.ConflictInfo] {
        syncthingManager.conflictFiles[folderID] ?? []
    }

    var body: some View {
        List {
            Section {
                VStack(alignment: .leading, spacing: 8) {
                    Text("What is a conflict?")
                        .font(.headline)
                    Text("A conflict happens when a file is edited on two devices at the same time. Syncthing saves both versions to prevent data loss.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                .padding(.vertical, 4)
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
                            VStack(alignment: .leading, spacing: 4) {
                                Text(conflict.originalPath)
                                    .font(.body)
                                HStack(spacing: 8) {
                                    Label(conflict.formattedConflictDate, systemImage: "clock")
                                    Label(conflict.deviceShortID, systemImage: "laptopcomputer")
                                }
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            }
                            .padding(.vertical, 2)
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

import SwiftUI

struct ConflictListView: View {
    let folderID: String
    let conflicts: [SyncthingManager.ConflictInfo]
    let syncthingManager: SyncthingManager

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
                                Label(formattedDate(conflict.conflictDate), systemImage: "clock")
                                Label(conflict.deviceShortID, systemImage: "laptopcomputer")
                            }
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        }
                        .padding(.vertical, 2)
                        .accessibilityElement(children: .combine)
                    }
                }
            } header: {
                Text("Conflicted Files")
            }
        }
        .navigationTitle("Conflicts")
        .navigationBarTitleDisplayMode(.inline)
    }

    private static let conflictDateParser: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyyMMdd-HHmmss"
        f.locale = Locale(identifier: "en_US_POSIX")
        return f
    }()

    private static let conflictDateDisplay: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "yyyy-MM-dd HH:mm"
        return f
    }()

    private func formattedDate(_ dateStr: String) -> String {
        guard let date = Self.conflictDateParser.date(from: dateStr) else { return dateStr }
        return Self.conflictDateDisplay.string(from: date)
    }
}

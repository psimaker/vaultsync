import SwiftUI

struct ConflictListView: View {
    let folderID: String
    /// When this folder syncs the whole Obsidian directory, scope the list to a
    /// single vault's subdirectory (e.g. "brain"); nil shows the folder's
    /// conflicts as a whole.
    var pathPrefix: String? = nil
    let syncthingManager: SyncthingManager

    /// Read live from the manager so an engine refresh is reflected without
    /// retaining a stale by-value snapshot captured at navigation time.
    private var conflicts: [SyncthingManager.ConflictInfo] {
        let all = syncthingManager.conflictFiles[folderID] ?? []
        guard let prefix = pathPrefix else { return all }
        return all.filter { $0.belongs(toVault: prefix) }
    }

    private var inspectionUnavailable: Bool {
        syncthingManager.conflictInspectionUnavailableFolderIDs.contains(folderID)
    }

    var body: some View {
        List {
            Section {
                VStack(alignment: .leading, spacing: VaultSpacing.s) {
                    Text(L10n.tr("What is a conflict?"))
                        .font(.headline)
                    Text(L10n.tr("A conflict can happen when a file changes on two devices at the same time. Review every visible copy, because automatic retention cannot guarantee that every version will remain available."))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                .padding(.vertical, VaultSpacing.xs)
            }

            Section {
                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    Label(L10n.tr("Conflict Recovery Unavailable"), systemImage: "lock.fill")
                        .foregroundStyle(Color.statusAttention)
                        .font(.headline)
                    Text(L10n.tr("Conflict recovery actions are not available in this version."))
                        .font(.subheadline)
                    Text(L10n.tr("Review the copies that are still available here. Leave files unchanged; VaultSync cannot run a recovery action in this version."))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .padding(.vertical, VaultSpacing.xs)
            }

            Section {
                if inspectionUnavailable {
                    VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                        Label(
                            L10n.tr("Conflict inspection is unavailable."),
                            systemImage: "exclamationmark.triangle.fill"
                        )
                        .font(.headline)
                        .foregroundStyle(Color.statusAttention)
                        Text(L10n.tr("VaultSync cannot verify whether the conflict list is complete. Previously visible copies remain shown for review."))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    .padding(.vertical, VaultSpacing.xs)
                }

                if conflicts.isEmpty, !inspectionUnavailable {
                    Label(L10n.tr("No conflicts found"), systemImage: "doc.text.magnifyingglass")
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
                                    Label(L10n.tr("Conflict Copy"), systemImage: "doc.on.doc")
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
                Text(L10n.tr("Conflicted Files"))
            }
        }
        .navigationTitle(L10n.tr("Conflicts"))
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

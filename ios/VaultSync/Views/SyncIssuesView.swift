import SwiftUI

struct SyncIssuesView: View {
    let issues: [SyncthingManager.SyncIssueItem]
    let syncthingManager: SyncthingManager
    let onRescanFailedFolders: () -> Void
    let onOpenAddDevice: () -> Void
    let onAcceptFirstPendingShare: () -> Void
    let onRescanAllVaults: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(issues) { issue in
                VStack(alignment: .leading, spacing: 8) {
                    HStack(alignment: .top, spacing: 8) {
                        Image(systemName: symbol(for: issue))
                            .foregroundStyle(color(for: issue))
                            .font(.body.weight(.semibold))
                            .accessibilityHidden(true)

                        VStack(alignment: .leading, spacing: 3) {
                            Text(issue.title)
                                .font(.subheadline.weight(.semibold))
                            Text(issue.message)
                                .font(.caption)
                            Text(issue.remediation)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                            if let url = troubleshootingURL(for: issue.kind) {
                                Link("Learn how to fix", destination: url)
                                    .font(.caption2)
                            }
                        }
                    }
                    .accessibilityElement(children: .combine)

                    actionView(for: issue)
                }
                .padding(.vertical, 4)
            }
        }
    }

    private func symbol(for issue: SyncthingManager.SyncIssueItem) -> String {
        switch issue.kind {
        case .folderErrors, .conflicts, .staleSync:
            return "exclamationmark.triangle.fill"
        case .backgroundSync:
            return "clock.badge.exclamationmark"
        case .disconnectedPeers:
            return "wifi.exclamationmark"
        case .pendingShares:
            return "tray.full.fill"
        }
    }

    private func color(for issue: SyncthingManager.SyncIssueItem) -> Color {
        switch issue.severity {
        case .critical:
            return .red
        case .warning:
            return .orange
        }
    }

    @ViewBuilder
    private func actionView(for issue: SyncthingManager.SyncIssueItem) -> some View {
        switch issue.kind {
        case .folderErrors:
            Button("Rescan Failed Vaults") {
                onRescanFailedFolders()
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.small)

        case .disconnectedPeers:
            Button("Add or Reconnect Device") {
                onOpenAddDevice()
            }
            .buttonStyle(.bordered)
            .controlSize(.small)

        case .pendingShares:
            Button("Accept First Pending Share") {
                onAcceptFirstPendingShare()
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.small)
            .disabled(syncthingManager.actionablePendingFolders.isEmpty)

        case .conflicts:
            if let destination = firstConflictDestination(preferredFolderID: issue.folderID) {
                NavigationLink("Resolve Conflicts") {
                    ConflictListView(
                        folderID: destination.folderID,
                        conflicts: destination.conflicts,
                        syncthingManager: syncthingManager
                    )
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
            }

        case .staleSync:
            Button("Rescan All Vaults") {
                onRescanAllVaults()
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .disabled(syncthingManager.folders.isEmpty)

        case .backgroundSync:
            Button("Run Foreground Rescan") {
                onRescanAllVaults()
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .disabled(syncthingManager.folders.isEmpty)
        }
    }

    private func firstConflictDestination(
        preferredFolderID: String?
    ) -> (folderID: String, conflicts: [SyncthingManager.ConflictInfo])? {
        if let preferredFolderID,
           let conflicts = syncthingManager.conflictFiles[preferredFolderID],
           !conflicts.isEmpty {
            return (preferredFolderID, conflicts)
        }

        guard let entry = syncthingManager.conflictFiles
            .sorted(by: { $0.key < $1.key })
            .first(where: { !$0.value.isEmpty }) else {
            return nil
        }
        return (entry.key, entry.value)
    }

    private func troubleshootingURL(for kind: SyncthingManager.SyncIssueItem.Kind) -> URL? {
        let anchor: String
        switch kind {
        case .folderErrors:
            anchor = "bookmark-access-expired"
        case .disconnectedPeers:
            anchor = "required-device-disconnected"
        case .pendingShares:
            anchor = "no-pending-shares-appear"
        case .conflicts:
            anchor = "background-sync-not-working"
        case .staleSync, .backgroundSync:
            anchor = "background-sync-not-working"
        }
        return URL(string: "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md#\(anchor)")
    }
}

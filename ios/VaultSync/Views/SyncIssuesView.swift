import SwiftUI

struct SyncIssuesView: View {
    let issues: [SyncthingManager.SyncIssueItem]
    let syncthingManager: SyncthingManager
    let onRescanFailedFolders: () -> Void
    let onOpenAddDevice: () -> Void
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
                            VStack(alignment: .leading, spacing: 3) {
                                Text(issue.title)
                                    .font(.subheadline.weight(.semibold))
                                Text(issue.message)
                                    .font(.caption)
                                Text(issue.remediation)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            .accessibilityElement(children: .combine)

                            if let url = troubleshootingURL(for: issue.kind) {
                                ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                                    .font(.caption2)
                            }
                        }
                    }

                    actionView(for: issue)
                }
                .padding(.vertical, 4)
            }
        }
    }

    private func symbol(for issue: SyncthingManager.SyncIssueItem) -> String {
        switch issue.kind {
        case .pathCollision, .nestedFolders, .conflictRetentionSafety, .folderErrors, .conflicts, .staleSync:
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
            return .statusError
        case .warning:
            return .statusAttention
        }
    }

    // Buttons are `.regular` (the 44pt minimum touch target — `.small` violated
    // it), and a button that could do nothing is hidden instead of disabled with
    // no explanation: the prose remediation above it remains the guidance.
    @ViewBuilder
    private func actionView(for issue: SyncthingManager.SyncIssueItem) -> some View {
        switch issue.kind {
        case .pathCollision, .nestedFolders:
            // No safe automated fix: removing, renaming, or re-accepting an
            // overlapping folder risks more data loss, so recovery is
            // deliberately manual. The remediation prose above is the guidance
            // (re-select the container / remove the affected vault → it is
            // re-added into its own folder).
            EmptyView()

        case .conflictRetentionSafety:
            // The stop is deliberately read-only. Any conflict copies still
            // present in the refreshed cache remain reviewable (#150).
            if let destination = Self.conflictDestination(
                preferredFolderID: issue.folderID,
                conflictFiles: syncthingManager.conflictFiles,
                unavailableFolderIDs: syncthingManager.conflictInspectionUnavailableFolderIDs,
                allowFallback: false
            ) {
                NavigationLink(L10n.tr("Review Conflicts")) {
                    ConflictListView(
                        folderID: destination,
                        syncthingManager: syncthingManager
                    )
                }
                .buttonStyle(.bordered)
                .controlSize(.regular)
            }

        case .folderErrors:
            // A rescan cannot recreate a missing folder marker — when marker
            // loss is the only error, hide the button and let the prose
            // remediation guide the manual recovery (#65, same stance as
            // .pathCollision).
            if syncthingManager.hasRescanableFolderErrors {
                Button("Rescan Failed Vaults") {
                    onRescanFailedFolders()
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.regular)
            }

        case .disconnectedPeers:
            Button("Add or Reconnect Device") {
                onOpenAddDevice()
            }
            .buttonStyle(.bordered)
            .controlSize(.regular)

        case .pendingShares:
            // Retained enum case for durable snapshot compatibility. Pending
            // offers are inspection-only in 2.0.2 and have no issue action.
            EmptyView()

        case .conflicts:
            if let destination = Self.conflictDestination(
                preferredFolderID: issue.folderID,
                conflictFiles: syncthingManager.conflictFiles,
                unavailableFolderIDs: syncthingManager.conflictInspectionUnavailableFolderIDs,
                allowFallback: true
            ) {
                NavigationLink(L10n.tr("Review Conflicts")) {
                    ConflictListView(
                        folderID: destination,
                        syncthingManager: syncthingManager
                    )
                }
                .buttonStyle(.bordered)
                .controlSize(.regular)
            }

        case .staleSync:
            if !syncthingManager.foregroundRescanEligibleFolderIDs.isEmpty {
                Button("Rescan All Vaults") {
                    onRescanAllVaults()
                }
                .buttonStyle(.bordered)
                .controlSize(.regular)
            }

        case .backgroundSync:
            if !syncthingManager.foregroundRescanEligibleFolderIDs.isEmpty {
                Button("Run Foreground Rescan") {
                    onRescanAllVaults()
                }
                .buttonStyle(.bordered)
                .controlSize(.regular)
            }
        }
    }

    nonisolated static func conflictDestination(
        preferredFolderID: String?,
        conflictFiles: [String: [SyncthingManager.ConflictInfo]],
        unavailableFolderIDs: Set<String> = [],
        allowFallback: Bool
    ) -> String? {
        if let preferredFolderID,
           conflictFiles[preferredFolderID]?.isEmpty == false
            || unavailableFolderIDs.contains(preferredFolderID) {
            return preferredFolderID
        }

        guard allowFallback else { return nil }

        if let entry = conflictFiles
            .sorted(by: { $0.key < $1.key })
            .first(where: { !$0.value.isEmpty }) {
            return entry.key
        }
        return unavailableFolderIDs.sorted().first
    }

    private func troubleshootingURL(for kind: SyncthingManager.SyncIssueItem.Kind) -> URL? {
        let anchor: String
        switch kind {
        case .pathCollision, .nestedFolders, .conflictRetentionSafety, .pendingShares:
            // No troubleshooting-doc section for this yet, and the inline
            // remediation is the complete fix path — don't surface a
            // misdirecting link (same stance as `.conflicts`).
            return nil
        case .folderErrors:
            anchor = "bookmark-access-expired"
        case .disconnectedPeers:
            anchor = "required-device-disconnected"
        case .conflicts:
            // No conflict-resolution section exists in the troubleshooting doc,
            // and "Background Sync Not Working" is unrelated. The inline
            // review action is the complete read-only path, so don't surface a
            // misdirecting link here.
            return nil
        case .staleSync, .backgroundSync:
            anchor = "background-sync-not-working"
        }
        return URL(string: "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md#\(anchor)")
    }
}

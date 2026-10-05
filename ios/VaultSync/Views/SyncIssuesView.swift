import SwiftUI

struct SyncIssuesView: View {
    let issues: [SyncthingManager.SyncIssueItem]
    let syncthingManager: SyncthingManager
    let onRescanFailedFolders: () -> Void
    let onOpenAddDevice: () -> Void
    let onRescanAllVaults: () -> Void

    /// Problems get a full card on the wash of their severity — what is
    /// wrong, why, the next step and its action. Conflicts are an ordinary
    /// decision and get the canvas's compact rows instead, one per synced
    /// folder ("Notes · 1 conflict"), each opening exactly that folder's
    /// conflicts; the explanation and the choices live one tap away (#187
    /// review). Pending shares never reach this view: their own section
    /// carries the share with its sender and choices.
    var body: some View {
        ForEach(issues) { issue in
            if issue.kind == .conflicts, !conflictedFolders.isEmpty {
                conflictRows
            } else {
                issueCard(issue)
            }
        }
    }

    private var conflictRows: some View {
        VaultCardGroup(tone: .attention) {
            ForEach(conflictedFolders, id: \.id) { folder in
                NavigationLink(value: SyncRoute.conflicts(folderID: folder.id, pathPrefix: nil)) {
                    VaultRow(
                        folder.name,
                        subtitle: VaultStatusModel.Label.conflicts(folder.fileCount).text,
                        systemImage: "exclamationmark.triangle.fill",
                        iconTint: .statusAttention
                    ) {
                        VaultChevron()
                    }
                }
                .buttonStyle(.vaultRow)
            }
        }
    }

    /// Synced folders holding conflicts, with their distinct conflicted
    /// files — the same count the issue and the vault rows use.
    private var conflictedFolders: [(id: String, name: String, fileCount: Int)] {
        syncthingManager.conflictFiles
            .filter { !$0.value.isEmpty }
            .sorted { $0.key < $1.key }
            .map { entry in
                let label = syncthingManager.folders.first { $0.id == entry.key }?.label ?? ""
                return (
                    id: entry.key,
                    name: label.isEmpty ? entry.key : label,
                    fileCount: Set(entry.value.map(\.originalPath)).count
                )
            }
    }

    private func issueCard(_ issue: SyncthingManager.SyncIssueItem) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            HStack(alignment: .top, spacing: VaultSpacing.m) {
                Image(systemName: symbol(for: issue))
                    .font(.title3)
                    .foregroundStyle(color(for: issue))
                    .frame(width: 28)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                        Text(issue.title)
                            .font(.body.weight(.semibold))
                            .foregroundStyle(Color.vaultLabel)
                        Text(issue.message)
                            .font(.subheadline)
                            .foregroundStyle(Color.vaultLabel)
                        Text(issue.remediation)
                            .font(.footnote)
                            .foregroundStyle(Color.vaultSecondaryLabel)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityElement(children: .combine)

                    if let url = troubleshootingURL(for: issue.kind) {
                        ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                            .font(.footnote)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }

            actionView(for: issue)
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(tone: issue.severity == .critical ? .error : .attention)
    }

    private func symbol(for issue: SyncthingManager.SyncIssueItem) -> String {
        switch issue.kind {
        case .pathCollision, .nestedFolders, .folderErrors, .conflicts, .staleSync:
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

    // Buttons keep the 44pt minimum touch target (`.small` violated it), and a
    // button that could do nothing is hidden instead of disabled with no
    // explanation: the prose remediation above it remains the guidance.
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

        case .folderErrors:
            // A rescan cannot recreate a missing folder marker — when marker
            // loss is the only error, hide the button and let the prose
            // remediation guide the manual recovery (#65, same stance as
            // .pathCollision).
            if syncthingManager.hasRescanableFolderErrors {
                Button("Rescan Failed Vaults") {
                    onRescanFailedFolders()
                }
                .buttonStyle(.vault(.primary, compact: true))
            }

        case .disconnectedPeers:
            Button("Add or Reconnect Device") {
                onOpenAddDevice()
            }
            .buttonStyle(.vault(.primary, compact: true))

        case .pendingShares, .conflicts:
            // No action on a card: a pending share is presented with its
            // sender and choices in Pending Shares, and conflicts get the
            // compact row that opens the conflict list (#187 review).
            EmptyView()

        case .staleSync:
            if !syncthingManager.folders.isEmpty {
                Button("Rescan All Vaults") {
                    onRescanAllVaults()
                }
                .buttonStyle(.vault(.primary, compact: true))
            }

        case .backgroundSync:
            if !syncthingManager.folders.isEmpty {
                Button("Run Foreground Rescan") {
                    onRescanAllVaults()
                }
                .buttonStyle(.vault(.primary, compact: true))
            }
        }
    }

    private func troubleshootingURL(for kind: SyncthingManager.SyncIssueItem.Kind) -> URL? {
        let anchor: String
        switch kind {
        case .pathCollision, .nestedFolders:
            // No troubleshooting-doc section for this yet, and the inline
            // remediation is the complete fix path — don't surface a
            // misdirecting link (same stance as `.conflicts`).
            return nil
        case .folderErrors:
            anchor = "bookmark-access-expired"
        case .disconnectedPeers:
            anchor = "required-device-disconnected"
        case .pendingShares:
            anchor = "no-pending-shares-appear"
        case .conflicts:
            // No conflict-resolution section exists in the troubleshooting doc,
            // and "Background Sync Not Working" is unrelated. The conflicts
            // row opens the conflict list — the correct fix path — so don't
            // surface a misdirecting link here.
            return nil
        case .staleSync, .backgroundSync:
            anchor = "background-sync-not-working"
        }
        return URL(string: "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md#\(anchor)")
    }
}

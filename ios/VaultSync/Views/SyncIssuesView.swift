import SwiftUI

struct SyncIssuesView: View {
    let issues: [SyncthingManager.SyncIssueItem]
    let syncthingManager: SyncthingManager
    let onRescanFailedFolders: () -> Void
    let onOpenAddDevice: () -> Void
    let onAcceptFirstPendingShare: () -> Void
    let onRescanAllVaults: () -> Void

    /// One card per issue (#187), on the wash of its severity: critical
    /// issues on the error wash, warnings on the attention wash.
    var body: some View {
        ForEach(issues) { issue in
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

        case .pendingShares:
            if !syncthingManager.actionablePendingFolders.isEmpty {
                // "First" only when there IS a queue — for a single share the
                // qualifier read as if more were hiding somewhere (#71).
                Button(syncthingManager.actionablePendingFolders.count == 1
                    ? L10n.tr("Accept Pending Share")
                    : L10n.tr("Accept First Pending Share")) {
                    onAcceptFirstPendingShare()
                }
                .buttonStyle(.vault(.primary, compact: true))
            }

        case .conflicts:
            if let destination = firstConflictDestination(preferredFolderID: issue.folderID) {
                NavigationLink(value: SyncRoute.conflicts(folderID: destination.folderID, pathPrefix: nil)) {
                    Text("Resolve Conflicts")
                }
                .buttonStyle(.vault(.primary, compact: true))
            }

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
            // and "Background Sync Not Working" is unrelated. The inline
            // "Resolve Conflicts" action is the correct fix path, so don't
            // surface a misdirecting link here.
            return nil
        case .staleSync, .backgroundSync:
            anchor = "background-sync-not-working"
        }
        return URL(string: "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md#\(anchor)")
    }
}

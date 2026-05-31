import SwiftUI

struct PendingSharesView: View {
    let pendingFolders: [SyncthingManager.PendingFolderInfo]
    let ignoredFolders: [SyncthingManager.PendingFolderInfo]
    let failureByFolderID: [String: SyncUserError]
    let inFlightFolderIDs: Set<String>
    let obsidianAccessible: Bool
    var onAccept: (SyncthingManager.PendingFolderInfo) -> Void
    var onRetry: (SyncthingManager.PendingFolderInfo) -> Void
    var onIgnore: (SyncthingManager.PendingFolderInfo) -> Void
    var onRestoreIgnored: (SyncthingManager.PendingFolderInfo) -> Void
    var onReconnectObsidian: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if !obsidianAccessible {
                ActionCard(
                    status: .attention,
                    title: L10n.tr("Connect Obsidian to accept shares"),
                    message: L10n.tr("Share requests are shown below, but Accept and Retry are disabled until your Obsidian folder is connected."),
                    actionTitle: L10n.tr("Reconnect Obsidian Folder"),
                    action: onReconnectObsidian
                )
            }

            if pendingFolders.isEmpty {
                Label("No active pending shares", systemImage: "checkmark.circle")
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(.secondary)
            }

            ForEach(pendingFolders) { folder in
                pendingRow(folder)
            }

            if !ignoredFolders.isEmpty {
                DisclosureGroup(L10n.fmt("Ignored shares (%d)", ignoredFolders.count)) {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(ignoredFolders) { folder in
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(displayName(for: folder))
                                        .font(.subheadline.weight(.semibold))
                                    Text(offeredByDescription(for: folder))
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                                Spacer()
                                Button("Restore Share") {
                                    onRestoreIgnored(folder)
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                            }
                        }
                    }
                    .padding(.top, 8)
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 4)
    }

    @ViewBuilder
    private func pendingRow(_ folder: SyncthingManager.PendingFolderInfo) -> some View {
        let failure = failureByFolderID[folder.id]
        let hasFailure = failure != nil
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 8) {
                Image(systemName: hasFailure ? "exclamationmark.circle.fill" : "tray.and.arrow.down.fill")
                    .foregroundStyle(hasFailure ? Color.statusAttention : Color.statusInfo)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(displayName(for: folder))
                            .font(.body.weight(.semibold))
                        Text(hasFailure ? L10n.tr("Needs Attention") : L10n.tr("Ready"))
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(hasFailure ? Color.statusAttention : Color.statusInfo)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background((hasFailure ? Color.statusAttention : Color.statusInfo).opacity(0.12), in: Capsule())
                    }

                    Text(offeredByDescription(for: folder))
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    if let failure {
                        Text(failure.message)
                            .font(.caption)
                            .foregroundStyle(Color.statusAttention)
                        if !failure.remediation.isEmpty {
                            Text(failure.remediation)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
                Spacer()
            }
            .accessibilityElement(children: .combine)

            HStack(spacing: 8) {
                if inFlightFolderIDs.contains(folder.id) {
                    ProgressView()
                        .controlSize(.small)
                        .accessibilityHidden(true)
                    Text("Applying…")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else if failure == nil {
                    Button("Accept Share") {
                        onAccept(folder)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!obsidianAccessible)
                } else {
                    Button("Retry Accept") {
                        onRetry(folder)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!obsidianAccessible)
                }

                Button("Ignore for Now") {
                    onIgnore(folder)
                }
                .buttonStyle(.bordered)
                .disabled(inFlightFolderIDs.contains(folder.id))
            }
        }
        .padding(VaultSpacing.m)
        .vaultCard()
    }

    private func displayName(for folder: SyncthingManager.PendingFolderInfo) -> String {
        folder.label.isEmpty ? folder.id : folder.label
    }

    private func offeredByDescription(for folder: SyncthingManager.PendingFolderInfo) -> String {
        let names = folder.offeredBy.map { offeredDevice in
            offeredDevice.name.isEmpty ? offeredDevice.deviceID : offeredDevice.name
        }
        if names.isEmpty {
            return L10n.tr("Shared by an unknown device")
        }
        return L10n.fmt("Shared by: %@", names.joined(separator: ", "))
    }
}

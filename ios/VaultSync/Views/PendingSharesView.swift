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
                VStack(alignment: .leading, spacing: 8) {
                    Label("Connect Obsidian to accept shares", systemImage: "folder.badge.questionmark")
                        .foregroundStyle(.orange)
                    Text("Share requests are shown below, but Accept and Retry are disabled until your Obsidian folder is connected.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Button {
                        onReconnectObsidian()
                    } label: {
                        Label("Reconnect Obsidian Folder", systemImage: "folder.badge.gearshape")
                    }
                    .buttonStyle(.borderedProminent)
                }
                .padding(12)
                .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .accessibilityElement(children: .combine)
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
                DisclosureGroup("Ignored shares (\(ignoredFolders.count))") {
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
                    .foregroundStyle(hasFailure ? .orange : .blue)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(displayName(for: folder))
                            .font(.body.weight(.semibold))
                        Text(hasFailure ? "Needs Attention" : "Ready")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(hasFailure ? .orange : .blue)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background((hasFailure ? Color.orange : Color.blue).opacity(0.12), in: Capsule())
                    }

                    Text(offeredByDescription(for: folder))
                        .font(.caption)
                        .foregroundStyle(.secondary)

                    if let failure {
                        Text(failure.message)
                            .font(.caption)
                            .foregroundStyle(.orange)
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
                        .accessibilityLabel("Applying share")
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
        .padding(12)
        .background(Color(.secondarySystemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    private func displayName(for folder: SyncthingManager.PendingFolderInfo) -> String {
        folder.label.isEmpty ? folder.id : folder.label
    }

    private func offeredByDescription(for folder: SyncthingManager.PendingFolderInfo) -> String {
        let names = folder.offeredBy.map { offeredDevice in
            offeredDevice.name.isEmpty ? offeredDevice.deviceID : offeredDevice.name
        }
        if names.isEmpty {
            return "Shared by an unknown device"
        }
        return "Shared by: \(names.joined(separator: ", "))"
    }
}

import SwiftUI

struct PendingSharesView: View {
    let pendingFolders: [SyncthingManager.PendingFolderInfo]
    let ignoredFolders: [SyncthingManager.PendingFolderInfo]

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.l) {
            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                Label(L10n.tr("Pending shares are read-only in this version."), systemImage: "lock.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Color.statusInfo)
                Text(L10n.tr("You can inspect who shared each offer, but no action is available."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
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
                    VStack(alignment: .leading, spacing: VaultSpacing.s) {
                        ForEach(ignoredFolders) { folder in
                            HStack {
                                VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                                    Text(displayName(for: folder))
                                        .font(.subheadline.weight(.semibold))
                                    Text(offeredByDescription(for: folder))
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                                Spacer()
                                StatusTag(
                                    text: L10n.tr("Read Only"),
                                    tint: Color.statusInfo
                                )
                            }
                        }
                    }
                    .padding(.top, VaultSpacing.s)
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, VaultSpacing.xs)
    }

    @ViewBuilder
    private func pendingRow(_ folder: SyncthingManager.PendingFolderInfo) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            HStack(alignment: .top, spacing: VaultSpacing.s) {
                Image(systemName: "tray.full.fill")
                    .foregroundStyle(Color.statusInfo)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    HStack(spacing: VaultSpacing.xs) {
                        Text(displayName(for: folder))
                            .font(.body.weight(.semibold))
                        StatusTag(
                            text: L10n.tr("Read Only"),
                            tint: Color.statusInfo
                        )
                    }

                    Text(offeredByDescription(for: folder))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
            }
            .accessibilityElement(children: .combine)
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

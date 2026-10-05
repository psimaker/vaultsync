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
    var onChooseTarget: (SyncthingManager.PendingFolderInfo) -> Void
    var onReconnectObsidian: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
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
                Label(L10n.tr("No active pending shares"), systemImage: "checkmark.circle")
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .padding(.horizontal, VaultSpacing.xs)
            }

            ForEach(pendingFolders) { folder in
                pendingCard(folder)
            }

            if !ignoredFolders.isEmpty {
                ignoredSharesCard
            }
        }
    }

    /// One offer on the info wash (#187) — the attention wash once the
    /// automatic pass parked it, so a share waiting on a decision reads
    /// differently from one that is simply ready.
    private func pendingCard(_ folder: SyncthingManager.PendingFolderInfo) -> some View {
        let failure = failureByFolderID[folder.id]
        let hasFailure = failure != nil
        let inFlight = inFlightFolderIDs.contains(folder.id)
        return VStack(alignment: .leading, spacing: VaultSpacing.m) {
            HStack(alignment: .top, spacing: VaultSpacing.m) {
                Image(systemName: hasFailure ? "exclamationmark.circle.fill" : "tray.and.arrow.down.fill")
                    .font(.title3)
                    .foregroundStyle(hasFailure ? Color.statusAttention : Color.statusInfo)
                    .frame(width: 28)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    Text(displayName(for: folder))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Color.vaultLabel)
                    StatusChip(
                        text: hasFailure ? L10n.tr("Needs Attention") : L10n.tr("Ready"),
                        tone: hasFailure ? .attention : .info
                    )
                    Text(offeredByDescription(for: folder))
                        .font(.footnote)
                        .foregroundStyle(Color.vaultSecondaryLabel)

                    if let failure {
                        Text(failure.message)
                            .font(.subheadline)
                            .foregroundStyle(Color.statusAttentionText)
                        if !failure.remediation.isEmpty {
                            Text(failure.remediation)
                                .font(.footnote)
                                .foregroundStyle(Color.vaultSecondaryLabel)
                        }
                    }
                }
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityElement(children: .combine)

            VaultButtonRow {
                if inFlight {
                    HStack(spacing: VaultSpacing.s) {
                        ProgressView()
                            .controlSize(.small)
                            .accessibilityHidden(true)
                        Text(L10n.tr("Applying…"))
                            .font(.footnote)
                            .foregroundStyle(Color.vaultSecondaryLabel)
                    }
                    .frame(maxWidth: .infinity, minHeight: VaultMetrics.compactButtonHeight)
                } else if failure == nil {
                    Button(L10n.tr("Accept Share")) {
                        onAccept(folder)
                    }
                    .buttonStyle(.vault(.primary, compact: true))
                    .disabled(!obsidianAccessible)
                } else {
                    // Not "Retry": most parks come from the AUTOMATIC pass
                    // (e.g. a merge waiting for consent) — "Retry" falsely
                    // implied a prior attempt by the user (#71).
                    Button(L10n.tr("Review and Accept")) {
                        onRetry(folder)
                    }
                    .buttonStyle(.vault(.primary, compact: true))
                    .disabled(!obsidianAccessible)
                }

                Button(L10n.tr("Ignore for Now")) {
                    onIgnore(folder)
                }
                .buttonStyle(.vault(.neutral, compact: true))
                .disabled(inFlight)
            }

            // The per-share manual path (#52): pick an existing empty vault or
            // create a custom-named folder instead of the share-label default.
            if !inFlight {
                Button(L10n.tr("Choose Vault…")) {
                    onChooseTarget(folder)
                }
                .buttonStyle(.vault(.neutral, compact: true))
                .disabled(!obsidianAccessible)
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(tone: hasFailure ? .attention : .info)
    }

    private var ignoredSharesCard: some View {
        DisclosureGroup(L10n.fmt("Ignored shares (%d)", ignoredFolders.count)) {
            VStack(alignment: .leading, spacing: VaultSpacing.l) {
                ForEach(ignoredFolders) { folder in
                    VStack(alignment: .leading, spacing: VaultSpacing.s) {
                        VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                            Text(displayName(for: folder))
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(Color.vaultLabel)
                            Text(offeredByDescription(for: folder))
                                .font(.footnote)
                                .foregroundStyle(Color.vaultSecondaryLabel)
                        }
                        .accessibilityElement(children: .combine)
                        // Restoring hands the share back to auto-accept;
                        // "Choose Vault…" accepts it directly into a picked
                        // target instead (#52).
                        VaultButtonRow {
                            Button(L10n.tr("Restore Share")) {
                                onRestoreIgnored(folder)
                            }
                            .buttonStyle(.vault(.neutral, compact: true))
                            Button(L10n.tr("Choose Vault…")) {
                                onChooseTarget(folder)
                            }
                            .buttonStyle(.vault(.neutral, compact: true))
                            .disabled(!obsidianAccessible)
                        }
                    }
                }
            }
            .padding(.top, VaultSpacing.m)
        }
        .font(.subheadline)
        .tint(Color.vaultAccentText)
        .padding(VaultSpacing.l)
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

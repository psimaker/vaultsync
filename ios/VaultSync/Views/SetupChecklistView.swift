import SwiftUI

struct SetupChecklistView: View {
    var viewModel: SetupChecklistViewModel
    /// When set, incomplete items with an in-app entry point render a real action
    /// button below their remediation text — instead of leaving the user to
    /// navigate there by prose directions.
    var onAction: ((SetupChecklistViewModel.ChecklistAction) -> Void)? = nil
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    /// #187: the summary and every item are their own cards on the page,
    /// instead of bordered boxes nested inside one big card.
    var body: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            VStack(alignment: .leading, spacing: VaultSpacing.l) {
                headerSection

                ProgressView(value: viewModel.completionProgress)
                    .tint(viewModel.isReadyToFinish ? Color.statusSuccess : Color.statusAttention)
                    .accessibilityLabel(L10n.tr("Setup status progress"))
                    .accessibilityValue(L10n.fmt("%d of %d essentials ready", viewModel.completedRequiredCount, viewModel.totalRequiredCount))
            }
            .padding(VaultSpacing.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .vaultCard()

            ForEach(viewModel.items) { item in
                checklistRow(item)
            }
        }
    }

    @ViewBuilder
    private var headerSection: some View {
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 8) {
                Text(L10n.tr("Check the essentials for syncing. You can complete setup actions from the VaultSync home screen."))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                progressChip
            }
        } else {
            HStack(alignment: .firstTextBaseline) {
                Text(L10n.tr("Check the essentials for syncing. You can complete setup actions from the VaultSync home screen."))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                Spacer()
                progressChip
            }
        }
    }

    private var progressChip: some View {
        StatusChip(
            text: L10n.fmt("%d of %d essentials ready", viewModel.completedRequiredCount, viewModel.totalRequiredCount),
            tone: viewModel.isReadyToFinish ? .success : .neutral
        )
        .monospacedDigit()
    }

    @ViewBuilder
    private func checklistRow(_ item: SetupChecklistViewModel.ChecklistItem) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if dynamicTypeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 8) {
                        Image(systemName: statusIcon(for: item))
                            .font(.body.weight(.semibold))
                            .foregroundStyle(statusColor(for: item))
                            .accessibilityHidden(true)
                        Text(item.title)
                            .font(.body.weight(.semibold))
                    }
                    optionalBadge(for: item)
                }
                .accessibilityElement(children: .combine)
                .accessibilityValue(statusAccessibilityValue(for: item))
            } else {
                HStack(spacing: 8) {
                    Image(systemName: statusIcon(for: item))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(statusColor(for: item))
                            .accessibilityHidden(true)
                    Text(item.title)
                        .font(.body.weight(.semibold))
                    Spacer()
                    optionalBadge(for: item)
                }
                .accessibilityElement(children: .combine)
                .accessibilityValue(statusAccessibilityValue(for: item))
            }

            Text(item.description)
                .font(.subheadline)
                .foregroundStyle(item.isComplete ? Color.vaultSecondaryLabel : Color.vaultLabel)

            if !item.remediation.isEmpty && !item.isComplete {
                HStack(alignment: .top, spacing: 6) {
                    Image(systemName: "arrow.forward.circle.fill")
                        .foregroundStyle(Color.vaultSecondaryLabel)
                        .accessibilityHidden(true)
                    Text(item.remediation)
                }
                .font(.footnote)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .accessibilityElement(children: .combine)
            }

            if !item.isComplete, let action = item.action, let onAction {
                Button(actionTitle(for: action)) {
                    onAction(action)
                }
                .buttonStyle(.vault(.primary, compact: true))
                .padding(.top, VaultSpacing.xxs)
            }

        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard()
    }

    private func statusIcon(for item: SetupChecklistViewModel.ChecklistItem) -> String {
        if item.isOptional {
            return item.isComplete ? "checkmark.circle.fill" : "circle.dashed"
        }
        if item.isComplete { return "checkmark.circle.fill" }
        return "exclamationmark.circle.fill"
    }

    private func statusColor(for item: SetupChecklistViewModel.ChecklistItem) -> Color {
        if item.isOptional {
            return item.isComplete ? .statusSuccess : .vaultSecondaryLabel
        }
        if item.isComplete { return .statusSuccess }
        return .statusAttention
    }

    private func actionTitle(for action: SetupChecklistViewModel.ChecklistAction) -> String {
        switch action {
        case .connectObsidian:
            return L10n.tr("Connect Obsidian Folder")
        case .addDevice:
            return L10n.tr("Add Device")
        case .openRelayTab:
            return L10n.tr("Open the Relay tab")
        }
    }

    @ViewBuilder
    private func optionalBadge(for item: SetupChecklistViewModel.ChecklistItem) -> some View {
        if item.isOptional {
            StatusChip(text: L10n.tr("Optional"), tone: item.isComplete ? .success : .neutral)
        }
    }

    /// VoiceOver status for a checklist item — required items previously exposed no
    /// completion state at all (their only signal was a decorative, a11y-hidden icon).
    private func statusAccessibilityValue(for item: SetupChecklistViewModel.ChecklistItem) -> String {
        if item.isComplete { return L10n.tr("Done") }
        if item.isOptional { return L10n.tr("Optional") }
        return L10n.tr("Needs Attention")
    }
}

/// The Setup Status checklist as a self-contained sheet — shared by Settings
/// and the tappable status header (#95) so the two entry points cannot drift.
struct SetupChecklistSheet: View {
    let syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    var onAction: ((SetupChecklistViewModel.ChecklistAction) -> Void)? = nil
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            VaultPage {
                SetupChecklistView(
                    viewModel: SetupChecklistViewModel(
                        syncthingManager: syncthingManager,
                        vaultManager: vaultManager,
                        subscriptionManager: subscriptionManager
                    ),
                    onAction: onAction
                )
            }
            .navigationTitle(L10n.tr("Setup Status"))
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}

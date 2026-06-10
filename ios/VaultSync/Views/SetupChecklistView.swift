import SwiftUI

struct SetupChecklistView: View {
    var viewModel: SetupChecklistViewModel
    /// When set, incomplete items with an in-app entry point render a real action
    /// button below their remediation text — instead of leaving the user to
    /// navigate there by prose directions.
    var onAction: ((SetupChecklistViewModel.ChecklistAction) -> Void)? = nil
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            headerSection

            ProgressView(value: viewModel.completionProgress)
                .tint(viewModel.isReadyToFinish ? Color.statusSuccess : Color.statusAttention)
                .accessibilityLabel(L10n.tr("Setup status progress"))
                .accessibilityValue(L10n.fmt("%d of %d essentials ready", viewModel.completedRequiredCount, viewModel.totalRequiredCount))

            ForEach(viewModel.items) { item in
                checklistRow(item)
            }

        }
        .padding(VaultSpacing.l)
        .vaultCard()
    }

    @ViewBuilder
    private var headerSection: some View {
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 8) {
                Text(L10n.tr("Check the essentials for syncing. You can complete setup actions from the VaultSync home screen."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                Text(L10n.fmt("%d of %d essentials ready", viewModel.completedRequiredCount, viewModel.totalRequiredCount))
                    .font(.caption.weight(.semibold).monospacedDigit())
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, VaultSpacing.s)
                    .padding(.vertical, VaultSpacing.xs)
                    .background(Color(.tertiarySystemBackground), in: Capsule())
            }
        } else {
            HStack(alignment: .firstTextBaseline) {
                Text(L10n.tr("Check the essentials for syncing. You can complete setup actions from the VaultSync home screen."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                Spacer()
                Text(L10n.fmt("%d of %d essentials ready", viewModel.completedRequiredCount, viewModel.totalRequiredCount))
                    .font(.caption.weight(.semibold).monospacedDigit())
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, VaultSpacing.s)
                    .padding(.vertical, VaultSpacing.xs)
                    .background(Color(.tertiarySystemBackground), in: Capsule())
            }
        }
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
                .foregroundStyle(item.isComplete ? .secondary : .primary)

            if !item.remediation.isEmpty && !item.isComplete {
                HStack(alignment: .top, spacing: 6) {
                    Image(systemName: "arrow.forward.circle.fill")
                        .foregroundStyle(.secondary)
                        .accessibilityHidden(true)
                    Text(item.remediation)
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                .accessibilityElement(children: .combine)
            }

            if !item.isComplete, let action = item.action, let onAction {
                Button(actionTitle(for: action)) {
                    onAction(action)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.regular)
                .padding(.top, VaultSpacing.xxs)
            }

        }
        .padding(VaultSpacing.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
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
            return item.isComplete ? .statusSuccess : .secondary
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
            StatusTag(text: L10n.tr("Optional"), tint: statusColor(for: item))
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

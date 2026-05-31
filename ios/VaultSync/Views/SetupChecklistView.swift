import SwiftUI

struct SetupChecklistView: View {
    var viewModel: SetupChecklistViewModel
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
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
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
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
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

    @ViewBuilder
    private func optionalBadge(for item: SetupChecklistViewModel.ChecklistItem) -> some View {
        if item.isOptional {
            Text(L10n.tr("Optional"))
                .font(.caption2.weight(.semibold))
                .foregroundStyle(statusColor(for: item))
                .padding(.horizontal, VaultSpacing.s)
                .padding(.vertical, 3)
                .background(statusColor(for: item).opacity(0.15), in: Capsule())
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

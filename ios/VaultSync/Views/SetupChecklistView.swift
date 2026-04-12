import SwiftUI

struct SetupChecklistView: View {
    var viewModel: SetupChecklistViewModel
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            headerSection

            ProgressView(value: viewModel.completionProgress)
                .tint(viewModel.isReadyToFinish ? .green : .orange)
                .accessibilityLabel("Setup checklist progress")
                .accessibilityValue(L10n.fmt("%d of %d required steps complete", viewModel.completedRequiredCount, viewModel.totalRequiredCount))

            ForEach(viewModel.items) { item in
                checklistRow(item)
            }

        }
        .padding(16)
        .background(Color(.secondarySystemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    @ViewBuilder
    private var headerSection: some View {
        if dynamicTypeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 8) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Setup Checklist")
                        .font(.title3.weight(.semibold))
                    Text("Finish the required steps so sync stays reliable.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Text("\(viewModel.completedRequiredCount)/\(viewModel.totalRequiredCount) required")
                    .font(.caption.weight(.semibold).monospacedDigit())
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
                    .background(Color(.tertiarySystemBackground), in: Capsule())
            }
        } else {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Setup Checklist")
                        .font(.title3.weight(.semibold))
                    Text("Finish the required steps so sync stays reliable.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Text("\(viewModel.completedRequiredCount)/\(viewModel.totalRequiredCount) required")
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
                    Text(statusText(for: item))
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(statusColor(for: item))
                        .padding(.horizontal, 8)
                        .padding(.vertical, 3)
                        .background(statusColor(for: item).opacity(0.15), in: Capsule())
                }
                .accessibilityElement(children: .combine)
            } else {
                HStack(spacing: 8) {
                    Image(systemName: statusIcon(for: item))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(statusColor(for: item))
                        .accessibilityHidden(true)
                    Text(item.title)
                        .font(.body.weight(.semibold))
                    Spacer()
                    Text(statusText(for: item))
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(statusColor(for: item))
                        .padding(.horizontal, 8)
                        .padding(.vertical, 3)
                        .background(statusColor(for: item).opacity(0.15), in: Capsule())
                }
                .accessibilityElement(children: .combine)
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
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemBackground))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
    }

    private func statusIcon(for item: SetupChecklistViewModel.ChecklistItem) -> String {
        if item.isComplete { return "checkmark.circle.fill" }
        if item.isOptional { return "circle.dashed" }
        return "exclamationmark.circle.fill"
    }

    private func statusColor(for item: SetupChecklistViewModel.ChecklistItem) -> Color {
        if item.isComplete { return .green }
        if item.isOptional { return .secondary }
        return .orange
    }

    private func statusText(for item: SetupChecklistViewModel.ChecklistItem) -> String {
        if item.isComplete { return L10n.tr("Complete") }
        if item.isOptional { return L10n.tr("Optional") }
        return L10n.tr("Required")
    }
}

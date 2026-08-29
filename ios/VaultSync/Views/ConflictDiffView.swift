import SwiftUI

struct ConflictDiffView: View {
    let folderID: String
    let conflict: SyncthingManager.ConflictInfo
    let syncthingManager: SyncthingManager

    @State private var originalInspection: SyncBridgeService.FileInspectionResult?
    @State private var conflictInspection: SyncBridgeService.FileInspectionResult?
    @State private var isLoading = true
    @State private var showLineDiff = false

    var body: some View {
        Group {
            if isLoading {
                ProgressView(L10n.tr("Loading files…"))
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: VaultSpacing.l) {
                        inspectionNotice

                        VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                            Text(conflict.originalPath)
                                .font(.headline)
                            HStack(spacing: VaultSpacing.s) {
                                Label(conflict.formattedConflictDate, systemImage: "clock")
                            }
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .accessibilityElement(children: .combine)
                        }
                        .padding(.horizontal)

                        Divider()

                        if comparableContents != nil {
                            Toggle(L10n.tr("Show Line-by-Line Diff"), isOn: $showLineDiff)
                                .padding(.horizontal)
                                .padding(.bottom, VaultSpacing.xxs)
                        }

                        comparisonContent
                    }
                    .padding(.vertical)
                }
            }
        }
        .navigationTitle(L10n.tr("Conflict Details"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await loadContent()
        }
    }

    private var inspectionNotice: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.xs) {
            Label(L10n.tr("Conflict Recovery Unavailable"), systemImage: "lock.fill")
                .font(.headline)
                .foregroundStyle(Color.statusAttention)
            Text(L10n.tr("Conflict recovery actions are not available in this version."))
                .font(.subheadline)
            Text(L10n.tr("Review the copies that are still available here. Leave files unchanged; VaultSync cannot run a recovery action in this version."))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal)
        .accessibilityElement(children: .combine)
    }

    /// The body of the comparison — line-by-line diff (with a colour/sign legend)
    /// or the two side-by-side file panes.
    @ViewBuilder
    private var comparisonContent: some View {
        if showLineDiff, let comparableContents {
            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                Text(L10n.tr("Differences"))
                    .font(.subheadline.bold())
                    .padding(.horizontal)
                diffLegend
                LineDiffView(
                    original: comparableContents.original,
                    conflict: comparableContents.conflict
                )
            }
        } else {
            fileSection(
                title: L10n.tr("Current File"),
                icon: "doc.text",
                inspection: originalInspection ?? .unavailable
            )

            fileSection(
                title: L10n.tr("Conflict Copy"),
                icon: "doc.on.doc",
                inspection: conflictInspection ?? .unavailable
            )
        }
    }

    private var comparableContents: (original: String, conflict: String)? {
        guard case let .content(original)? = originalInspection,
              case let .content(conflict)? = conflictInspection else {
            return nil
        }
        return (original, conflict)
    }

    /// Colour is never the only signal: the plus/minus symbols carry the same
    /// meaning for colourblind users and VoiceOver.
    private var diffLegend: some View {
        HStack(spacing: VaultSpacing.m) {
            Label(L10n.tr("Conflict Copy"), systemImage: "plus")
                .foregroundStyle(Color.statusSuccess)
            Label(L10n.tr("Current File"), systemImage: "minus")
                .foregroundStyle(Color.statusError)
        }
        .font(.caption2)
        .padding(.horizontal)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.tr("Added lines are from the conflict copy; removed lines are from the current file."))
    }

    private func fileSection(
        title: String,
        icon: String,
        inspection: SyncBridgeService.FileInspectionResult
    ) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
            Label(title, systemImage: icon)
                .font(.subheadline.bold())
                .padding(.horizontal)

            ScrollView(.horizontal, showsIndicators: false) {
                Text(fileInspectionText(inspection))
                    .font(.vaultMono(.caption))
                    .padding(VaultSpacing.m)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(Color(.secondarySystemBackground))
            .clipShape(RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous))
            .padding(.horizontal)
        }
    }

    private func fileInspectionText(_ inspection: SyncBridgeService.FileInspectionResult) -> String {
        switch inspection {
        case let .content(content):
            return content.isEmpty ? L10n.tr("(empty)") : content
        case .unavailable:
            return L10n.tr("This copy is unavailable for inspection.")
        }
    }

    private func loadContent() async {
        let capturedFolderID = folderID
        let capturedConflict = conflict

        let (orig, conf) = await Task.detached {
            let o = SyncBridgeService.readFileContent(folderID: capturedFolderID, relPath: capturedConflict.originalPath)
            let c = SyncBridgeService.readFileContent(folderID: capturedFolderID, relPath: capturedConflict.conflictPath)
            return (o, c)
        }.value

        originalInspection = orig
        conflictInspection = conf
        isLoading = false
    }
}

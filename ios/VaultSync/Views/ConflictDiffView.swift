import SwiftUI

struct ConflictDiffView: View {
    let folderID: String
    let conflict: SyncthingManager.ConflictInfo
    let syncthingManager: SyncthingManager

    @State private var originalContent = ""
    @State private var conflictContent = ""
    @State private var isLoading = true
    @State private var loadError: String?
    @State private var alertMessage: String?
    @State private var showAlert = false
    @State private var showLineDiff = false
    
    // Action confirmation flow
    @State private var actionToConfirm: ResolveAction?
    @State private var showConfirmAlert = false
    
    // Result summary flow
    @State private var resultSummaryMessage = ""
    @State private var showResultSummary = false

    // Always-skip flow
    @State private var showSkipConfirmation = false
    @State private var skipRemovedCount: Int = 0
    @State private var skipErrorMessage: String?
    @State private var showSkipError = false

    @Environment(\.dismiss) private var dismiss
    
    enum ResolveAction {
        case keepThis
        case keepOther
        case keepBoth
    }

    var body: some View {
        Group {
            if isLoading {
                ProgressView("Loading files…")
            } else if let loadError {
                ContentUnavailableView(
                    "Cannot Load Files",
                    systemImage: "exclamationmark.triangle",
                    description: Text(loadError)
                )
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(conflict.originalPath)
                                .font(.headline)
                            HStack(spacing: 8) {
                                Label(conflict.deviceShortID, systemImage: "laptopcomputer")
                                Label(conflict.formattedConflictDate, systemImage: "clock")
                            }
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .accessibilityElement(children: .combine)
                        }
                        .padding(.horizontal)
                        
                        Divider()
                        
                        Toggle("Show Line-by-Line Diff", isOn: $showLineDiff)
                            .padding(.horizontal)
                            .padding(.bottom, 4)

                        comparisonContent
                    }
                    .padding(.vertical)
                }
            }
        }
        .navigationTitle("Resolve Conflict")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button {
                        skipThisFile()
                    } label: {
                        Label(L10n.tr("Always skip on this iPhone"),
                              systemImage: "line.3.horizontal.decrease.circle")
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                        .accessibilityLabel(L10n.tr("More actions"))
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            if !isLoading && loadError == nil {
                resolutionBar
            }
        }
        .alert("Error", isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
        .confirmationDialog(
            "Resolve Conflict",
            isPresented: $showConfirmAlert,
            titleVisibility: .visible,
            presenting: actionToConfirm
        ) { action in
            Button(confirmButtonTitle(for: action), role: action == .keepBoth ? nil : .destructive) {
                executeAction(action)
            }
            Button("Cancel", role: .cancel) { }
        } message: { action in
            Text(confirmMessage(for: action))
        }
        .alert("Conflict Resolved", isPresented: $showResultSummary) {
            Button("Done") {
                dismiss()
            }
        } message: {
            Text(resultSummaryMessage)
        }
        .alert(L10n.tr("Skipping enabled"), isPresented: $showSkipConfirmation) {
            Button("OK") {
                showSkipConfirmation = false
                dismiss()
            }
        } message: {
            let base = L10n.fmt(
                "'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters.",
                conflict.originalPath
            )
            if skipRemovedCount == 1 {
                Text(base + "\n\n" + L10n.tr("1 existing conflict copy was removed."))
            } else if skipRemovedCount > 1 {
                Text(base + "\n\n" + L10n.fmt("%d existing conflict copies were removed.", skipRemovedCount))
            } else {
                Text(base)
            }
        }
        .alert(L10n.tr("Could not add filter"), isPresented: $showSkipError) {
            Button("OK") { showSkipError = false }
        } message: {
            Text(skipErrorMessage ?? "")
        }
        .task {
            await loadContent()
        }
    }

    /// The bottom resolution bar: full-width, ≥44pt buttons (replacing the tiny
    /// caption2 tab-bar-style icons). Every action routes through confirmAction so
    /// all three confirm before mutating files — including Keep Both, which used
    /// to mutate with no confirmation.
    private var resolutionBar: some View {
        VStack(spacing: VaultSpacing.s) {
            Button {
                confirmAction(.keepThis)
            } label: {
                Label(L10n.tr("Keep This Device's Version"), systemImage: "iphone")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .accessibilityHint(L10n.tr("Discards the version from the other device."))

            HStack(spacing: VaultSpacing.s) {
                Button {
                    confirmAction(.keepBoth)
                } label: {
                    Text(L10n.tr("Keep Both"))
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .accessibilityHint(L10n.tr("Keeps your local file and renames the other device's file."))

                Button(role: .destructive) {
                    confirmAction(.keepOther)
                } label: {
                    Text(L10n.tr("Keep Other"))
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .accessibilityHint(L10n.tr("Overwrites your local file with the version from the other device."))
            }
        }
        .controlSize(.large)
        .tint(.vaultAccent)
        .padding(VaultSpacing.l)
        .background(.bar)
    }

    /// The body of the comparison — line-by-line diff (with a colour/sign legend)
    /// or the two side-by-side file panes. Extracted from `body` to keep each
    /// view expression small enough for the Swift type-checker.
    @ViewBuilder
    private var comparisonContent: some View {
        if showLineDiff {
            VStack(alignment: .leading, spacing: 4) {
                Text("Differences")
                    .font(.subheadline.bold())
                    .padding(.horizontal)
                diffLegend
                LineDiffView(original: originalContent, conflict: conflictContent)
            }
        } else {
            fileSection(
                title: L10n.tr("This Device"),
                icon: "iphone",
                content: originalContent
            )

            fileSection(
                title: L10n.fmt("Other Device (%@)", conflict.deviceShortID),
                icon: "laptopcomputer",
                content: conflictContent
            )
        }
    }

    /// Legend so the +/green and -/red mapping is explicit (colour is never the
    /// only signal — the +/- symbols carry the same meaning for colourblind and
    /// VoiceOver users).
    private var diffLegend: some View {
        HStack(spacing: 12) {
            Label(L10n.tr("Other Device"), systemImage: "plus")
                .foregroundStyle(Color.statusSuccess)
            Label(L10n.tr("This Device"), systemImage: "minus")
                .foregroundStyle(Color.statusError)
        }
        .font(.caption2)
        .padding(.horizontal)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.tr("Added lines come from the other device; removed lines are your version on this device."))
    }

    private func skipThisFile() {
        // Wrap the call in a Task so the button handler returns immediately
        // and SwiftUI can dispatch any UI updates (alert presentation, view
        // dismiss) cleanly. The work itself still runs on the main actor —
        // skipFileAndCleanupConflicts is @MainActor-isolated because it
        // reads/writes SyncthingManager state — so this does not yet move
        // the file I/O off the main thread. A fuller move to a background
        // executor would require splitting the bridge cleanup, rescan, and
        // refresh paths into nonisolated entry points, which is a separate
        // refactor.
        Task { @MainActor in
            let (err, removed) = syncthingManager.skipFileAndCleanupConflicts(
                folderID: folderID,
                originalPath: conflict.originalPath
            )
            if let err {
                skipErrorMessage = err.message
                showSkipError = true
                return
            }
            skipRemovedCount = removed
            showSkipConfirmation = true
        }
    }

    private func fileSection(title: String, icon: String, content: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Label(title, systemImage: icon)
                .font(.subheadline.bold())
                .padding(.horizontal)

            ScrollView(.horizontal, showsIndicators: false) {
                Text(content.isEmpty ? L10n.tr("(empty or unreadable)") : content)
                    .font(.system(.caption, design: .monospaced))
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(Color(.secondarySystemBackground))
            .clipShape(RoundedRectangle(cornerRadius: 8))
            .padding(.horizontal)
        }
    }

    private func loadContent() async {
        let capturedFolderID = folderID
        let capturedConflict = conflict

        let (orig, conf, err): (String, String, String?) = await Task.detached {
            let o = SyncBridgeService.readFileContent(folderID: capturedFolderID, relPath: capturedConflict.originalPath)
            let c = SyncBridgeService.readFileContent(folderID: capturedFolderID, relPath: capturedConflict.conflictPath)
            if let oErr = o.error, let cErr = c.error {
                let oUser = SyncUserError.from(rawMessage: oErr, fallbackTitle: L10n.tr("File Read Failed"))
                let cUser = SyncUserError.from(rawMessage: cErr, fallbackTitle: L10n.tr("File Read Failed"))
                return ("", "", L10n.fmt("Could not read files.\n\n%@\n%@", oUser.message, cUser.message))
            }
            if let oErr = o.error {
                let user = SyncUserError.from(rawMessage: oErr, fallbackTitle: L10n.tr("File Read Failed"))
                return ("", c.content ?? "", L10n.fmt("Could not read original file.\n\n%@", user.userVisibleDescription))
            }
            if let cErr = c.error {
                let user = SyncUserError.from(rawMessage: cErr, fallbackTitle: L10n.tr("File Read Failed"))
                return (o.content ?? "", "", L10n.fmt("Could not read conflict file.\n\n%@", user.userVisibleDescription))
            }
            return (o.content ?? "", c.content ?? "", nil as String?)
        }.value

        originalContent = orig
        conflictContent = conf
        if let err {
            loadError = err
        }
        isLoading = false
    }

    private func confirmAction(_ action: ResolveAction) {
        actionToConfirm = action
        showConfirmAlert = true
    }
    
    private func confirmMessage(for action: ResolveAction) -> String {
        switch action {
        case .keepThis:
            return L10n.tr("This will permanently discard the version from the other device.")
        case .keepOther:
            return L10n.tr("This will permanently discard your local version.")
        case .keepBoth:
            return L10n.tr("Your local version is kept, and the other device’s version is added under a new name. Nothing is discarded.")
        }
    }
    
    private func confirmButtonTitle(for action: ResolveAction) -> String {
        switch action {
        case .keepThis: return L10n.tr("Keep This Device's Version")
        case .keepOther: return L10n.tr("Keep Other Device's Version")
        case .keepBoth: return L10n.tr("Keep Both")
        }
    }

    private func executeAction(_ action: ResolveAction) {
        switch action {
        case .keepThis:
            resolve(keepConflict: false)
        case .keepOther:
            resolve(keepConflict: true)
        case .keepBoth:
            keepBoth()
        }
    }

    private func resolve(keepConflict: Bool) {
        if let err = syncthingManager.resolveConflict(
            folderID: folderID,
            conflictFileName: conflict.conflictPath,
            keepConflict: keepConflict
        ) {
            alertMessage = SyncUserError.from(
                rawMessage: err,
                fallbackTitle: L10n.tr("Conflict Resolution Failed")
            ).userVisibleDescription
            showAlert = true
        } else {
            let filename = (conflict.originalPath as NSString).lastPathComponent
            if keepConflict {
                resultSummaryMessage = L10n.fmt("The file '%@' was overwritten with the version from the other device.", filename)
            } else {
                resultSummaryMessage = L10n.fmt("The file '%@' was kept as your local version. The other device's version was discarded.", filename)
            }
            showResultSummary = true
        }
    }

    private func keepBoth() {
        let (err, newPath) = syncthingManager.keepBothConflict(
            folderID: folderID,
            conflict: conflict
        )
        if let err {
            alertMessage = SyncUserError.from(
                rawMessage: err,
                fallbackTitle: L10n.tr("Conflict Resolution Failed")
            ).userVisibleDescription
            showAlert = true
        } else {
            let filename = (conflict.originalPath as NSString).lastPathComponent
            let renamedFilename = newPath != nil ? (newPath! as NSString).lastPathComponent : L10n.tr("a new name")
            resultSummaryMessage = L10n.fmt(
                "Both versions were kept.\n\nYour local version remains as '%@'.\nThe other device's version was renamed to '%@'.",
                filename,
                renamedFilename
            )
            showResultSummary = true
        }
    }
}

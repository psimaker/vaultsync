import SwiftUI

struct ConflictDiffView: View {
    let folderID: String
    let conflict: SyncthingManager.ConflictInfo
    let syncthingManager: SyncthingManager

    @State private var originalContent = ""
    @State private var conflictContent = ""
    @State private var isLoading = true
    @State private var loadError: String?
    /// Set when either version exceeds the bridge's read bound (#184):
    /// nothing was loaded, the comparison happens in Obsidian.
    @State private var tooLargeBytes: Int64?
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
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    /// At the accessibility text sizes a pinned three-button bar would take
    /// half the screen and leave the two versions a sliver to scroll in —
    /// the actions move to the end of the page instead.
    private var actionsInline: Bool { dynamicTypeSize.isAccessibilitySize }
    
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
            } else if let tooLargeBytes {
                if actionsInline {
                    VaultPage {
                        tooLargeContent(bytes: tooLargeBytes)
                        resolutionButtons
                    }
                } else {
                    tooLargeContent(bytes: tooLargeBytes)
                }
            } else {
                VaultPage {
                    conflictHeader

                    Toggle(L10n.tr("Show Line-by-Line Diff"), isOn: $showLineDiff)
                        .font(.subheadline)
                        .foregroundStyle(Color.vaultLabel)
                        .tint(Color.vaultAccent)
                        .padding(.horizontal, VaultSpacing.l)
                        .frame(minHeight: VaultMetrics.compactButtonHeight)
                        .vaultCard()

                    comparisonContent

                    if actionsInline {
                        resolutionButtons
                            .padding(.top, VaultSpacing.s)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color.vaultBackground.ignoresSafeArea())
        .navigationTitle("Resolve Conflict")
        .navigationBarTitleDisplayMode(.inline)
        #if DEBUG
        .onAppear {
            // LAB: present the resolve consent immediately for the UI-audit
            // fixture run (#64); reachable only via launch argument.
            if UIAuditFixture.active == UIAuditFixture.conflictResolveConsent {
                actionToConfirm = .keepThis
                showConfirmAlert = true
            }
        }
        #endif
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
            if !isLoading && loadError == nil && !actionsInline {
                resolutionBar
            }
        }
        .alert("Error", isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
        // Same iOS-26 rendering defect as the consent dialogs: a
        // .confirmationDialog hides its cancel-role button there, and one of
        // these actions discards a version of a note — .alert keeps Cancel
        // visible on every OS version (#64, decision 011).
        .alert(
            "Resolve Conflict",
            isPresented: $showConfirmAlert,
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
        resolutionButtons
            .padding(.horizontal, VaultSpacing.gutter)
            .padding(.vertical, VaultSpacing.m)
            .frame(maxWidth: VaultMetrics.readableWidth)
            .frame(maxWidth: .infinity)
            .background(.bar)
    }

    private var resolutionButtons: some View {
        VStack(spacing: VaultSpacing.s) {
            Button {
                confirmAction(.keepThis)
            } label: {
                Label(L10n.tr("Keep This Device's Version"), systemImage: "iphone")
            }
            .buttonStyle(.vault(.primary, compact: true))
            .accessibilityHint(L10n.tr("Discards the version from the other device."))

            VaultButtonRow {
                Button {
                    confirmAction(.keepBoth)
                } label: {
                    Text(L10n.tr("Keep Both"))
                }
                .buttonStyle(.vault(.tinted, compact: true))
                .accessibilityHint(L10n.tr("Keeps your local file and renames the other device's file."))

                Button(role: .destructive) {
                    confirmAction(.keepOther)
                } label: {
                    Text(L10n.tr("Keep Other"))
                }
                .buttonStyle(.vault(.destructive, compact: true))
                .accessibilityHint(L10n.tr("Overwrites your local file with the version from the other device."))
            }
        }
    }

    /// File, other device and conflict time — the card the canvas opens the
    /// conflict with.
    private var conflictHeader: some View {
        HStack(alignment: .top, spacing: VaultSpacing.m) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.title3)
                .foregroundStyle(Color.statusAttention)
                .frame(width: 28)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                Text(conflict.originalPath)
                    .font(.headline)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
                // Wraps label by label at large text sizes instead of
                // breaking the device ID and the date mid-word.
                VaultFlowLayout(spacing: VaultSpacing.s) {
                    Label(conflict.deviceShortID, systemImage: "laptopcomputer")
                    Label(conflict.formattedConflictDate, systemImage: "clock")
                }
                .font(.footnote)
                .foregroundStyle(Color.vaultSecondaryLabel)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(VaultSpacing.l)
        .vaultCard()
        .accessibilityElement(children: .combine)
    }

    /// The body of the comparison — line-by-line diff (with a colour/sign legend)
    /// or the two side-by-side file panes. Extracted from `body` to keep each
    /// view expression small enough for the Swift type-checker.
    @ViewBuilder
    private var comparisonContent: some View {
        if showLineDiff {
            VStack(alignment: .leading, spacing: VaultSpacing.s) {
                Text("Differences")
                    .font(.subheadline.bold())
                    .foregroundStyle(Color.vaultLabel)
                diffLegend
                LineDiffView(original: originalContent, conflict: conflictContent)
            }
            .padding(VaultSpacing.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .vaultCard()
        } else {
            fileSection(
                title: L10n.tr("This Device"),
                icon: "iphone",
                content: originalContent,
                emphasized: true
            )

            fileSection(
                title: L10n.fmt("Other Device (%@)", conflict.deviceShortID),
                icon: "laptopcomputer",
                content: conflictContent,
                emphasized: false
            )
        }
    }

    /// Legend so the +/green and -/red mapping is explicit (colour is never the
    /// only signal — the +/- symbols carry the same meaning for colourblind and
    /// VoiceOver users).
    private var diffLegend: some View {
        HStack(spacing: 12) {
            Label(L10n.tr("Other Device"), systemImage: "plus")
                .foregroundStyle(Color.statusSuccessText)
            Label(L10n.tr("This Device"), systemImage: "minus")
                .foregroundStyle(Color.statusErrorText)
        }
        .font(.caption2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.tr("Added lines come from the other device; removed lines are your version on this device."))
    }

    private func skipThisFile() {
        // The button handler returns immediately; the ignore write, the
        // conflict-copy cleanup and the rescan run off the main actor inside
        // the manager (#184), and only the alert state comes back here.
        Task { @MainActor in
            let (err, removed) = await syncthingManager.skipFileAndCleanupConflicts(
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

    /// One version of the note. This device's version carries the accent
    /// outline (the canvas's "this iPhone" box), the other device's copy the
    /// hairline.
    private func fileSection(title: String, icon: String, content: String, emphasized: Bool) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            Label(title, systemImage: icon)
                .font(.subheadline.bold())
                .foregroundStyle(Color.vaultLabel)

            ScrollView(.horizontal, showsIndicators: false) {
                Text(content.isEmpty ? L10n.tr("(empty or unreadable)") : content)
                    .font(.vaultMono(.caption))
                    .foregroundStyle(Color.vaultLabel)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(VaultSpacing.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.vaultSurface, in: RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous)
                .strokeBorder(emphasized ? Color.vaultAccent : Color.vaultHairline, lineWidth: emphasized ? 1.5 : 1)
        )
    }

    /// The note exceeds the bridge's read bound (#184, decision 041): nothing
    /// was loaded, so nothing is rendered or diffed here. The resolution bar
    /// stays — the choice remains manual (decision 028); only the comparison
    /// moves to Obsidian.
    private func tooLargeContent(bytes: Int64) -> some View {
        ContentUnavailableView {
            Label(L10n.tr("Too Large to Compare"), systemImage: "doc.text.magnifyingglass")
        } description: {
            Text(L10n.fmt(
                "This note is %@ — above the %@ VaultSync loads for a comparison. Compare the versions in Obsidian, then choose which one to keep below.",
                Self.formattedBytes(bytes),
                Self.formattedBytes(SyncBridgeService.maxReadFileBytes())
            ))
        }
    }

    private static func formattedBytes(_ bytes: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }

    struct LoadedContent: Equatable, Sendable {
        var original = ""
        var conflict = ""
        var tooLargeBytes: Int64?
        var error: String?
    }

    private func loadContent() async {
        #if DEBUG
        // LAB: the design-preview fixture runs without an engine, so there
        // is nothing to read — show its fictional note bodies (#187).
        if UIAuditFixture.active == UIAuditFixture.designPreview {
            originalContent = DesignPreviewFixture.conflictLocalText
            conflictContent = DesignPreviewFixture.conflictRemoteText
            isLoading = false
            return
        }
        #endif
        let capturedFolderID = folderID
        let capturedConflict = conflict

        let loaded: LoadedContent = await Task.detached {
            let limit = SyncBridgeService.maxReadFileBytes()
            let o = Self.boundedRead(folderID: capturedFolderID, relPath: capturedConflict.originalPath, limit: limit)
            let c = Self.boundedRead(folderID: capturedFolderID, relPath: capturedConflict.conflictPath, limit: limit)
            return Self.loadedContent(original: o, conflict: c)
        }.value

        originalContent = loaded.original
        conflictContent = loaded.conflict
        tooLargeBytes = loaded.tooLargeBytes
        if let err = loaded.error {
            loadError = err
        }
        isLoading = false
    }

    /// What the screen shows for a pair of reads. A failed read wins over an
    /// oversized one: the failure state hides the resolution actions, and a
    /// version that could not be read must never be discarded or overwritten
    /// by a choice made without seeing it (#184).
    nonisolated static func loadedContent(
        original o: SyncBridgeService.ConflictFileRead,
        conflict c: SyncBridgeService.ConflictFileRead
    ) -> LoadedContent {
        switch (o, c) {
        case (.failed(let oErr), .failed(let cErr)):
            let oUser = SyncUserError.from(rawMessage: oErr, fallbackTitle: L10n.tr("File Read Failed"))
            let cUser = SyncUserError.from(rawMessage: cErr, fallbackTitle: L10n.tr("File Read Failed"))
            return LoadedContent(error: L10n.fmt("Could not read files.\n\n%@\n%@", oUser.message, cUser.message))
        case (.failed(let oErr), _):
            let user = SyncUserError.from(rawMessage: oErr, fallbackTitle: L10n.tr("File Read Failed"))
            var loaded = LoadedContent(error: L10n.fmt("Could not read original file.\n\n%@", user.userVisibleDescription))
            if case .content(let cText) = c { loaded.conflict = cText }
            return loaded
        case (_, .failed(let cErr)):
            let user = SyncUserError.from(rawMessage: cErr, fallbackTitle: L10n.tr("File Read Failed"))
            var loaded = LoadedContent(error: L10n.fmt("Could not read conflict file.\n\n%@", user.userVisibleDescription))
            if case .content(let oText) = o { loaded.original = oText }
            return loaded
        case (.tooLarge(let oBytes), .tooLarge(let cBytes)):
            // Either version over the bound: nothing to compare here. Report
            // the larger size so the message matches what the user sees in
            // Obsidian.
            return LoadedContent(tooLargeBytes: max(oBytes, cBytes))
        case (.tooLarge(let bytes), .content), (.content, .tooLarge(let bytes)):
            return LoadedContent(tooLargeBytes: bytes)
        case (.content(let oText), .content(let cText)):
            return LoadedContent(original: oText, conflict: cText)
        }
    }

    /// The bridge enforces the bound; this side re-checks the bytes it got,
    /// so a stale bridge can never hand the diff a note above the limit
    /// (same bound in both layers, decision 041).
    private nonisolated static func boundedRead(folderID: String, relPath: String, limit: Int64) -> SyncBridgeService.ConflictFileRead {
        let read = SyncBridgeService.readFileContent(folderID: folderID, relPath: relPath)
        if case .content(let text) = read, Int64(text.utf8.count) > limit {
            return .tooLarge(bytes: Int64(text.utf8.count))
        }
        return read
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

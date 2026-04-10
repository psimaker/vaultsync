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
    
    @Environment(\.dismiss) private var dismiss
    
    enum ResolveAction {
        case keepThis
        case keepOther
        case keepBoth
    }

    var body: some View {
        Group {
            if isLoading {
                ProgressView("Loading files...")
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
                                Label(conflict.conflictDate, systemImage: "clock")
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

                        if showLineDiff {
                            VStack(alignment: .leading, spacing: 4) {
                                Text("Differences")
                                    .font(.subheadline.bold())
                                    .padding(.horizontal)
                                LineDiffView(original: originalContent, conflict: conflictContent)
                            }
                        } else {
                            fileSection(
                                title: "This Device",
                                icon: "iphone",
                                content: originalContent
                            )

                            fileSection(
                                title: "Other Device (\(conflict.deviceShortID))",
                                icon: "laptopcomputer",
                                content: conflictContent
                            )
                        }
                    }
                    .padding(.vertical)
                }
            }
        }
        .navigationTitle("Resolve Conflict")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .bottomBar) {
                Button {
                    confirmAction(.keepThis)
                } label: {
                    VStack(spacing: 2) {
                        Image(systemName: "iphone")
                            .accessibilityHidden(true)
                        Text("Keep This")
                            .font(.caption2)
                    }
                }
                .accessibilityLabel("Keep this device version")
                .accessibilityHint("Discards the version from the other device.")

                Spacer()

                Button {
                    executeAction(.keepBoth)
                } label: {
                    VStack(spacing: 2) {
                        Image(systemName: "doc.on.doc")
                            .accessibilityHidden(true)
                        Text("Keep Both")
                            .font(.caption2)
                    }
                }
                .accessibilityLabel("Keep both versions")
                .accessibilityHint("Keeps your local file and renames the other device's file.")

                Spacer()

                Button {
                    confirmAction(.keepOther)
                } label: {
                    VStack(spacing: 2) {
                        Image(systemName: "laptopcomputer")
                            .accessibilityHidden(true)
                        Text("Keep Other")
                            .font(.caption2)
                    }
                }
                .accessibilityLabel("Keep other device version")
                .accessibilityHint("Overwrites your local file with the version from the other device.")
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
            Button(confirmButtonTitle(for: action), role: .destructive) {
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
        .task {
            await loadContent()
        }
    }

    private func fileSection(title: String, icon: String, content: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Label(title, systemImage: icon)
                .font(.subheadline.bold())
                .padding(.horizontal)

            ScrollView(.horizontal, showsIndicators: false) {
                Text(content.isEmpty ? "(empty or unreadable)" : content)
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
                let oUser = SyncUserError.from(rawMessage: oErr, fallbackTitle: "File Read Failed")
                let cUser = SyncUserError.from(rawMessage: cErr, fallbackTitle: "File Read Failed")
                return ("", "", "Could not read files.\n\n\(oUser.message)\n\(cUser.message)")
            }
            if let oErr = o.error {
                let user = SyncUserError.from(rawMessage: oErr, fallbackTitle: "File Read Failed")
                return ("", c.content ?? "", "Could not read original file.\n\n\(user.userVisibleDescription)")
            }
            if let cErr = c.error {
                let user = SyncUserError.from(rawMessage: cErr, fallbackTitle: "File Read Failed")
                return (o.content ?? "", "", "Could not read conflict file.\n\n\(user.userVisibleDescription)")
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
            return "This will permanently discard the version from the other device."
        case .keepOther:
            return "This will permanently discard your local version."
        case .keepBoth:
            return ""
        }
    }
    
    private func confirmButtonTitle(for action: ResolveAction) -> String {
        switch action {
        case .keepThis: return "Keep This Device's Version"
        case .keepOther: return "Keep Other Device's Version"
        case .keepBoth: return "Keep Both"
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
                fallbackTitle: "Conflict Resolution Failed"
            ).userVisibleDescription
            showAlert = true
        } else {
            let filename = (conflict.originalPath as NSString).lastPathComponent
            if keepConflict {
                resultSummaryMessage = "The file '\(filename)' was overwritten with the version from the other device."
            } else {
                resultSummaryMessage = "The file '\(filename)' was kept as your local version. The other device's version was discarded."
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
                fallbackTitle: "Conflict Resolution Failed"
            ).userVisibleDescription
            showAlert = true
        } else {
            let filename = (conflict.originalPath as NSString).lastPathComponent
            let renamedFilename = newPath != nil ? (newPath! as NSString).lastPathComponent : "a new name"
            resultSummaryMessage = "Both versions were kept.\n\nYour local version remains as '\(filename)'.\nThe other device's version was renamed to '\(renamedFilename)'."
            showResultSummary = true
        }
    }
}

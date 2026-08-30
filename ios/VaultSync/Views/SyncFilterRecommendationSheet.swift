import SwiftUI

struct SyncFilterRecommendationSheet: View {
    let folderID: String
    let syncthingManager: SyncthingManager
    private let scanner: @Sendable (String) async -> KnownPatternScanResult
    @Environment(\.dismiss) private var dismiss

    @State private var detected: [DetectedPattern] = []
    @State private var enabledPresetIDs: Set<String> = Set(IgnorePreset.recommended.map(\.id))
    @State private var enabledDetectedPatterns: Set<String> = []
    @State private var scanGeneration = FilterScanGeneration()
    @State private var applyErrorMessage: String?

    init(
        folderID: String,
        syncthingManager: SyncthingManager,
        scanner: @escaping @Sendable (String) async -> KnownPatternScanResult = { capturedFolderID in
            await Task.detached(priority: .utility) {
                SyncthingManager.scanFolderForKnownPatterns(folderID: capturedFolderID)
            }.value
        }
    ) {
        self.folderID = folderID
        self.syncthingManager = syncthingManager
        self.scanner = scanner
    }

    var body: some View {
        NavigationStack {
            Group {
                if allowsChanges {
                    List {
                        Section {
                            Text(L10n.tr("Skip these on this iPhone? You can change this anytime in Sync Filters."))
                                .font(.callout)
                                .foregroundStyle(.secondary)
                        }

                        Section(header: Text(L10n.tr("Recommended"))) {
                            ForEach(IgnorePreset.recommended) { preset in
                                presetToggle(preset)
                            }
                        }

                        if !detected.isEmpty {
                            Section(header: Text(L10n.tr("Found in this vault"))) {
                                ForEach(detected) { item in
                                    detectedToggle(item)
                                }
                            }
                        }
                    }
                } else {
                    ContentUnavailableView {
                        Label(safetyError.title, systemImage: "lock.fill")
                    } description: {
                        Text(safetyError.message)
                        Text(safetyError.remediation)
                    }
                }
            }
            .navigationTitle(L10n.tr("Sync Filters"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if allowsChanges {
                    ToolbarItem(placement: .topBarLeading) {
                        Button(L10n.tr("Skip")) {
                            syncthingManager.markRecommendationSheetShown(folderID: folderID)
                            dismiss()
                        }
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button(L10n.tr("Done")) {
                            if let err = apply() {
                                applyErrorMessage = err.message
                                return
                            }
                            syncthingManager.markRecommendationSheetShown(folderID: folderID)
                            dismiss()
                        }
                        .bold()
                    }
                } else {
                    ToolbarItem(placement: .topBarLeading) {
                        Button(L10n.tr("Cancel")) {
                            applyErrorMessage = nil
                            dismiss()
                        }
                    }
                }
            }
            .task(id: scanTaskID) {
                await scan()
            }
            .alert(L10n.tr("Could not save filters"), isPresented: errorBinding) {
                Button(L10n.tr("OK")) { applyErrorMessage = nil }
            } message: {
                Text(applyErrorMessage ?? "")
            }
        }
    }

    private var safetyState: ConflictSafetyPolicy.State {
        syncthingManager.conflictSafetyState(folderID: folderID)
    }

    private var scanTaskID: FilterScanTaskID {
        FilterScanTaskID(folderID: folderID, safetyState: safetyState)
    }

    private var allowsChanges: Bool {
        ConflictSafetyPolicy.allowsMutation(for: safetyState)
    }

    private var safetyError: SyncUserError {
        SyncUserError.conflictSafetyError(for: safetyState)
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { applyErrorMessage != nil }, set: { if !$0 { applyErrorMessage = nil } })
    }

    private func presetToggle(_ preset: IgnorePreset) -> some View {
        let isOn = Binding(
            get: { enabledPresetIDs.contains(preset.id) },
            set: { newValue in
                if newValue {
                    enabledPresetIDs.insert(preset.id)
                } else {
                    enabledPresetIDs.remove(preset.id)
                }
            }
        )
        return Toggle(isOn: isOn) {
            VStack(alignment: .leading, spacing: 2) {
                Text(L10n.tr(preset.label)).font(.body)
                Text(L10n.tr(preset.description)).font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private func detectedToggle(_ item: DetectedPattern) -> some View {
        let preset = IgnorePreset.preset(forDetectedPattern: item.pattern)
        let isOn = Binding(
            get: {
                if let preset { return enabledPresetIDs.contains(preset.id) }
                return enabledDetectedPatterns.contains(item.pattern)
            },
            set: { newValue in
                if let preset {
                    if newValue {
                        enabledPresetIDs.insert(preset.id)
                    } else {
                        enabledPresetIDs.remove(preset.id)
                    }
                } else {
                    if newValue {
                        enabledDetectedPatterns.insert(item.pattern)
                    } else {
                        enabledDetectedPatterns.remove(item.pattern)
                    }
                }
            }
        )
        return Toggle(isOn: isOn) {
            VStack(alignment: .leading, spacing: 2) {
                Text(L10n.tr(item.label)).font(.body)
                Text(formattedSize(item)).font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private func scan() async {
        guard safetyState == .clear else {
            scanGeneration.invalidate()
            detected.removeAll()
            enabledDetectedPatterns.removeAll()
            enabledPresetIDs = Set(IgnorePreset.recommended.map(\.id))
            return
        }
        guard let token = scanGeneration.begin(
            folderID: folderID,
            safetyState: safetyState
        ) else { return }
        detected.removeAll()
        enabledDetectedPatterns.removeAll()
        enabledPresetIDs = Set(IgnorePreset.recommended.map(\.id))

        let capturedFolderID = folderID
        let scanResult = await scanner(capturedFolderID)
        let scanComplete: Bool
        switch scanResult {
        case .complete:
            scanComplete = true
        case .unavailable:
            scanComplete = false
        }
        let completion = scanGeneration.complete(
            token: token,
            currentFolderID: folderID,
            currentSafetyState: safetyState,
            taskCancelled: Task.isCancelled,
            scanComplete: scanComplete
        )
        guard completion == .commit,
              case let .complete(result) = scanResult else { return }

        detected = result
        for item in result {
            if let preset = IgnorePreset.preset(forDetectedPattern: item.pattern) {
                enabledPresetIDs.insert(preset.id)
            } else {
                enabledDetectedPatterns.insert(item.pattern)
            }
        }
    }

    /// Delegate the deselect-aware, safe-read apply to SyncthingManager so
    /// the sheet stays presentation-only and the read-modify-write logic
    /// lives next to the rest of the filter API.
    private func apply() -> SyncUserError? {
        syncthingManager.applyRecommendedFilters(
            folderID: folderID,
            enabledPresetIDs: enabledPresetIDs,
            detectedPatterns: detected.map(\.pattern),
            enabledDetectedPatterns: enabledDetectedPatterns
        )
    }

    private func formattedSize(_ item: DetectedPattern) -> String {
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useMB, .useGB, .useKB]
        formatter.countStyle = .file
        let size = formatter.string(fromByteCount: item.sizeBytes)
        return L10n.fmt("%@ — %d files", size, item.fileCount)
    }
}

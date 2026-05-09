import SwiftUI

struct SyncFilterRecommendationSheet: View {
    let folderID: String
    let folderLabel: String
    let syncthingManager: SyncthingManager
    @Environment(\.dismiss) private var dismiss

    @State private var detected: [DetectedPattern] = []
    @State private var enabledPresetIDs: Set<String> = Set(IgnorePreset.recommended.map(\.id))
    @State private var enabledDetectedPatterns: Set<String> = []
    @State private var hasScanned = false
    @State private var applyErrorMessage: String?

    var body: some View {
        NavigationStack {
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
            .navigationTitle(L10n.tr("Sync Filters"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
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
            }
            .task { await scan() }
            .alert(L10n.tr("Could not save filters"), isPresented: errorBinding) {
                Button(L10n.tr("OK")) { applyErrorMessage = nil }
            } message: {
                Text(applyErrorMessage ?? "")
            }
        }
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
        guard !hasScanned else { return }
        hasScanned = true
        let id = folderID
        let result = await Task.detached {
            SyncthingManager.scanFolderForKnownPatterns(folderID: id)
        }.value
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

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

    /// Compute the final `.stignore` content based on the user's choices, then
    /// write it. Any pattern that the sheet manages (presets + detected items)
    /// is removed first, then re-added only if currently enabled. This makes
    /// deselect actually take effect — including for the recommended patterns
    /// that addFolder() silently auto-applied just before the sheet appeared.
    /// Custom/unmanaged patterns the user added previously are preserved.
    private func apply() -> SyncUserError? {
        let existing = syncthingManager.ignorePatterns(folderID: folderID)
        let managed = Set(IgnorePreset.all.flatMap(\.patterns))
            .union(detected.map(\.pattern))

        var patterns = existing.filter { !managed.contains($0) }

        for preset in IgnorePreset.all where enabledPresetIDs.contains(preset.id) {
            for pattern in preset.patterns where !patterns.contains(pattern) {
                patterns.append(pattern)
            }
        }
        for pattern in enabledDetectedPatterns where !patterns.contains(pattern) {
            patterns.append(pattern)
        }
        return syncthingManager.setIgnorePatterns(folderID: folderID, patterns: patterns)
    }

    private func formattedSize(_ item: DetectedPattern) -> String {
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useMB, .useGB, .useKB]
        formatter.countStyle = .file
        let size = formatter.string(fromByteCount: item.sizeBytes)
        return L10n.fmt("%@ — %d files", size, item.fileCount)
    }
}

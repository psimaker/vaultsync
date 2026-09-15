import SwiftUI

struct IgnorePatternsView: View {
    let folderID: String
    let syncthingManager: SyncthingManager

    @State private var ignoredPatterns: Set<String> = []
    @State private var detected: [DetectedPattern] = []
    @State private var newPattern: String = ""
    @State private var alertMessage: String?
    @State private var hasLoadedScan = false
    @State private var filtersUnavailable = false

    var body: some View {
        List {
            if filtersUnavailable {
                unavailableSection
            } else {
                recommendedSection
                if !detected.isEmpty {
                    foundSection
                }
                otherPresetsSection
                customSection
            }
            footerSection
        }
        .navigationTitle(L10n.tr("Sync Filters"))
        .navigationBarTitleDisplayMode(.inline)
        .task { await initialLoad() }
        .alert(L10n.tr("Sync Filter Error"), isPresented: errorBinding) {
            Button(L10n.tr("OK")) { alertMessage = nil }
        } message: { Text(alertMessage ?? "") }
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { alertMessage != nil }, set: { if !$0 { alertMessage = nil } })
    }

    /// The filter file could not be read (#182). Rendering every preset as
    /// "off" would claim there are no filters while filters may exist, and a
    /// toggle would then rewrite the file from that false picture — so the
    /// toggles stay hidden until a read succeeds.
    private var unavailableSection: some View {
        Section {
            Label(L10n.tr("Sync filters are unavailable"), systemImage: "exclamationmark.triangle")
                .foregroundStyle(Color.statusAttention)
            Text(L10n.tr("The filter file for this vault could not be read. Make sure the vault is still accessible, then try again."))
                .font(.caption)
                .foregroundStyle(.secondary)
            Button(L10n.tr("Try Again")) { reloadPatterns() }
        }
    }

    private var recommendedSection: some View {
        Section(header: Text(L10n.tr("Recommended"))) {
            ForEach(IgnorePreset.recommended) { preset in
                presetRow(preset)
            }
        }
    }

    private var foundSection: some View {
        Section(header: Text(L10n.tr("Found in this vault"))) {
            ForEach(detected) { item in
                if let preset = IgnorePreset.preset(forDetectedPattern: item.pattern) {
                    presetRow(preset, sizeOverride: item)
                } else {
                    Toggle(isOn: detectedToggleBinding(for: item)) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(L10n.tr(item.label)).font(.body)
                            Text(formattedSize(item))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
    }

    private var otherPresetsSection: some View {
        let recommendedIDs = Set(IgnorePreset.recommended.map(\.id))
        let detectedIDs = Set(detected.compactMap {
            IgnorePreset.preset(forDetectedPattern: $0.pattern)?.id
        })
        let others = IgnorePreset.all.filter {
            !recommendedIDs.contains($0.id) && !detectedIDs.contains($0.id)
        }
        return Section(header: Text(L10n.tr("Other presets"))) {
            ForEach(others) { preset in
                presetRow(preset)
            }
        }
    }

    private var customSection: some View {
        Section(header: Text(L10n.tr("Custom patterns"))) {
            ForEach(customEntries, id: \.self) { entry in
                VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                    Text(entry.original)
                        .font(.vaultMono(.body))
                    if entry.hasConflictGlob {
                        Text(L10n.tr("+ conflict copies"))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .onDelete(perform: deleteCustom)

            HStack(alignment: .top) {
                TextField(L10n.tr("Add pattern (e.g. *.tmp)"), text: $newPattern, axis: .vertical)
                    .font(.vaultMono(.body))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                    .lineLimit(1...6)
                Button(L10n.tr("Add")) { addCustom() }
                    .disabled(IgnorePatternInput.parse(newPattern).isEmpty)
            }
        }
    }

    private var footerSection: some View {
        Section {
            ExternalLinkButton(titleKey: "How filters work", url: DocURL.syncthingIgnoring)
        }
    }

    // MARK: - Derived state

    private var customEntries: [SkipFamilyEntry] {
        let presetPatterns = Set(IgnorePreset.all.flatMap(\.patterns))
        let lines = ignoredPatterns
            .filter { !presetPatterns.contains($0) }
            .sorted()
        return SkipFamilyGrouping.group(customLines: lines)
    }

    private func isActive(_ preset: IgnorePreset) -> Bool {
        preset.patterns.allSatisfy { ignoredPatterns.contains($0) }
    }

    // MARK: - Rows

    private func presetRow(_ preset: IgnorePreset, sizeOverride: DetectedPattern? = nil) -> some View {
        Toggle(isOn: bindingFor(preset)) {
            VStack(alignment: .leading, spacing: 2) {
                Text(L10n.tr(preset.label)).font(.body)
                if let sizeOverride {
                    Text(formattedSize(sizeOverride))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    Text(L10n.tr(preset.description))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private func bindingFor(_ preset: IgnorePreset) -> Binding<Bool> {
        Binding(
            get: { isActive(preset) },
            set: { newValue in
                if let err = syncthingManager.togglePreset(preset, folderID: folderID, enabled: newValue) {
                    alertMessage = err.message
                }
                reloadPatterns()
            }
        )
    }

    private func detectedToggleBinding(for item: DetectedPattern) -> Binding<Bool> {
        Binding(
            get: { ignoredPatterns.contains(item.pattern) },
            set: { newValue in
                if newValue {
                    if let err = syncthingManager.addIgnorePattern(item.pattern, folderID: folderID) {
                        alertMessage = err.message
                    }
                } else {
                    if let err = syncthingManager.removeIgnorePatterns([item.pattern], folderID: folderID) {
                        alertMessage = err.message
                    }
                }
                reloadPatterns()
            }
        )
    }

    // MARK: - Mutations

    private func addCustom() {
        // Accept a multi-line / multi-pattern paste: one pattern per line, with
        // blank and `//` comment lines dropped. See IgnorePatternInput.parse.
        let patterns = IgnorePatternInput.parse(newPattern)
        guard !patterns.isEmpty else {
            newPattern = ""
            return
        }
        if let err = syncthingManager.addIgnorePatterns(patterns, folderID: folderID) {
            alertMessage = err.message
            return
        }
        newPattern = ""
        reloadPatterns()
    }

    private func deleteCustom(at offsets: IndexSet) {
        let visible = customEntries
        let toRemoveLines: [String] = offsets.flatMap { index -> [String] in
            guard index < visible.count else { return [] }
            return visible[index].underlyingLines
        }
        guard !toRemoveLines.isEmpty else { return }
        if let err = syncthingManager.removeIgnorePatterns(toRemoveLines, folderID: folderID) {
            alertMessage = err.message
            return
        }
        reloadPatterns()
    }

    // MARK: - Loading

    private func initialLoad() async {
        reloadPatterns()
        if !hasLoadedScan {
            hasLoadedScan = true
            let id = folderID
            let scanResult = await Task.detached {
                SyncthingManager.scanFolderForKnownPatterns(folderID: id)
            }.value
            detected = scanResult
        }
    }

    private func reloadPatterns() {
        if let patterns = syncthingManager.ignorePatterns(folderID: folderID) {
            ignoredPatterns = Set(patterns)
            filtersUnavailable = false
        } else {
            ignoredPatterns = []
            filtersUnavailable = true
        }
    }

    // MARK: - Formatting

    private func formattedSize(_ item: DetectedPattern) -> String {
        let formatter = ByteCountFormatter()
        formatter.allowedUnits = [.useMB, .useGB, .useKB]
        formatter.countStyle = .file
        let size = formatter.string(fromByteCount: item.sizeBytes)
        return L10n.fmt("%@ — %d files", size, item.fileCount)
    }
}

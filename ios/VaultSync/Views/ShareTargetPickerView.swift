import SwiftUI

/// Sheet for issue #52: choose where a pending share syncs on this iPhone —
/// an existing *empty* vault under the Obsidian directory, or a new folder
/// with a custom name (decoupling the local vault name from the share label,
/// which stays unchanged on the offering devices). Auto-accept remains the
/// default; this sheet is the per-share manual path.
///
/// The picker itself carries no safety burden beyond honest presentation:
/// every confirmed choice is re-validated in `VaultManager` (overlap +
/// emptiness) and by the Go engine's overlap hard floor.
struct ShareTargetPickerView: View {
    let shareLabel: String
    /// The folder name auto-accept would use — prefills the new-folder field.
    let defaultName: String
    /// Existing empty vaults eligible as targets (`eligibleShareTargets`).
    let eligibleVaults: [String]
    /// Attempt the accept; returns a user-facing error, or nil on success.
    var onConfirm: (String) -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var selectedVault: String?
    @State private var newFolderName: String
    @State private var errorMessage: String?

    init(
        shareLabel: String,
        defaultName: String,
        eligibleVaults: [String],
        onConfirm: @escaping (String) -> String?
    ) {
        self.shareLabel = shareLabel
        self.defaultName = defaultName
        self.eligibleVaults = eligibleVaults
        self.onConfirm = onConfirm
        _newFolderName = State(initialValue: defaultName)
    }

    /// A picked existing vault wins; otherwise the (trimmed) new-folder name.
    private var effectiveTarget: String {
        selectedVault ?? newFolderName.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Text(L10n.fmt("Choose where \"%@\" syncs on this iPhone. The share keeps its name on your other devices.", shareLabel))
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }

                Section {
                    if eligibleVaults.isEmpty {
                        Text(L10n.tr("No empty vaults found. Create the vault in Obsidian first, or use a new folder below."))
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    } else {
                        ForEach(eligibleVaults, id: \.self) { vault in
                            Button {
                                selectedVault = selectedVault == vault ? nil : vault
                            } label: {
                                HStack {
                                    Label(vault, systemImage: "folder")
                                    Spacer()
                                    if selectedVault == vault {
                                        Image(systemName: "checkmark")
                                            .foregroundStyle(Color.vaultAccent)
                                            .accessibilityHidden(true)
                                    }
                                }
                            }
                            .tint(.primary)
                            .accessibilityAddTraits(selectedVault == vault ? .isSelected : [])
                        }
                    }
                } header: {
                    Text(L10n.tr("Existing Empty Vaults"))
                } footer: {
                    Text(L10n.tr("Only vaults without any notes yet can be linked to a share."))
                }

                Section {
                    TextField(L10n.tr("Folder name"), text: $newFolderName)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .onChange(of: newFolderName) { _, _ in
                            // Typing a name is choosing the new-folder option.
                            selectedVault = nil
                        }
                } header: {
                    Text(L10n.tr("New Folder"))
                } footer: {
                    Text(L10n.tr("Creates a folder with this name in your Obsidian directory and syncs the share into it."))
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.callout)
                            .foregroundStyle(Color.statusError)
                    }
                }
            }
            .navigationTitle(L10n.tr("Choose Vault"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button(L10n.tr("Cancel")) { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button(L10n.tr("Link Share")) {
                        if let err = onConfirm(effectiveTarget) {
                            errorMessage = err
                            return
                        }
                        dismiss()
                    }
                    .bold()
                    .disabled(effectiveTarget.isEmpty)
                }
            }
        }
    }
}

import Foundation
import Testing
@testable import VaultSync

@Suite("Manual share target & user-removed re-accept (#52)")
struct ManualShareTargetTests {

    /// Production passes `FolderPathReconciler.canonical`; tests inject identity
    /// so the decision logic is exercised without touching the filesystem. The
    /// functions lowercase internally, so occupied entries are supplied lowercased.
    private let identity: (String) -> String = { $0 }

    private func occupied(_ paths: String...) -> Set<String> {
        Set(paths.map { $0.lowercased() })
    }

    private func validate(
        root: String = "/Obsidian",
        rootIsVault: Bool = false,
        name: String,
        occupied: Set<String> = [],
        entries: [String]? = nil
    ) -> ShareTargetDecision {
        VaultManager.validateManualTarget(
            rawRoot: root,
            rootIsVault: rootIsVault,
            name: name,
            occupiedCanonLower: occupied,
            targetEntries: entries,
            canonicalize: identity
        )
    }

    private func resolve(
        manualTarget: String?,
        root: String = "/Obsidian",
        rootIsVault: Bool = false,
        name: String,
        occupied: Set<String> = []
    ) -> ShareTargetDecision {
        VaultManager.resolveAcceptPath(
            manualTarget: manualTarget,
            manualTargetConsented: true,
            rawRoot: root,
            rootIsVault: rootIsVault,
            folderName: name,
            occupiedCanonLower: occupied,
            mergeConfirmed: false,
            canonicalize: identity,
            // These suites cover target *placement*; every candidate resolves
            // as not-yet-existing. The non-empty rule (#54) has its own suite
            // (ImplicitMergeGuardTests).
            listingFor: { _ in nil }
        )
    }

    // MARK: - Empty-vault eligibility (pure core)

    @Test("An empty directory is an eligible target")
    func emptyListingIsEligible() {
        #expect(VaultManager.isEmptyVaultListing([]))
    }

    @Test("A vault holding only .obsidian is eligible, in any case spelling")
    func obsidianOnlyIsEligible() {
        #expect(VaultManager.isEmptyVaultListing([".obsidian"]))
        #expect(VaultManager.isEmptyVaultListing([".Obsidian"]))
    }

    @Test("Notes, hidden leftovers, or a sync marker disqualify a target")
    func contentDisqualifies() {
        #expect(!VaultManager.isEmptyVaultListing(["Note.md"]))
        #expect(!VaultManager.isEmptyVaultListing([".obsidian", "Daily"]))
        #expect(!VaultManager.isEmptyVaultListing([".stfolder"]))
        #expect(!VaultManager.isEmptyVaultListing([".DS_Store"]))
    }

    // MARK: - validateManualTarget

    @Test("A fresh folder name maps to its own subdirectory under the root")
    func freshNameMapsUnderRoot() {
        #expect(validate(name: "Life") == .path("/Obsidian/Life"))
    }

    @Test("An existing empty vault is accepted as target")
    func existingEmptyVaultAccepted() {
        #expect(validate(name: "Life", entries: [".obsidian"]) == .path("/Obsidian/Life"))
    }

    @Test("A blank name is refused with guidance")
    func blankNameRefused() {
        guard case .refused(let message) = validate(name: "   ") else {
            Issue.record("expected a refusal for a blank name")
            return
        }
        #expect(message.contains("folder name"))
    }

    @Test("Invalid path characters are sanitized, not refused")
    func invalidCharactersSanitized() {
        #expect(validate(name: "Life/2026") == .path("/Obsidian/Life-2026"))
    }

    @Test("A target that is another vault's directory is refused, case-insensitively")
    func overlapSameDirectoryRefused() {
        guard case .refused(let message) = validate(name: "LIFE", occupied: occupied("/obsidian/life")) else {
            Issue.record("expected a refusal for an occupied directory")
            return
        }
        // Names the folder, states the reason, gives the next step.
        #expect(message.contains("LIFE"))
        #expect(message.contains("already synced by another vault"))
        #expect(message.contains("Choose a different folder"))
    }

    @Test("A target holding another vault's directory deeper down is refused")
    func overlapContainingOccupiedRefused() {
        guard case .refused = validate(name: "Life", occupied: occupied("/obsidian/life/attachments")) else {
            Issue.record("expected a refusal for a target containing an occupied directory")
            return
        }
    }

    @Test("A non-empty folder is refused: linking would merge two content sets")
    func nonEmptyTargetRefused() {
        guard case .refused(let message) = validate(name: "Life", entries: [".obsidian", "Note.md"]) else {
            Issue.record("expected a refusal for a non-empty target")
            return
        }
        #expect(message.contains("Life"))
        #expect(message.contains("empty vault"))
    }

    @Test("#45 follow-up: a compromised root refuses every manual target")
    func compromisedRootRefused() {
        guard case .refused(let message) = validate(name: "Life", occupied: occupied("/obsidian")) else {
            Issue.record("expected a refusal when the root lies in an occupied directory")
            return
        }
        #expect(message.contains("contains your vaults"))
    }

    // MARK: - resolveAcceptPath (override precedence)

    @Test("Without an override the label-derived mapping applies")
    func noOverrideUsesLabelMapping() {
        #expect(
            resolve(manualTarget: nil, name: "Obsidian-Vault-Life")
                == .path("/Obsidian/Obsidian-Vault-Life")
        )
    }

    @Test("A stored manual target wins over the share-label default")
    func overrideWinsOverLabelDefault() {
        #expect(
            resolve(manualTarget: "Life", name: "Obsidian-Vault-Life")
                == .path("/Obsidian/Life")
        )
    }

    @Test("#270: a new manual choice under a selected folder that is itself a vault is refused")
    func newManualChoiceUnderAVaultRootIsRefused() {
        guard case .refused(let message) = validate(rootIsVault: true, name: "Notes") else {
            Issue.record("a new target inside a vault must be refused")
            return
        }
        #expect(message.contains("contains your vaults"))
        // The same name under a container is a folder of its own.
        #expect(validate(rootIsVault: false, name: "Notes") == .path("/Obsidian/Notes"))
    }

    @Test("A recorded override keeps its place when the selected folder is itself a vault")
    func overrideKeepsItsPlaceUnderAVaultRoot() {
        // Recorded consent (#52): the label-derived mapping refuses under such
        // a root (#270), the location the user chose stays what it was.
        #expect(
            resolve(manualTarget: "Life", rootIsVault: true, name: "Obsidian-Vault-Life")
                == .path("/Obsidian/Life")
        )
    }

    @Test("#52: an unsafe override is refused with path, reason, and next step — never a silent fallback")
    func unsafeOverrideRefusedWithGuidance() {
        let decision = resolve(
            manualTarget: "Life",
            name: "Obsidian-Vault-Life",
            occupied: occupied("/obsidian/life")
        )
        guard case .refused(let message) = decision else {
            Issue.record("an unsafe override must refuse, not fall back to the share-label default")
            return
        }
        // Names the stored location…
        #expect(message.contains("\"Life\""))
        // …states the reason…
        #expect(message.contains("overlaps"))
        // …and tells the user the next step.
        #expect(message.contains("Choose Vault…"))
        #expect(message.contains("remove the vault"))
    }

    @Test("An override whose directory holds another vault deeper down is refused")
    func overrideContainingOccupiedRefused() {
        guard case .refused = resolve(
            manualTarget: "Life",
            name: "Obsidian-Vault-Life",
            occupied: occupied("/obsidian/life/inner")
        ) else {
            Issue.record("expected a refusal for an override containing an occupied directory")
            return
        }
    }

    @Test("Refusal without a safe location keeps the container-folder guidance")
    func labelRefusalKeepsGuidance() {
        guard case .refused(let message) = resolve(
            manualTarget: nil,
            rootIsVault: true,
            name: "Second",
            occupied: occupied("/obsidian")
        ) else {
            Issue.record("expected a refusal when no safe location exists")
            return
        }
        #expect(message.contains("contains your vaults"))
    }

    // MARK: - Manual target sidecar

    @Test("Sidecar set / read round-trips through UserDefaults and overwrites cleanly")
    func sidecarRoundTrip() {
        UserDefaults.standard.removeObject(forKey: "vaultsync.manualShareTargets")
        UserDefaults.standard.removeObject(forKey: "vaultsync.manualShareTargetRoots")
        #expect(ManualShareTargetStore.target(forFolder: "f1") == nil)
        #expect(ManualShareTargetStore.consentRoot(forFolder: "f1") == nil)

        ManualShareTargetStore.setTarget("Life", forFolder: "f1", underRoot: "/root-a")
        #expect(ManualShareTargetStore.target(forFolder: "f1") == "Life")
        #expect(ManualShareTargetStore.consentRoot(forFolder: "f1") == "/root-a")
        #expect(ManualShareTargetStore.target(forFolder: "other") == nil)
        #expect(ManualShareTargetStore.consentRoot(forFolder: "other") == nil)

        // The same name confirmed under another selected folder moves the
        // consent with it (#270).
        ManualShareTargetStore.setTarget("Life", forFolder: "f1", underRoot: "/root-b")
        #expect(ManualShareTargetStore.consentRoot(forFolder: "f1") == "/root-b")

        ManualShareTargetStore.setTarget("Work", forFolder: "f1", underRoot: "/root-b")
        #expect(ManualShareTargetStore.target(forFolder: "f1") == "Work")

        // A record written by an earlier version has a name and no folder.
        UserDefaults.standard.removeObject(forKey: "vaultsync.manualShareTargetRoots")
        #expect(ManualShareTargetStore.target(forFolder: "f1") == "Work")
        #expect(ManualShareTargetStore.consentRoot(forFolder: "f1") == nil)

        UserDefaults.standard.removeObject(forKey: "vaultsync.manualShareTargets")
    }

    // MARK: - Auto-accept suppression for user-removed folders

    @Test("#52: a share whose folder the user removed is not auto-accept eligible")
    func userRemovedSharesAreSkipped() {
        let pending = [
            SyncthingManager.PendingFolderInfo(id: "keep", label: "Keep", offeredBy: []),
            SyncthingManager.PendingFolderInfo(id: "removed", label: "Removed", offeredBy: []),
        ]
        let eligible = SyncthingManager.autoAcceptEligible(actionable: pending, userRemoved: ["removed"])
        #expect(eligible.map(\.id) == ["keep"])
    }

    @Test("Nothing is suppressed while the user has removed nothing")
    func noSuppressionByDefault() {
        let pending = [SyncthingManager.PendingFolderInfo(id: "a", label: "A", offeredBy: [])]
        #expect(SyncthingManager.autoAcceptEligible(actionable: pending, userRemoved: []).map(\.id) == ["a"])
    }

    @Test("The user-removed set is restored across launches")
    @MainActor
    func userRemovedSetPersists() {
        UserDefaults.standard.set(["f1"], forKey: "syncthing.userRemovedFolderIDs")
        defer { UserDefaults.standard.removeObject(forKey: "syncthing.userRemovedFolderIDs") }

        let manager = SyncthingManager()
        #expect(manager.userRemovedFolderIDs == ["f1"])
    }
}

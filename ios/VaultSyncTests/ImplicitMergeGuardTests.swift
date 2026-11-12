import Foundation
import Testing
@testable import VaultSync

@Suite("No implicit merge into a non-empty unconfigured folder (#54)")
struct ImplicitMergeGuardTests {

    /// Production passes `FolderPathReconciler.canonical`; tests inject
    /// identity so the decision logic runs without the filesystem. Directory
    /// listings are injected the same way, keyed by resolved path.
    private let identity: (String) -> String = { $0 }

    private func resolve(
        manualTarget: String? = nil,
        root: String = "/Obsidian",
        rootIsVault: Bool = false,
        name: String,
        occupied: Set<String> = [],
        mergeConfirmed: Bool = false,
        listings: [String: [String]] = [:]
    ) -> ShareTargetDecision {
        VaultManager.resolveAcceptPath(
            manualTarget: manualTarget,
            rawRoot: root,
            rootIsVault: rootIsVault,
            folderName: name,
            occupiedCanonLower: Set(occupied.map { $0.lowercased() }),
            mergeConfirmed: mergeConfirmed,
            canonicalize: identity,
            listingFor: { listings[$0] }
        )
    }

    // MARK: - Emptiness rule, mirrored against the Go hard floor

    /// Mirror of `TestNonEmptyTargetErrorMirrorsSwiftEmptyVaultRule` in
    /// `go/bridge/pendingfolders_test.go` (#54): both layers must decide the
    /// same cases identically — a divergent edge case would be a silent gap
    /// between this guard and the Go hard floor. Keep the two tables in sync.
    @Test("Swift and the Go floor decide emptiness identically")
    func emptinessRuleMirrorsGoFloor() {
        let cases: [(name: String, listing: [String]?, empty: Bool)] = [
            ("missing directory", nil, true),
            ("empty directory", [], true),
            ("obsidian config only", [".obsidian"], true),
            ("obsidian case variant", [".Obsidian"], true),
            ("stfolder marker disqualifies", [".stfolder"], false),
            ("note disqualifies", ["note.md"], false),
            ("obsidian plus note disqualifies", [".obsidian", "note.md"], false),
            ("hidden leftover disqualifies", [".DS_Store"], false),
        ]
        for testCase in cases {
            let decision = resolve(
                name: "Notes",
                listings: testCase.listing.map { ["/Obsidian/Notes": $0] } ?? [:]
            )
            if testCase.empty {
                #expect(decision == .path("/Obsidian/Notes"), "\(testCase.name)")
            } else {
                #expect(
                    decision == .requiresMergeConfirmation(path: "/Obsidian/Notes", targetName: "Notes"),
                    "\(testCase.name)"
                )
            }
        }
    }

    // MARK: - Consent and precedence

    @Test("The user's confirmation lets the same accept proceed (remove + re-accept recovery)")
    func confirmationUnblocksTheAccept() {
        let decision = resolve(
            name: "Notes",
            mergeConfirmed: true,
            listings: ["/Obsidian/Notes": ["note.md"]]
        )
        #expect(decision == .path("/Obsidian/Notes"))
    }

    @Test("A recorded manual target stays exempt: it is recorded consent (#52/006)")
    func recordedManualTargetNeedsNoConfirmation() {
        let decision = resolve(
            manualTarget: "My Notes",
            name: "Notes",
            listings: ["/Obsidian/My Notes": ["note.md", ".stfolder"]]
        )
        #expect(decision == .path("/Obsidian/My Notes"))
    }

    @Test("An overlapping manual target is still refused — consent never overrides the #45 floor")
    func overlapRefusalStillWinsOverConsent() {
        let decision = resolve(
            manualTarget: "Vault",
            name: "Vault",
            occupied: ["/Obsidian/Vault"],
            mergeConfirmed: true,
            listings: [:]
        )
        guard case .refused = decision else {
            Issue.record("expected refusal for an overlapping manual target, got \(decision)")
            return
        }
    }

    @Test("A compromised root is refused, not offered as a merge — no consent can nest a vault (#45)")
    func compromisedRootRefusesBeforeMergeCheck() {
        let decision = resolve(
            name: "Notes",
            occupied: ["/Obsidian"],
            mergeConfirmed: true,
            listings: ["/Obsidian/Notes": ["note.md"]]
        )
        guard case .refused = decision else {
            Issue.record("expected refusal under a compromised root, got \(decision)")
            return
        }
    }

    // MARK: - A vault as root and suffix candidates

    @Test("#270: a selected folder that is itself a vault refuses the share — a merge decision cannot open it")
    func vaultAsRootRefusesEvenWhenMergeConfirmed() {
        for mergeConfirmed in [false, true] {
            let decision = resolve(
                rootIsVault: true,
                name: "Life",
                mergeConfirmed: mergeConfirmed,
                listings: ["/Obsidian": [".obsidian", "Daily", "note.md"]]
            )
            guard case .refused(let message) = decision else {
                Issue.record("expected a refusal under a vault-as-root, got \(decision)")
                continue
            }
            #expect(message.contains("contains your vaults"))
        }
    }

    @Test("#270: a vault named like the root goes to its own folder, guarded like every other target")
    func vaultNamedLikeTheRootGetsTheSameGuard() {
        #expect(
            resolve(name: "Obsidian", listings: ["/Obsidian": [".obsidian"]])
                == .path("/Obsidian/Obsidian")
        )
        #expect(
            resolve(name: "Obsidian", listings: ["/Obsidian/Obsidian": ["note.md"]])
                == .requiresMergeConfirmation(path: "/Obsidian/Obsidian", targetName: "Obsidian")
        )
    }

    @Test("A numeric-suffix candidate holding foreign content is confirmed, never silently merged or skipped")
    func suffixCandidateGetsTheSameGuard() {
        // "Notes" is another vault's directory, so the share resolves to
        // "Notes (2)" — which happens to exist with content. Diverting on to
        // "Notes (3)" would silently split a legitimate re-accept of the
        // vault that used to live in "Notes (2)" (006's rejected fallback).
        let decision = resolve(
            name: "Notes",
            occupied: ["/Obsidian/Notes"],
            listings: ["/Obsidian/Notes (2)": ["note.md"]]
        )
        #expect(decision == .requiresMergeConfirmation(path: "/Obsidian/Notes (2)", targetName: "Notes (2)"))
    }
}

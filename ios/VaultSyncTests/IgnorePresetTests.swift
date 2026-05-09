import Testing
@testable import VaultSync

@Suite("IgnorePreset catalog")
struct IgnorePresetTests {
    @Test func recommendedContainsWorkspaceAndTrash() {
        let ids = IgnorePreset.recommended.map(\.id)
        #expect(ids == ["workspace", "trash"])
    }

    @Test func allPresetsHaveNonEmptyPatterns() {
        for preset in IgnorePreset.all {
            #expect(!preset.patterns.isEmpty, "preset \(preset.id) has no patterns")
        }
    }

    @Test func presetIDsAreUnique() {
        let ids = IgnorePreset.all.map(\.id)
        #expect(Set(ids).count == ids.count)
    }

    @Test func workspacePresetCoversBothWorkspaceFiles() {
        let patterns = Set(IgnorePreset.workspace.patterns)
        #expect(patterns.contains(".obsidian/workspace.json"))
        #expect(patterns.contains(".obsidian/workspace-mobile.json"))
    }

    @Test func gitPresetIsNotInRecommended() {
        let recommendedIDs = Set(IgnorePreset.recommended.map(\.id))
        #expect(!recommendedIDs.contains("git"))
    }

    @Test func presetForDetectedPatternMapsGit() {
        let preset = IgnorePreset.preset(forDetectedPattern: ".git")
        #expect(preset?.id == "git")
    }

    @Test func presetForDetectedPatternMapsCopilot() {
        let preset = IgnorePreset.preset(forDetectedPattern: ".copilot-index")
        #expect(preset?.id == "copilot")
    }

    @Test func presetForDetectedPatternReturnsNilForUnknown() {
        let preset = IgnorePreset.preset(forDetectedPattern: "node_modules")
        #expect(preset == nil)
    }
}

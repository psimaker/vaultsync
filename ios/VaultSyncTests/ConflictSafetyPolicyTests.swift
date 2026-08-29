import Testing
@testable import VaultSync

@Suite("Conflict safety live-status policy (#150)")
struct ConflictSafetyPolicyTests {
    @Test("Immutable receive-side runtime policy keeps only send-only mutable (#150)")
    func immutableReceiveSideRuntimePolicy() {
        #expect(ConflictSafetyPolicy.receiveSideReadOnlyRuntimeEnabled)
        for type in ["sendreceive", "receiveonly", "receiveencrypted"] {
            #expect(ConflictSafetyPolicy.runtimeState(forFolderType: type) == .stopped)
        }
        #expect(ConflictSafetyPolicy.runtimeState(forFolderType: "sendonly") == .clear)
        #expect(ConflictSafetyPolicy.runtimeState(forFolderType: nil) == .unknown)
        #expect(ConflictSafetyPolicy.runtimeState(forFolderType: "future-mode") == .unknown)

        #expect(ConflictSafetyPolicy.aggregate([]) == .unknown)
        #expect(ConflictSafetyPolicy.aggregate([.clear, .unknown]) == .unknown)
        #expect(ConflictSafetyPolicy.aggregate([.unknown, .stopped, .clear]) == .stopped)
    }

    @Test("Stable stop wins over an idle success shape (#150)")
    func stoppedWinsOverIdle() {
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "idle",
            errorReason: ConflictSafetyPolicy.stoppedReason,
            hasRawErrorDetail: true
        ) == .stopped)
        #expect(ConflictSafetyPolicy.actionErrorCode(for: .stopped)
            == ConflictSafetyPolicy.engineStopMarker)
    }

    @Test("Missing, contradictory, and unavailable evidence are unknown (#150)")
    func incompleteEvidenceIsUnknown() {
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: false,
            state: nil,
            errorReason: nil
        ) == .unknown)
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "",
            errorReason: nil
        ) == .unknown)
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "idle",
            errorReason: nil,
            hasRawErrorDetail: true
        ) == .unknown)
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "error",
            errorReason: nil
        ) == .unknown)
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "error",
            errorReason: "unclassified_generic_reason"
        ) == .unknown)
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "error",
            errorReason: "permission_denied",
            hasRawErrorDetail: true
        ) == .unknown)
        for state in ["unknown", "bogus", "future-valid", "paused"] {
            #expect(ConflictSafetyPolicy.classify(
                statusReadable: true,
                state: state,
                errorReason: nil
            ) == .unknown)
        }

        for reason in [
            ConflictSafetyPolicy.folderErrorEvidenceUnavailableReason,
            ConflictSafetyPolicy.folderCompletionEvidenceUnavailableReason,
        ] {
            #expect(ConflictSafetyPolicy.classify(
                statusReadable: true,
                state: "idle",
                errorReason: reason
            ) == .unknown)
            #expect(ConflictSafetyPolicy.state(forEventReason: reason) == .unknown)
        }
        #expect(ConflictSafetyPolicy.actionErrorCode(for: .unknown)
            == ConflictSafetyPolicy.engineStopMarker)
    }

    @Test("Only coherent live status is clear (#150)")
    func coherentStatusIsClear() {
        for state in [
            "idle",
            "scanning",
            "scan-waiting",
            "sync-waiting",
            "sync-preparing",
            "syncing",
            "cleaning",
            "clean-waiting",
        ] {
            #expect(ConflictSafetyPolicy.classify(
                statusReadable: true,
                state: state,
                errorReason: nil
            ) == .clear)
        }
        #expect(ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: "  SYNC-PREPARING\n",
            errorReason: nil
        ) == .clear)
        #expect(ConflictSafetyPolicy.actionErrorCode(for: .clear) == nil)
        #expect(ConflictSafetyPolicy.state(forEventReason: "permission_denied") == nil)
    }
}

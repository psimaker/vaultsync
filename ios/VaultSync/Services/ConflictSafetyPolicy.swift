import Foundation

/// Pure interpretation of the bridge's conflict-safety evidence (#150).
///
/// Engine evidence and the configured folder type are the authorities. Swift
/// deliberately persists no shadow marker: before a fresh status is readable,
/// actions remain blocked as `.unknown`, while the compile-time receive policy
/// reproduces its stop after every restart. This type performs no I/O, logging,
/// configuration, or mutation.
enum ConflictSafetyPolicy {
    enum State: Equatable, Sendable {
        case clear
        case stopped
        case unknown
    }

    static let stoppedReason = "conflict_retention_safety_stop"
    static let engineStopMarker = "vaultsync-conflict-retention-safety-stop"
    static let folderErrorEvidenceUnavailableReason = "folder_error_evidence_unavailable"
    static let folderCompletionEvidenceUnavailableReason = "folder_completion_evidence_unavailable"
    static let statusUnavailableActionCode = "conflict_safety_status_unavailable"

    /// 2.0.2 deliberately keeps every receive-capable folder read-only. This
    /// is a compile-time runtime policy, not a preference or persisted latch:
    /// a restart cannot silently re-enable receive-side mutation.
    static let receiveSideReadOnlyRuntimeEnabled = true

    private static let unavailableReasons: Set<String> = [
        folderErrorEvidenceUnavailableReason,
        folderCompletionEvidenceUnavailableReason,
    ]

    // `lib/model/folderstate.go` in the pinned Syncthing fork is the wire
    // authority. A future or malformed value cannot authorize mutation until
    // this allowlist is deliberately reviewed alongside that upgrade.
    private static let knownNonErrorStates: Set<String> = [
        "idle",
        "scanning",
        "scan-waiting",
        "sync-waiting",
        "sync-preparing",
        "syncing",
        "cleaning",
        "clean-waiting",
    ]

    /// Classifies only fixed bridge fields. Raw messages and paths never become
    /// part of the decision or an emitted error.
    static func classify(
        statusReadable: Bool,
        state: String?,
        errorReason: String?,
        hasRawErrorDetail: Bool = false
    ) -> State {
        guard statusReadable else { return .unknown }

        let normalizedState = state?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() ?? ""
        let normalizedReason = errorReason?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() ?? ""

        if normalizedReason == stoppedReason {
            return .stopped
        }
        if unavailableReasons.contains(normalizedReason) {
            return .unknown
        }
        guard !normalizedState.isEmpty else {
            return .unknown
        }

        // A reason/detail on a success-shaped state is contradictory evidence.
        // Any remaining error state is also insufficient to authorize a
        // conflict mutation: arbitrary bridge detail is not a clear signal.
        if normalizedState == "error" {
            return .unknown
        }
        if !normalizedReason.isEmpty || hasRawErrorDetail {
            return .unknown
        }
        return knownNonErrorStates.contains(normalizedState) ? .clear : .unknown
    }

    static func actionErrorCode(for state: State) -> String? {
        switch state {
        case .clear:
            return nil
        case .stopped, .unknown:
            // Action ABIs expose one fixed path-free stop. The structured
            // status reason still distinguishes a known stop from unavailable
            // evidence for read-only UI presentation.
            return engineStopMarker
        }
    }

    static func allowsMutation(for state: State) -> Bool {
        state == .clear
    }

    /// The configured folder type is an independent authorization input. A
    /// clear-shaped status can never override the 2.0.2 receive-side stop, and
    /// an unknown future type stays fail-closed until explicitly reviewed.
    static func runtimeState(forFolderType type: String?) -> State {
        let normalized = type?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        switch normalized {
        case "sendonly":
            return .clear
        case "sendreceive", "receiveonly", "receiveencrypted":
            return receiveSideReadOnlyRuntimeEnabled ? .stopped : .clear
        default:
            return .unknown
        }
    }

    /// Combines independent safety evidence. A durable stop wins over missing
    /// evidence; missing evidence wins over an otherwise clear result.
    static func aggregate(_ states: [State]) -> State {
        guard !states.isEmpty else { return .unknown }
        if states.contains(.stopped) { return .stopped }
        if states.contains(.unknown) { return .unknown }
        return .clear
    }

    static func state(forEventReason reason: String?) -> State? {
        let normalized = reason?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if normalized == stoppedReason {
            return .stopped
        }
        if let normalized, unavailableReasons.contains(normalized) {
            return .unknown
        }
        return nil
    }
}

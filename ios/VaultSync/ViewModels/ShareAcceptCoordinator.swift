import Foundation
import Observation

/// Retained injectable acceptance core for regression coverage of the former
/// flow. In 2.0.2 it has no live environment factory and no shipping view or
/// app construction: pending shares are inspection-only (#150). A future
/// caller requires a separately approved recovery doctrine and explicit live
/// wiring instead of bypassing that policy here.
@MainActor
@Observable
final class ShareAcceptCoordinator {

    // MARK: - Types

    /// A share whose label-default target already contains files, waiting for
    /// the user's explicit merge decision (#54). Drives the merge confirmation
    /// dialog; confirming re-runs the accept with `mergeConfirmed`, which
    /// re-validates everything at confirm time. Only manual accepts set it —
    /// the automatic pass records the parked state and waits (decision 007).
    struct MergeConfirmationRequest: Identifiable {
        let folder: SyncthingManager.PendingFolderInfo
        let targetName: String
        var id: String { folder.id }
    }

    enum AcceptSource {
        case automatic
        case manual
    }

    /// Injected effects used only by focused regression tests in 2.0.2.
    struct Environment {
        var settled: @MainActor () -> Bool
        var vaultAccessible: @MainActor () -> Bool
        var receiveSafetyState: @MainActor () -> ConflictSafetyPolicy.State = { .unknown }
        var pendingFolders: @MainActor () -> [SyncthingManager.PendingFolderInfo]
        var autoAcceptEligible: @MainActor () -> [SyncthingManager.PendingFolderInfo]
        var accept: @MainActor (_ folder: SyncthingManager.PendingFolderInfo, _ mergeConfirmed: Bool) -> PendingShareAcceptOutcome
        var acceptIntoTarget: @MainActor (_ folder: SyncthingManager.PendingFolderInfo, _ targetName: String) -> String?
        var unignorePendingFolder: @MainActor (_ id: String) -> Void
        var ignorePendingFolder: @MainActor (_ id: String) -> Void
        // Once-per-reason gating of the automatic-refusal modal (#95); the
        // inline share row keeps every failure visible regardless. Defaults
        // keep the modal ungated so focused tests need not wire the store.
        var shouldPresentRefusalAlert: @MainActor (_ folderID: String, _ reason: String) -> Bool = { _, _ in true }
        var markRefusalAlertPresented: @MainActor (_ folderID: String, _ reason: String) -> Void = { _, _ in }
        var clearRefusalAlertRecord: @MainActor (_ folderID: String) -> Void = { _ in }

    }

    // MARK: - Published state

    private(set) var pendingShareFailures: [String: SyncUserError] = [:]
    private(set) var pendingShareInFlight: Set<String> = []

    /// Set only by manual accepts; the home screen binds its merge dialog to
    /// it. Writable so the host's Cancel button (and the UI-audit fixture)
    /// can clear/seed it.
    var pendingMergeConfirmation: MergeConfirmationRequest?

    /// One-shot message for whichever host view is mounted: it presents the
    /// text in its own alert and resets this to nil.
    var alertMessage: String?

    private let environment: Environment

    init(environment: Environment) {
        self.environment = environment
    }

    // MARK: - Automatic pass

    func runAutomaticPass() {
        let pendingIDs = Set(environment.pendingFolders().map(\.id))
        pendingShareFailures = pendingShareFailures.filter { pendingIDs.contains($0.key) }
        pendingShareInFlight = pendingShareInFlight.intersection(pendingIDs)

        if let safetyError = receiveSafetyError() {
            for folder in environment.autoAcceptEligible() {
                pendingShareFailures[folder.id] = safetyError
            }
            return
        }

        // Accept decisions only run on settled paths (#56, decision 008): a
        // pass during a pending path reconcile would judge overlap against
        // pre-reconcile folder paths — stale exactly after a container move.
        // Held passes re-fire from the hosts' settled onChange triggers.
        guard environment.settled() else { return }

        guard environment.vaultAccessible() else { return }

        // Auto-accept skips shares whose folder the user removed on this
        // iPhone (doctrine 002 / #52) — those stay visible as pending rows
        // until the user accepts them explicitly.
        for folder in environment.autoAcceptEligible() where pendingShareFailures[folder.id] == nil {
            guard !pendingShareInFlight.contains(folder.id) else { continue }
            accept(folder, source: .automatic)
        }
    }

    // MARK: - Accepts

    func accept(
        _ folder: SyncthingManager.PendingFolderInfo,
        source: AcceptSource,
        mergeConfirmed: Bool = false
    ) {
        if let safetyError = receiveSafetyError() {
            pendingShareFailures[folder.id] = safetyError
            return
        }

        // Accept decisions only run on settled paths (#56, decision 008). The
        // automatic pass is already held in runAutomaticPass and re-fires on
        // settle; this guard also covers the manual paths (retry, merge
        // confirmation — #54's re-validation would otherwise judge the same
        // stale occupied set). A manual tap gets the transient explanation,
        // never a silent no-op (002). No failure is recorded, so nothing
        // blocks the automatic re-fire.
        guard environment.settled() else {
            if source == .manual {
                alertMessage = L10n.tr("Vault locations are still being checked. Try again in a moment.")
            }
            return
        }

        pendingShareInFlight.insert(folder.id)
        let outcome = environment.accept(folder, mergeConfirmed)
        pendingShareInFlight.remove(folder.id)

        switch outcome {
        case .refused(let err):
            let userError = SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Could Not Accept Share"))
            pendingShareFailures[folder.id] = userError
            // The automatic pass re-runs on every launch (initial: true), so a
            // persistently refused share re-fired the same modal forever (#95).
            // The modal is once per (folder, reason), persisted; the inline
            // share row keeps the failure visible every session regardless.
            // alertMessage == nil: with two refusals in one synchronous pass
            // the second would overwrite the first's one-shot message while
            // both got marked as presented — the first alert would be
            // suppressed forever without ever rendering. First refusal wins
            // the slot; later ones stay unmarked and alert on a later pass.
            if source == .automatic,
               alertMessage == nil,
               environment.shouldPresentRefusalAlert(folder.id, err) {
                environment.markRefusalAlertPresented(folder.id, err)
                alertMessage = L10n.fmt(
                    "Could not accept share '%@'.\n\n%@",
                    folder.label.isEmpty ? folder.id : folder.label,
                    userError.userVisibleDescription
                )
            }

        case .needsMergeConfirmation(let targetName):
            // The target already holds content — never merge without the
            // user's explicit decision (#54, doctrine 002/006). The share row
            // keeps the explanation either way (also stops auto retries); an
            // explicit tap additionally gets the confirmation dialog. No
            // modal alert on the automatic pass: this is a decision waiting
            // for the user, not an error.
            pendingShareFailures[folder.id] = SyncUserError(
                category: .fileAccess,
                title: L10n.tr("Folder Already Contains Files"),
                message: L10n.fmt(
                    "The folder \"%@\" already contains files. Accepting this share would combine its contents with the shared vault and sync the result to the other devices.",
                    targetName
                ),
                remediation: L10n.tr("Tap \"Review and Accept\" to decide, or \"Choose Vault…\" to pick a different location."),
                technicalDetails: nil
            )
            if source == .manual {
                pendingMergeConfirmation = MergeConfirmationRequest(
                    folder: folder,
                    targetName: targetName
                )
            }

        case .accepted:
            pendingShareFailures.removeValue(forKey: folder.id)
            // A future refusal after a successful accept is a new situation —
            // it may alert again (#95).
            environment.clearRefusalAlertRecord(folder.id)
            environment.unignorePendingFolder(folder.id)
        }
    }

    /// Accept a share into a user-picked target (#52). Returns the error to
    /// show inline in the picker sheet, or nil on success (the sheet then
    /// dismisses itself).
    func acceptManually(
        folder: SyncthingManager.PendingFolderInfo,
        intoTargetNamed targetName: String
    ) -> String? {
        if let safetyError = receiveSafetyError() {
            pendingShareFailures[folder.id] = safetyError
            return safetyError.userVisibleDescription
        }

        // Accept decisions only run on settled paths (#56, decision 008) —
        // the picker's empty-target and overlap validation (#52) reads the
        // same occupied-path set the reconcile is still rewriting.
        guard environment.settled() else {
            return L10n.tr("Vault locations are still being checked. Try again in a moment.")
        }

        pendingShareInFlight.insert(folder.id)
        let err = environment.acceptIntoTarget(folder, targetName)
        pendingShareInFlight.remove(folder.id)

        if let err {
            return SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Could Not Accept Share")).userVisibleDescription
        }
        pendingShareFailures.removeValue(forKey: folder.id)
        environment.clearRefusalAlertRecord(folder.id)
        environment.unignorePendingFolder(folder.id)
        return nil
    }

    /// The user confirmed merging the share into the existing folder — re-run
    /// the accept with consent attached. Everything (overlaps, emptiness, the
    /// resolved path) is re-validated at confirm time, so a state change while
    /// the dialog was open cannot smuggle the accept somewhere unsafe.
    func confirmMergeAccept(_ request: MergeConfirmationRequest) {
        if let safetyError = receiveSafetyError() {
            pendingMergeConfirmation = nil
            pendingShareFailures[request.folder.id] = safetyError
            return
        }
        pendingMergeConfirmation = nil
        pendingShareFailures.removeValue(forKey: request.folder.id)
        accept(request.folder, source: .manual, mergeConfirmed: true)
    }

    func retry(_ folder: SyncthingManager.PendingFolderInfo) {
        if let safetyError = receiveSafetyError() {
            pendingShareFailures[folder.id] = safetyError
            return
        }
        pendingShareFailures.removeValue(forKey: folder.id)
        accept(folder, source: .manual)
    }

    func ignore(_ folder: SyncthingManager.PendingFolderInfo) {
        environment.ignorePendingFolder(folder.id)
        pendingShareFailures.removeValue(forKey: folder.id)
    }

    /// A share that had no safe location under the old Obsidian root may
    /// succeed under a newly connected one (#45 follow-up) — clear recorded
    /// failures so the next pass attempts it again.
    func clearRecordedFailures() {
        pendingShareFailures.removeAll()
    }

    private func receiveSafetyError() -> SyncUserError? {
        let state = environment.receiveSafetyState()
        guard state != .clear else { return nil }
        return SyncUserError.conflictSafetyError(for: state)
    }
}

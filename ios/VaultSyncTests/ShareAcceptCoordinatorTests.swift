import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Shared accept pass keeps identical gates at every mount point (#92)")
struct ShareAcceptCoordinatorTests {

    private static func offer(_ id: String, label: String? = nil) -> SyncthingManager.PendingFolderInfo {
        SyncthingManager.PendingFolderInfo(id: id, label: label ?? id, offeredBy: [])
    }

    @MainActor
    private final class Recorder {
        var accepts: [(id: String, mergeConfirmed: Bool)] = []
        var unignored: [String] = []
        var ignored: [String] = []
    }

    private static func env(
        settled: @escaping @MainActor () -> Bool = { true },
        accessible: @escaping @MainActor () -> Bool = { true },
        pending: [SyncthingManager.PendingFolderInfo],
        eligible: [SyncthingManager.PendingFolderInfo]? = nil,
        recorder: Recorder,
        outcome: @escaping @MainActor (SyncthingManager.PendingFolderInfo) -> PendingShareAcceptOutcome = { _ in .accepted },
        acceptIntoTarget: @escaping @MainActor (SyncthingManager.PendingFolderInfo, String) -> String? = { _, _ in nil }
    ) -> ShareAcceptCoordinator.Environment {
        ShareAcceptCoordinator.Environment(
            settled: settled,
            vaultAccessible: accessible,
            pendingFolders: { pending },
            autoAcceptEligible: { eligible ?? pending },
            accept: { folder, mergeConfirmed in
                recorder.accepts.append((folder.id, mergeConfirmed))
                return outcome(folder)
            },
            acceptIntoTarget: acceptIntoTarget,
            unignorePendingFolder: { recorder.unignored.append($0) },
            ignorePendingFolder: { recorder.ignored.append($0) }
        )
    }

    @Test("Unsettled paths hold the automatic pass without recording failures (decision 008 — nothing may block the re-fire)")
    func unsettledPathsHoldAutomaticPass() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            settled: { false }, pending: [Self.offer("f1")], recorder: recorder))
        c.runAutomaticPass()
        #expect(recorder.accepts.isEmpty)
        #expect(c.pendingShareFailures.isEmpty)
        #expect(c.alertMessage == nil)
    }

    @Test("Inaccessible Obsidian folder holds the automatic pass")
    func inaccessibleVaultHoldsAutomaticPass() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            accessible: { false }, pending: [Self.offer("f1")], recorder: recorder))
        c.runAutomaticPass()
        #expect(recorder.accepts.isEmpty)
        #expect(c.pendingShareFailures.isEmpty)
    }

    @Test("Settled + accessible: each eligible offer accepted exactly once, without merge consent, and unignored")
    func settledPassAcceptsEachEligibleOfferOnce() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [Self.offer("f1"), Self.offer("f2")], recorder: recorder))
        c.runAutomaticPass()
        #expect(recorder.accepts.map { $0.id } == ["f1", "f2"])
        #expect(recorder.accepts.allSatisfy { $0.mergeConfirmed == false })
        #expect(recorder.unignored == ["f1", "f2"])
        #expect(c.pendingShareFailures.isEmpty)
    }

    @Test("The pass only attempts auto-accept-eligible offers — user-removed/ignored shares stay manual (doctrine 002 / #52)")
    func passRespectsEligibilityList() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [Self.offer("f1"), Self.offer("removed")],
            eligible: [Self.offer("f1")],
            recorder: recorder))
        c.runAutomaticPass()
        #expect(recorder.accepts.map { $0.id } == ["f1"])
    }

    @Test("Automatic needs-merge outcome parks: failure recorded, NO dialog, NO alert, and no re-attempt on the next pass (#54, decision 007)")
    func automaticMergeParksWithoutDialog() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [Self.offer("f1", label: "Life Notes")], recorder: recorder,
            outcome: { _ in .needsMergeConfirmation(targetName: "Life Notes") }))
        c.runAutomaticPass()
        #expect(c.pendingShareFailures["f1"] != nil)
        #expect(c.pendingMergeConfirmation == nil)
        #expect(c.alertMessage == nil)
        c.runAutomaticPass()
        #expect(recorder.accepts.count == 1)
    }

    @Test("Manual needs-merge outcome requests the confirmation dialog")
    func manualMergeRequestsDialog() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [Self.offer("f1")], recorder: recorder,
            outcome: { _ in .needsMergeConfirmation(targetName: "Life Notes") }))
        c.accept(Self.offer("f1"), source: .manual)
        #expect(c.pendingMergeConfirmation?.folder.id == "f1")
        #expect(c.pendingMergeConfirmation?.targetName == "Life Notes")
    }

    @Test("Automatic refusal records the failure and surfaces the one-shot alert message")
    func automaticRefusalRecordsFailureAndAlerts() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [Self.offer("f1")], recorder: recorder,
            outcome: { _ in .refused(message: "no safe location") }))
        c.runAutomaticPass()
        #expect(c.pendingShareFailures["f1"] != nil)
        #expect(c.alertMessage != nil)
    }

    @Test("Manual accept on unsettled paths gets the transient explanation, never a silent no-op, and records no failure (decision 008 / 002)")
    func manualAcceptOnUnsettledPathsGetsTransientMessage() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            settled: { false }, pending: [Self.offer("f1")], recorder: recorder))
        c.accept(Self.offer("f1"), source: .manual)
        #expect(recorder.accepts.isEmpty)
        #expect(c.alertMessage == L10n.tr("Vault locations are still being checked. Try again in a moment."))
        #expect(c.pendingShareFailures.isEmpty)
    }

    @Test("Manual-target accept on unsettled paths returns the transient message and never reaches the accept closure")
    func manualTargetAcceptHeldWhileUnsettled() {
        let recorder = Recorder()
        var reached = false
        let c = ShareAcceptCoordinator(environment: Self.env(
            settled: { false }, pending: [Self.offer("f1")], recorder: recorder,
            acceptIntoTarget: { _, _ in reached = true; return nil }))
        let result = c.acceptManually(folder: Self.offer("f1"), intoTargetNamed: "Target")
        #expect(result == L10n.tr("Vault locations are still being checked. Try again in a moment."))
        #expect(!reached)
    }

    @Test("Merge confirmation re-runs the accept WITH consent and re-checks settlement at confirm time")
    func confirmMergeRerunsWithConsentAndRevalidates() {
        let recorder = Recorder()
        var settled = true
        let c = ShareAcceptCoordinator(environment: Self.env(
            settled: { settled }, pending: [Self.offer("f1")], recorder: recorder))
        let request = ShareAcceptCoordinator.MergeConfirmationRequest(folder: Self.offer("f1"), targetName: "T")
        c.confirmMergeAccept(request)
        #expect(recorder.accepts.map { $0.mergeConfirmed } == [true])
        settled = false
        c.confirmMergeAccept(request)
        #expect(recorder.accepts.count == 1) // held; transient message instead
        #expect(c.alertMessage == L10n.tr("Vault locations are still being checked. Try again in a moment."))
    }

    @Test("Failures for offers no longer pending are pruned on the next pass")
    func vanishedOfferFailuresArePruned() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [], recorder: recorder,
            outcome: { _ in .refused(message: "x") }))
        c.accept(Self.offer("gone"), source: .manual)
        #expect(c.pendingShareFailures["gone"] != nil)
        c.runAutomaticPass()
        #expect(c.pendingShareFailures.isEmpty)
    }

    @Test("A re-entrant pass never double-accepts an in-flight offer (two mount points may both fire triggers during the onboarding transition)")
    func reentrantPassNeverDoubleAccepts() {
        let recorder = Recorder()
        final class Box { var c: ShareAcceptCoordinator? }
        let box = Box()
        let c = ShareAcceptCoordinator(environment: Self.env(
            pending: [Self.offer("f1")], recorder: recorder,
            outcome: { _ in box.c?.runAutomaticPass(); return .accepted }))
        box.c = c
        c.runAutomaticPass()
        #expect(recorder.accepts.count == 1)
    }
}

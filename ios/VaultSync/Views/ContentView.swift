import SwiftUI
import UserNotifications

/// Sync-tab navigation (#187). Value-based, so the stack can be driven
/// programmatically — popping after a vault removal, the design-preview
/// fixture — and a destination never depends on the list row that pushed it.
enum SyncRoute: Hashable {
    case vault(id: String)
    case conflicts(folderID: String, pathPrefix: String?)
    /// Carries the conflict by value: the resolve screen must keep showing the
    /// conflict it is resolving after the resolve removed it from the
    /// manager's list, while its result alert is still on screen.
    case conflict(folderID: String, conflict: SyncthingManager.ConflictInfo)
    case filters(folderID: String)
}

/// Devices-tab navigation (#187).
enum DeviceRoute: Hashable {
    case device(id: String)
}

extension SyncthingManager.ConflictInfo: Hashable {
    static func == (lhs: Self, rhs: Self) -> Bool {
        lhs.originalPath == rhs.originalPath
            && lhs.conflictPath == rhs.conflictPath
            && lhs.conflictDate == rhs.conflictDate
            && lhs.deviceShortID == rhs.deviceShortID
    }

    func hash(into hasher: inout Hasher) {
        hasher.combine(conflictPath)
    }
}

struct ContentView: View {
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    var shareAccept: ShareAcceptCoordinator
    var hubLinkRouter: HubLinkRouter
    @State private var showAddDevice = false
    /// The Add Hub sheet (#174), with the pairing link that opened it, if any.
    @State private var addHubRequest: AddHubRequest?
    @State private var showSettings = false
    @State private var showSetupChecklist = false
    @State private var showObsidianPicker = false
    /// Set when AddDeviceSheet reports a successful add; the hint alert is
    /// presented from the sheet's onDismiss so it survives the dismissal
    /// transition (#95) — same pattern as runPendingChecklistAction.
    @State private var showDeviceAddedHint = false
    @State private var pendingChecklistAction: SetupChecklistViewModel.ChecklistAction?
    @State private var alertMessage: String?
    @State private var showAlert = false
    /// Non-error notice (e.g. "you selected a single vault") — its own alert so
    /// it is not presented under the "Error" title.
    @State private var infoMessage: String?
    @State private var showInfoAlert = false
    @State private var shareTargetPickerFolder: SyncthingManager.PendingFolderInfo?
    @State private var pendingFilterSheetFolder: SyncthingManager.FolderInfo?
    @State private var vaultPendingRemoval: VaultRemovalTarget?
    @State private var showRelayUpsellCard = false
    @State private var showNotificationPrimerCard = false
    @State private var syncPath: [SyncRoute] = []
    @State private var devicesPath: [DeviceRoute] = []
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    #if DEBUG
    @State private var uiAuditDetailFixture: UIAuditDetailFixture?
    #endif

    /// A vault the user has asked to remove, pending confirmation. Drives the
    /// shared removal confirmation dialog used by both the "needs attention"
    /// card and the vault detail screen.
    private struct VaultRemovalTarget: Identifiable {
        let id: String
        let label: String
    }

    private static let relayUpsellShownKey = "relay-upsell-shown"
    private static let notificationPrimerShownKey = "notification-primer-shown"

    /// Cached formatter for the dashboard "Last sync" line. Produces a fully
    /// localized relative phrase ("2 hours ago" / "vor 2 Stunden" / "2 小时前").
    /// Output is static (not live-ticking), which is fine for a last-sync label —
    /// the dashboard re-renders on state changes anyway.
    private static let lastSyncFormatter: RelativeDateTimeFormatter = {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .full
        return f
    }()

    private enum Tab: Hashable {
        case sync
        case devices
        case relay
    }

    @State private var selectedTab: Tab = .sync

    var body: some View {
        TabView(selection: $selectedTab) {
            syncTab
                .tabItem {
                    Label(L10n.tr("Sync"), systemImage: "arrow.triangle.2.circlepath")
                }
                .tag(Tab.sync)

            devicesTab
                .tabItem {
                    Label(L10n.tr("Devices"), systemImage: "laptopcomputer.and.iphone")
                }
                .tag(Tab.devices)

            relayTab
                .tabItem {
                    Label(L10n.tr("Relay"), systemImage: "antenna.radiowaves.left.and.right")
                }
                .tag(Tab.relay)
        }
        // Sheets/alerts live at the shell level so cross-tab triggers (e.g. an
        // "Add Device" remediation tapped from a Sync-tab issue) present
        // regardless of which tab is active.
        .alert(L10n.tr("Something Went Wrong"), isPresented: $showAlert) {
            Button("OK") { }
        } message: {
            Text(alertMessage ?? "")
        }
        .alert(L10n.tr("Note"), isPresented: $showInfoAlert) {
            Button("OK") { }
        } message: {
            Text(infoMessage ?? "")
        }
        .sheet(isPresented: $showAddDevice, onDismiss: presentDeviceAddedHintIfNeeded) {
            AddDeviceSheet(
                syncthingManager: syncthingManager,
                onError: { message in
                    alertMessage = message
                    showAlert = true
                },
                onAdded: { showDeviceAddedHint = true }
            )
        }
        .sheet(item: $addHubRequest) { request in
            addHubSheet(request)
        }
        // A pairing link opened from the Camera app (#174) waits until no
        // other sheet or dialog is up — it never replaces one.
        .task(id: hubLinkRouter.pending) {
            var gate = HubLinkGate()
            while hubLinkRouter.pending != nil, !Task.isCancelled {
                let blocked = !canPresentHubLink || HubLinkGate.somethingIsPresented()
                if gate.shouldPresent(blocked: blocked, now: Date()) {
                    presentPendingHubLink()
                    return
                }
                try? await Task.sleep(for: .milliseconds(200))
            }
        }
        .sheet(isPresented: $showSettings, onDismiss: runPendingChecklistAction) {
            SettingsView(
                syncthingManager: syncthingManager,
                vaultManager: vaultManager,
                subscriptionManager: subscriptionManager,
                onChecklistAction: handleChecklistAction
            )
        }
        // Direct checklist entry from the tappable status hero (#95) — the
        // same runPendingChecklistAction onDismiss plumbing as Settings, so
        // checklist remediations present after the transition finishes.
        .sheet(isPresented: $showSetupChecklist, onDismiss: runPendingChecklistAction) {
            SetupChecklistSheet(
                syncthingManager: syncthingManager,
                vaultManager: vaultManager,
                subscriptionManager: subscriptionManager,
                onAction: { action in
                    showSetupChecklist = false
                    handleChecklistAction(action)
                }
            )
        }
        .sheet(isPresented: $showObsidianPicker) {
            FolderPicker(initialDirectoryURL: vaultManager.obsidianDirectoryURL, onCancel: {
                showObsidianPicker = false
            }) { url in
                showObsidianPicker = false
                Task {
                    if let err = await ObsidianReconnectFlow.run(
                        grantAccess: { vaultManager.grantAccess(url: url) },
                        onGrantSucceeded: {
                            // A share that had no safe location under the old
                            // root (e.g. the root was itself a vault, #45
                            // follow-up) may succeed under the new one — clear
                            // the failures so the retry pass attempts it.
                            shareAccept.clearRecordedFailures()
                            if let advisory = vaultManager.selectionAdvisory {
                                infoMessage = advisory
                                showInfoAlert = true
                                vaultManager.clearSelectionAdvisory()
                            }
                        },
                        reconcile: {
                            // Re-picking the Obsidian directory may resolve to
                            // a new container path — rebase any mapped folders
                            // onto it so a previously-unreachable vault
                            // reconnects.
                            await syncthingManager.reconcileFolderPaths(
                                obsidianRoot: vaultManager.obsidianBasePath
                            ).value
                        },
                        retryPendingShares: {
                            // Reconnecting produces no pendingFolders change
                            // event, so the standing onChange trigger stays
                            // silent — run the accept pass explicitly, on
                            // settled paths (#53).
                            shareAccept.runAutomaticPass()
                        }
                    ) {
                        alertMessage = mappedError(err, fallbackTitle: L10n.tr("Obsidian Folder Connection Failed")).userVisibleDescription
                        showAlert = true
                    }
                }
            }
        }
        // Consent decisions are presented as .alert, never .confirmationDialog:
        // on iOS 26 a confirmation dialog renders as a centered popover WITHOUT
        // a visible Cancel button, so the destructive action was the only
        // visible choice on the very dialogs that exist to slow it down
        // (#64, decision 011).
        .alert(
            L10n.tr("Remove this vault from this iPhone?"),
            isPresented: removalBinding,
            presenting: vaultPendingRemoval
        ) { target in
            Button(L10n.tr("Remove Vault"), role: .destructive) {
                removeVault(id: target.id)
            }
            Button(L10n.tr("Cancel"), role: .cancel) { vaultPendingRemoval = nil }
        } message: { target in
            Text(L10n.fmt("“%@” will stop syncing on this iPhone. Files already on your other devices are not deleted.", target.label))
        }
        .alert(
            L10n.tr("Sync into a folder that already contains files?"),
            isPresented: mergeConfirmationBinding,
            presenting: shareAccept.pendingMergeConfirmation
        ) { request in
            Button(L10n.tr("Merge and Sync"), role: .destructive) {
                shareAccept.confirmMergeAccept(request)
            }
            Button(L10n.tr("Cancel"), role: .cancel) { shareAccept.pendingMergeConfirmation = nil }
        } message: { request in
            Text(L10n.fmt(
                "The folder \"%@\" already contains files. If you accept, those files and the contents of the shared vault \"%@\" will be combined and synced to the other devices sharing this vault. Accept only if this folder holds this vault's own earlier notes — for example after removing the vault and accepting its share again. If it is a different vault or unrelated files, cancel and use \"Choose Vault…\" to pick a different location.",
                request.targetName,
                request.folder.label.isEmpty ? request.folder.id : request.folder.label
            ))
        }
        .sheet(item: $shareTargetPickerFolder) { folder in
            ShareTargetPickerView(
                shareLabel: folder.label.isEmpty ? folder.id : folder.label,
                defaultName: VaultManager.sanitizeDirectoryName(folder.label.isEmpty ? folder.id : folder.label),
                eligibleVaults: vaultManager.eligibleShareTargets(syncthingManager: syncthingManager),
                onConfirm: { targetName in
                    shareAccept.acceptManually(folder: folder, intoTargetNamed: targetName)
                }
            )
        }
        .onChange(of: syncthingManager.pendingFolders, initial: true) { _, _ in
            shareAccept.runAutomaticPass()
        }
        .onChange(of: syncthingManager.pathSettlement.settled) { _, settled in
            // Paths just settled: run the pass that was held during the
            // reconcile (#56). pendingFolders itself did not change, so the
            // standing trigger above stays silent — the same gap #53 closed
            // for the reconnect flow.
            if settled {
                shareAccept.runAutomaticPass()
            }
        }
        .onChange(of: shareAccept.alertMessage) { _, message in
            // The coordinator is host-agnostic (#92): whichever view is
            // mounted routes its one-shot messages into its own alert.
            guard let message else { return }
            shareAccept.alertMessage = nil
            alertMessage = message
            showAlert = true
        }
        .onChange(of: syncthingManager.lastSyncTime, initial: true) { _, _ in
            maybePresentRelayUpsell()
            maybePresentNotificationPrimer()
        }
        .onChange(of: hasAnySyncedContent) { _, _ in
            // The #94 content floor can become true in a poll that does not
            // move lastSyncTime — re-check so the card still lands at the
            // moment the first files actually arrive.
            maybePresentRelayUpsell()
        }
        .onChange(of: subscriptionManager.isRelaySubscribed) { _, _ in
            maybePresentRelayUpsell()
        }
        #if DEBUG
        .onAppear(perform: applyUIAuditFixture)
        .sheet(item: $uiAuditDetailFixture) { fixture in
            NavigationStack {
                switch fixture {
                case .deviceRemoval:
                    DeviceDetailView(
                        device: Self.uiAuditFixtureDevice,
                        syncthingManager: syncthingManager
                    )
                case .conflictResolve:
                    ConflictDiffView(
                        folderID: "uiaudit-vault",
                        conflict: Self.uiAuditFixtureConflict,
                        syncthingManager: syncthingManager
                    )
                }
            }
        }
        #endif
    }

    /// The Sync tab (#187, status first): the hero states one honest truth,
    /// then everything that needs the user, the pending shares, and the
    /// vaults — each in its own quiet card.
    private var syncTab: some View {
        NavigationStack(path: $syncPath) {
            VaultPage {
                statusHero
                oneTimeAsks
                obsidianAccessCard
                relayAttentionCard
                syncIssuesSection
                pendingSharesSection
                vaultsSection
                relayPromo
            }
            .refreshable {
                // Re-detect vaults created in Obsidian since the last scan
                // (#95). Read-only: republishes detectedVaults only — the
                // accept pass keys on pendingFolders/settlement, never on this.
                vaultManager.scanForVaults()
                await syncthingManager.performForegroundSync()
            }
            .navigationTitle(L10n.tr("Sync"))
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showSettings = true
                    } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityLabel(L10n.tr("Open Settings"))
                    .accessibilityHint(L10n.tr("Opens discovery, relay, and notification settings."))
                }
            }
            .navigationDestination(for: SyncRoute.self) { route in
                syncDestination(route)
            }
        }
    }

    /// The Devices tab — paired Syncthing peers and the add-device entry point.
    /// "Add" lives in the toolbar (the idiomatic spot), not in a section header.
    private var devicesTab: some View {
        NavigationStack(path: $devicesPath) {
            VaultPage {
                devicesContent
            }
            .navigationTitle(L10n.tr("Devices"))
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    // Two ways to add: a Hub by its pairing code (#174), or
                    // any Syncthing device by its Device ID.
                    Menu {
                        Button {
                            addHubRequest = AddHubRequest()
                        } label: {
                            Label(L10n.tr("Add Hub"), systemImage: "server.rack")
                        }
                        Button {
                            showAddDevice = true
                        } label: {
                            Label(L10n.tr("Add Device by ID"), systemImage: "laptopcomputer")
                        }
                    } label: {
                        Image(systemName: "plus")
                    }
                    .disabled(!syncthingManager.isRunning)
                    .accessibilityLabel(L10n.tr("Add Hub or Device"))
                    .accessibilityHint(L10n.tr("Pair with your VaultSync Hub by its code, or add a Syncthing device by its Device ID."))
                }
            }
            .navigationDestination(for: DeviceRoute.self) { route in
                deviceDestination(route)
            }
        }
    }

    /// The Relay tab — the unified Cloud Relay home (pitch + subscribe, or the
    /// cross-linked setup/verify funnel once subscribed).
    private var relayTab: some View {
        NavigationStack {
            RelayHomeView(
                syncthingManager: syncthingManager,
                subscriptionManager: subscriptionManager
            )
        }
    }

    // MARK: - Cloud Relay Upsell

    /// Presents the Cloud Relay offer at the "aha moment": the first time a real
    /// sync has completed while the user has at least one vault and is not
    /// subscribed. Shown as a dismissable dashboard card — never by silently
    /// switching tabs out from under the user. Acting on it (either way) retires
    /// it for good; the permanent dashboard affordance stays available.
    /// Any folder whose global index holds at least one file — proof that a
    /// remote index (or real local content known to the cluster) exists, and
    /// the #94 floor under the upsell for installs whose persisted last-sync
    /// date predates the honest detector.
    private var hasAnySyncedContent: Bool {
        syncthingManager.folderStatuses.values.contains { $0.globalFiles > 0 }
    }

    private func maybePresentRelayUpsell() {
        guard !subscriptionManager.isRelaySubscribed else {
            showRelayUpsellCard = false
            return
        }
        guard RelayUpsellGate.shouldPresent(
            isSubscribed: subscriptionManager.isRelaySubscribed,
            hasSyncFolders: !syncthingManager.folders.isEmpty,
            hasCompletedFirstSync: syncthingManager.lastSyncTime != nil,
            hasAnySyncedContent: hasAnySyncedContent,
            alreadyShown: UserDefaults.standard.bool(forKey: Self.relayUpsellShownKey)
        ) else { return }
        showRelayUpsellCard = true
    }

    private func dismissRelayUpsell(openRelay: Bool) {
        UserDefaults.standard.set(true, forKey: Self.relayUpsellShownKey)
        withAnimation(.snappy) { showRelayUpsellCard = false }
        if openRelay { selectedTab = .relay }
        // One ask at a time: the notification primer waits while the upsell
        // is visible — re-check now that it is gone (#69).
        maybePresentNotificationPrimer()
    }

    // MARK: - Notification Primer (#69)

    /// Present the primed notification ask: after the first completed sync
    /// (the first moment a conflict alert can matter), while permission is
    /// still undecided at the system level, and never on top of another
    /// dashboard ask. Acting on the card (either way) retires it for good;
    /// notifications remain reachable via iOS Settings.
    private func maybePresentNotificationPrimer() {
        guard NotificationPrimerGate.shouldCheck(
            alreadyHandled: UserDefaults.standard.bool(forKey: Self.notificationPrimerShownKey),
            hasSyncFolders: !syncthingManager.folders.isEmpty,
            hasCompletedFirstSync: syncthingManager.lastSyncTime != nil,
            otherCardVisible: showRelayUpsellCard
        ) else { return }
        Task {
            let settings = await UNUserNotificationCenter.current().notificationSettings()
            if NotificationPrimerGate.shouldPresent(authorizationStatus: settings.authorizationStatus) {
                showNotificationPrimerCard = true
            } else {
                // Already decided at the system level (e.g. the pre-1.8.0
                // onboarding prompt) — never primer again.
                UserDefaults.standard.set(true, forKey: Self.notificationPrimerShownKey)
            }
        }
    }

    private func dismissNotificationPrimer(enable: Bool) {
        UserDefaults.standard.set(true, forKey: Self.notificationPrimerShownKey)
        withAnimation(.snappy) { showNotificationPrimerCard = false }
        if enable {
            Task { await BackgroundSyncService.requestNotificationPermission() }
        }
    }

    // MARK: - Checklist Actions

    /// Remember a tapped checklist remediation. The settings sheet is
    /// dismissing when this fires, and presenting the next sheet during that
    /// transition gets silently dropped — so the action runs from the sheet's
    /// `onDismiss`, which fires only after the transition has fully completed
    /// (no timing guess).
    private func handleChecklistAction(_ action: SetupChecklistViewModel.ChecklistAction) {
        pendingChecklistAction = action
    }

    private func runPendingChecklistAction() {
        guard let action = pendingChecklistAction else { return }
        pendingChecklistAction = nil
        switch action {
        case .connectObsidian:
            showObsidianPicker = true
        case .addDevice:
            showAddDevice = true
        case .openRelayTab:
            selectedTab = .relay
        }
    }

    /// Post-add guidance (#95): the sheet used to close silently, and the
    /// most common pairing stall — the desktop never confirming the new
    /// device — was explained nowhere in the app.
    private func presentDeviceAddedHintIfNeeded() {
        guard showDeviceAddedHint else { return }
        showDeviceAddedHint = false
        infoMessage = L10n.tr("Device added. Now confirm this iPhone in Syncthing on your computer — a confirmation prompt appears there. Then share your vault to start syncing.")
        showInfoAlert = true
    }

    // MARK: - Add Hub (#174)

    @ViewBuilder
    private func addHubSheet(_ request: AddHubRequest) -> some View {
        #if DEBUG
        AddHubSheet(
            syncthingManager: syncthingManager,
            vaultManager: vaultManager,
            shareAccept: shareAccept,
            link: request.link,
            onReviewShares: { selectedTab = .sync },
            model: request.previewModel
        )
        #else
        AddHubSheet(
            syncthingManager: syncthingManager,
            vaultManager: vaultManager,
            shareAccept: shareAccept,
            link: request.link,
            onReviewShares: { selectedTab = .sync }
        )
        #endif
    }

    /// No sheet and no dialog is up, so an opened pairing link may present.
    private var canPresentHubLink: Bool {
        !showAddDevice && !showSettings && !showSetupChecklist && !showObsidianPicker
            && addHubRequest == nil && shareTargetPickerFolder == nil && pendingFilterSheetFolder == nil
            && vaultPendingRemoval == nil && shareAccept.pendingMergeConfirmation == nil
            && !showAlert && !showInfoAlert
            && pendingChecklistAction == nil && !showDeviceAddedHint
    }

    private func presentPendingHubLink() {
        guard hubLinkRouter.pending != nil, canPresentHubLink else { return }
        switch hubLinkRouter.take() {
        case .pair(let link):
            selectedTab = .devices
            addHubRequest = AddHubRequest(link: link)
        case .unusable(let problem):
            alertMessage = HubPairingCopy.scanProblem(problem)
            showAlert = true
        case nil:
            break
        }
    }

    // MARK: - UI-Audit Fixtures (#64/#65)

    #if DEBUG
    private enum UIAuditDetailFixture: String, Identifiable {
        case deviceRemoval
        case conflictResolve
        var id: String { rawValue }
    }

    /// LAB: seed the state behind a consent dialog or error row from the
    /// `-uiaudit-fixture` launch argument so it renders on a simulator
    /// without a paired peer or damaged on-disk state (#64/#65 audit
    /// evidence). Engine management is skipped for the whole fixture run —
    /// see UIAuditFixture. Compiled out of release builds.
    private func applyUIAuditFixture() {
        switch UIAuditFixture.active {
        case UIAuditFixture.mergeConsent:
            shareAccept.pendingMergeConfirmation = ShareAcceptCoordinator.MergeConfirmationRequest(
                folder: SyncthingManager.PendingFolderInfo(
                    id: "uiaudit-vault",
                    label: "Life Notes",
                    offeredBy: []
                ),
                targetName: "Life Notes"
            )
        case UIAuditFixture.removalConsent:
            vaultPendingRemoval = VaultRemovalTarget(id: "uiaudit-vault", label: "Life Notes")
        case UIAuditFixture.markerError:
            syncthingManager._testSetFolders([
                SyncthingManager.FolderInfo(
                    id: "uiaudit-vault",
                    label: "Life Notes",
                    path: "/var/mobile/Obsidian/Life Notes",
                    type: "sendreceive",
                    paused: false,
                    deviceIDs: []
                ),
            ])
            syncthingManager._testSetFolderStatuses([
                "uiaudit-vault": SyncthingManager.FolderStatusInfo(payload: .init(
                    state: "error",
                    stateChanged: "2026-07-07T10:00:00Z",
                    completionPct: 0,
                    globalBytes: 0,
                    globalFiles: 0,
                    localBytes: 0,
                    localFiles: 0,
                    needBytes: 0,
                    needFiles: 0,
                    inProgressBytes: 0,
                    errorReason: "unknown_error",
                    errorMessage: "folder marker missing (this indicates potential data loss, search docs/forum to get information about how to proceed)",
                    errorPath: "/var/mobile/Obsidian/Life Notes",
                    errorChanged: nil
                )),
            ])
        case UIAuditFixture.deviceRemovalConsent:
            uiAuditDetailFixture = .deviceRemoval
        case UIAuditFixture.conflictResolveConsent:
            uiAuditDetailFixture = .conflictResolve
        case UIAuditFixture.designPreview:
            DesignPreviewFixture.seed(syncthingManager: syncthingManager, vaultManager: vaultManager)
            openDesignPreviewScreen()
        default:
            break
        }
    }

    /// Opens the screen a design-preview run asks for (#187).
    private func openDesignPreviewScreen() {
        let notes = DesignPreviewFixture.notesID
        switch DesignPreviewFixture.screen {
        case .home, .attention, .syncing, .onboarding, .onboardingShare:
            break
        case .devices:
            selectedTab = .devices
        case .device:
            selectedTab = .devices
            devicesPath = [.device(id: DesignPreviewFixture.serverID)]
        case .vault:
            syncPath = [.vault(id: notes)]
        case .conflicts:
            syncPath = [.vault(id: notes), .conflicts(folderID: notes, pathPrefix: nil)]
        case .conflict:
            syncPath = [
                .vault(id: notes),
                .conflicts(folderID: notes, pathPrefix: nil),
                .conflict(folderID: notes, conflict: DesignPreviewFixture.conflict),
            ]
        case .relay:
            selectedTab = .relay
        case .settings:
            showSettings = true
        case .addHub:
            selectedTab = .devices
            addHubRequest = AddHubRequest(previewModel: DesignPreviewFixture.hubPairingModel(syncthingManager: syncthingManager))
        }
    }

    private static let uiAuditFixtureDevice: SyncthingManager.DeviceInfo = {
        // DeviceInfo has a custom Decodable init and no memberwise init —
        // decoding a literal is the fixture's only construction path.
        try! JSONDecoder().decode(
            SyncthingManager.DeviceInfo.self,
            from: Data(#"{"deviceID":"UIAUDIT-DEVICE","name":"Desktop","connected":true}"#.utf8)
        )
    }()

    private static let uiAuditFixtureConflict = SyncthingManager.ConflictInfo(
        originalPath: "Notes/daily.md",
        conflictPath: "Notes/daily.sync-conflict-20260707-101010-UIAUDIT.md",
        conflictDate: "2026-07-07T10:10:10Z",
        deviceShortID: "UIAUDIT"
    )
    #endif

    // MARK: - Status Hero

    /// The status-first hero (#187): one honest state for the whole app —
    /// the decision-012 cascade in `SyncHeaderModel`, unchanged — plus the
    /// facts behind it as chips. In the setup states the hero is the button
    /// into the checklist (#95): "Finish Setup" / "Action Needed" name a task
    /// whose checklist was three non-obvious hops away.
    @ViewBuilder
    private var statusHero: some View {
        let header = headerState
        let opensChecklist = SyncHeaderModel.opensChecklist(titleKey: header.titleKey)
        let hero = StatusHeroCard(
            status: header.status,
            title: L10n.tr(header.titleKey),
            subtitle: headerSubtitle,
            busy: shouldShowReconnectingUI,
            showsDisclosure: opensChecklist
        ) {
            heroChips
        }
        if opensChecklist {
            Button {
                showSetupChecklist = true
            } label: {
                hero
            }
            .buttonStyle(.plain)
            .accessibilityHint(L10n.tr("Opens the setup checklist."))
        } else {
            hero
        }
    }

    @ViewBuilder
    private var heroChips: some View {
        let vaultCount = vaultRows.count
        if vaultCount > 0 {
            StatusChip(
                text: vaultCount == 1 ? L10n.tr("1 vault") : L10n.fmt("%d vaults", vaultCount),
                systemImage: "folder"
            )
        }
        if syncthingManager.isRunning {
            devicesChip
        }
        if relayDashboardState == .active {
            // "active" means a REAL wake-up has actually reached this device
            // — not merely "provisioned + reachable" (K1). Same words as the
            // Relay tab's steady state, so the two screens never disagree.
            StatusChip(
                text: L10n.tr("Cloud Relay active"),
                tone: .accent,
                systemImage: "antenna.radiowaves.left.and.right"
            )
        }
    }

    /// Peer connectivity — the former dashboard row, now a chip. While every
    /// disconnected device is still inside its reconnect grace window, "0 of
    /// N connected" is normal warm-up, not a problem: a calm "connecting"
    /// chip instead of a warning tone, which stays reserved for devices that
    /// stayed disconnected beyond the grace period.
    @ViewBuilder
    private var devicesChip: some View {
        let connected = syncthingManager.devices.filter(\.connected).count
        let total = syncthingManager.devices.count
        let reconnectingDevices = syncthingManager.devices.filter { !$0.connected && !$0.paused }
        let isWarmingUp = connected == 0 && total > 0
            && !reconnectingDevices.isEmpty
            && reconnectingDevices.allSatisfy {
                syncthingManager.isWithinReconnectGrace(deviceID: $0.deviceID)
            }
        if total == 0 {
            StatusChip(text: L10n.tr("No devices configured"), systemImage: "laptopcomputer.and.iphone")
        } else if isWarmingUp {
            StatusChip(
                text: L10n.tr("Connecting to devices…"),
                tone: .starting,
                systemImage: "laptopcomputer.and.iphone"
            )
        } else {
            StatusChip(
                text: L10n.fmt("%d of %d devices connected", connected, total),
                tone: connected > 0 ? .success : .attention,
                systemImage: "laptopcomputer.and.iphone"
            )
        }
    }

    private var isSyncing: Bool {
        syncthingManager.folderStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }
    }

    private var isReconnecting: Bool {
        !syncthingManager.reconnectingRequiredDeviceIDs.isEmpty
    }

    /// True iff the reconnecting visuals (spinner + "Connecting to…"
    /// caption) should actually be shown. Higher-priority states (errors,
    /// "Starting…", folder errors) suppress the indicator instead of competing
    /// with it for visual hierarchy. The hero title itself stays positive —
    /// a grace-window reconnect is normal warm-up, not a problem state.
    private var shouldShowReconnectingUI: Bool {
        currentSyncError == nil
            && syncthingManager.isRunning
            && foldersWithErrors.isEmpty
            && isReconnecting
    }

    /// Canonical header state (#66, decision 012): glyph, color, and title
    /// derive from ONE source of truth — the same issue list the "Sync Issues"
    /// section renders — so the hero can never claim "All Synced" while an
    /// issue card is visible below it. The cascade itself lives in the pure,
    /// unit-tested `SyncHeaderModel`.
    ///
    /// A reconnect inside its grace window deliberately does NOT change the
    /// status: being briefly disconnected after a cold start is Syncthing's
    /// normal warm-up, so the hero keeps its positive state and only the
    /// busy spinner + subtitle communicate "connecting".
    private var headerState: SyncHeaderModel.State {
        SyncHeaderModel.derive(.init(
            hasEngineError: currentSyncError != nil,
            engineRunning: syncthingManager.isRunning,
            issueSeverities: syncthingManager.unresolvedIssues.map(\.severity),
            hasUnreachableFolders: !syncthingManager.unreachableFolders.isEmpty,
            isSyncing: isSyncing,
            hasSyncFolders: !syncthingManager.folders.isEmpty,
            vaultAccessible: vaultManager.isAccessible,
            vaultNeedsReconnect: vaultManager.needsReconnect,
            hasDetectedVaults: !vaultManager.detectedVaults.isEmpty,
            hasVaultsAwaitingFirstSync: hasVaultsAwaitingFirstSync
        ))
    }

    /// Any vault row that reads "Waiting for first sync" — taken from the
    /// rows' own model, so the hero can never say "All Synced" above one.
    private var hasVaultsAwaitingFirstSync: Bool {
        let unreachableIDs = unreachableFolderIDs
        return vaultRows.contains {
            vaultState($0, unreachableIDs: unreachableIDs).label == .waitingForFirstSync
        }
    }

    /// Secondary line for the status hero — the reconnecting progress or the
    /// last-sync relative time.
    private var headerSubtitle: String? {
        if shouldShowReconnectingUI {
            let ids = syncthingManager.reconnectingRequiredDeviceIDs
            if ids.count == 1,
               let device = syncthingManager.devices.first(where: { $0.deviceID == ids[0] }),
               !device.name.isEmpty {
                return L10n.fmt("Connecting to %@…", device.name)
            }
            return ids.count == 1
                ? L10n.tr("Connecting to 1 device…")
                : L10n.fmt("Connecting to %d devices…", ids.count)
        }
        if let lastSync = syncthingManager.lastSyncTime {
            return L10n.fmt("Last sync: %@", Self.lastSyncFormatter.localizedString(for: lastSync, relativeTo: Date()))
        }
        return nil
    }

    private var foldersWithErrors: [String] {
        syncthingManager.folderIDsWithErrors
    }

    private var currentSyncError: SyncUserError? {
        if let userError = syncthingManager.userError {
            return userError
        }
        if let error = syncthingManager.error {
            return mappedError(error)
        }
        return nil
    }

    // MARK: - One-Time Asks

    @ViewBuilder
    private var oneTimeAsks: some View {
        if showRelayUpsellCard {
            relayUpsellCard
        }
        if showNotificationPrimerCard {
            notificationPrimerCard
        }
    }

    /// The one-time Cloud Relay offer, shown as a dismissable card the first
    /// time a real sync completes (the "aha moment"). Replaces the old
    /// behavior of silently switching the selected tab, which yanked users out
    /// of whatever they were doing mid-celebration.
    private var relayUpsellCard: some View {
        askCard(
            systemImage: "antenna.radiowaves.left.and.right",
            title: L10n.tr("Get instant updates"),
            message: L10n.tr("Your first sync is done. Cloud Relay wakes this iPhone the moment your notes change — even while the app is closed."),
            confirmTitle: L10n.tr("View Cloud Relay"),
            onConfirm: { dismissRelayUpsell(openRelay: true) },
            onDecline: { dismissRelayUpsell(openRelay: false) }
        )
    }

    /// The primed notification ask (#69): explains WHY notifications help
    /// (conflict alerts) before any system prompt appears — replacing the
    /// bare permission dialog that used to fire over the empty main screen
    /// the moment onboarding completed. Only the explicit button triggers
    /// the system prompt.
    private var notificationPrimerCard: some View {
        askCard(
            systemImage: "bell.badge",
            title: L10n.tr("Get notified about conflicts"),
            message: L10n.tr("If a note changes on two devices at the same time, VaultSync can alert you so you can choose which version to keep."),
            confirmTitle: L10n.tr("Enable Notifications"),
            onConfirm: { dismissNotificationPrimer(enable: true) },
            onDecline: { dismissNotificationPrimer(enable: false) }
        )
    }

    /// One dismissable ask. `.contain`, not `.combine`: the card holds two
    /// buttons that must stay independently focusable for VoiceOver.
    private func askCard(
        systemImage: String,
        title: String,
        message: String,
        confirmTitle: String,
        onConfirm: @escaping () -> Void,
        onDecline: @escaping () -> Void
    ) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            HStack(spacing: VaultSpacing.m) {
                Image(systemName: systemImage)
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Color.vaultAccent)
                    .frame(width: 36, height: 36)
                    .background(Color.vaultAccentFill, in: Circle())
                    .accessibilityHidden(true)
                Text(title)
                    .font(.headline)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Text(message)
                .font(.subheadline)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .fixedSize(horizontal: false, vertical: true)
            VaultButtonRow {
                Button(confirmTitle, action: onConfirm)
                    .buttonStyle(.vault(.primary, compact: true))
                Button(L10n.tr("Not now"), action: onDecline)
                    .buttonStyle(.vault(.neutral, compact: true))
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard()
        .accessibilityElement(children: .contain)
    }

    // MARK: - Obsidian Access

    /// Without folder access nothing can sync or be accepted, so this card
    /// sits right under the hero instead of inside a section.
    @ViewBuilder
    private var obsidianAccessCard: some View {
        if !vaultManager.isAccessible {
            ActionCard(
                status: .attention,
                title: vaultManager.needsReconnect
                    ? L10n.tr("Obsidian access expired")
                    : L10n.tr("Obsidian folder not connected"),
                message: obsidianAccessMessage,
                actionTitle: vaultManager.needsReconnect
                    ? L10n.tr("Reconnect Obsidian Folder")
                    : L10n.tr("Connect Obsidian Folder"),
                action: { showObsidianPicker = true },
                secondary: { AnyView(obsidianAccessFooter) }
            )
        }
    }

    private var obsidianAccessMessage: String {
        if let issue = vaultManager.accessIssue {
            return joinedErrorMessage(issue.message, issue.remediation)
        }
        return L10n.tr("VaultSync needs one-time access to your Obsidian folder before it can accept shares.")
    }

    /// Picker guidance + first-install help below the connect button.
    private var obsidianAccessFooter: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            if vaultManager.accessIssue != nil,
               let url = SyncUserError.troubleshootingURL(anchor: vaultManager.needsReconnect ? "bookmark-access-expired" : "obsidian-folder-not-found") {
                ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                    .font(.footnote)
            }

            Text(L10n.tr("In the picker, choose \"On My iPhone\" → \"Obsidian\", then tap Open."))
                .font(.footnote)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .fixedSize(horizontal: false, vertical: true)

            DisclosureGroup(L10n.tr("Can't find the Obsidian folder?")) {
                Text(L10n.tr("Install Obsidian from the App Store and open it once. The folder appears after Obsidian creates it."))
                    .font(.footnote)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, VaultSpacing.xs)
            }
            .font(.footnote)
            .tint(Color.vaultAccentText)
            // The sole path to first-install help — a footnote-sized label is
            // far under the 44pt minimum tap target (#69).
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
    }

    // MARK: - Sync Issues

    /// Everything that keeps a vault from syncing, under the established
    /// "Sync Issues" name the docs and store copy point to: an engine
    /// failure, vaults on a dead path, folders in error, and the issue list
    /// — exactly the inputs of the hero's cascade (decision 012), so the
    /// hero never says "All Synced" above a card in this section.
    @ViewBuilder
    private var syncIssuesSection: some View {
        // A pending share is presented once, in Pending Shares below, with
        // its name, sender and choices; as an issue card it repeated itself
        // with a context-free "Accept Pending Share" (#187 review). It still
        // drives the hero through the full list (`headerState`).
        let issues = syncthingManager.unresolvedIssues.filter { $0.kind != .pendingShares }
        let unreachable = syncthingManager.unreachableFolders
        let folderErrorIDs = folderErrorCardIDs(excluding: unreachable)
        if currentSyncError != nil || !unreachable.isEmpty || !folderErrorIDs.isEmpty || !issues.isEmpty {
            VaultSectionHeader(L10n.tr("Sync Issues"))
            if let error = currentSyncError {
                ActionCard(
                    status: .error,
                    title: error.title,
                    message: joinedErrorMessage(error.message, error.remediation),
                    secondary: troubleshootingSecondary(for: error)
                )
            }
            ForEach(unreachable) { folder in
                unreachableVaultCard(folder)
            }
            if !unreachable.isEmpty {
                sectionFootnote(L10n.tr("Removing a vault only stops syncing it on this iPhone. The notes on your other devices are not affected."))
            }
            ForEach(folderErrorIDs, id: \.self) { folderID in
                folderErrorCard(folderID)
            }
            SyncIssuesView(
                issues: issues,
                syncthingManager: syncthingManager,
                onRescanFailedFolders: rescanFailedVaults,
                onOpenAddDevice: { showAddDevice = true },
                onRescanAllVaults: rescanAllVaults
            )
        }
    }

    /// Folders in error that get their own card. Vaults on a dead path are
    /// left out: their recovery card above already says what is wrong and
    /// offers the way out — the same exclusion the issue list makes.
    private func folderErrorCardIDs(excluding unreachable: [SyncthingManager.UnreachableFolder]) -> [String] {
        let unreachableIDs = Set(unreachable.map(\.id))
        return foldersWithErrors.filter { !unreachableIDs.contains($0) }
    }

    private func folderErrorCard(_ folderID: String) -> some View {
        let folder = syncthingManager.folders.first { $0.id == folderID }
        let folderError = syncthingManager.folderUserError(folderID: folderID)
        return ActionCard(
            status: .attention,
            title: folder?.label ?? folderID,
            message: joinedErrorMessage(
                folderError?.message ?? L10n.tr("Folder is currently in an error state."),
                folderError?.remediation ?? ""
            ),
            secondary: folderError.flatMap { troubleshootingSecondary(for: $0) }
        )
    }

    /// A folder the launch-time path reconcile could not heal (a stale
    /// app-container path, issue #25), with a guided way out — reconnect to
    /// the Obsidian directory if the folder maps to it, or remove it outright.
    private func unreachableVaultCard(_ folder: SyncthingManager.UnreachableFolder) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.m) {
            // Texts read as ONE VoiceOver element (name + what is wrong
            // together); the buttons stay independently focusable (#71).
            HStack(alignment: .top, spacing: VaultSpacing.m) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.title3)
                    .foregroundStyle(Color.statusAttention)
                    .frame(width: 28)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                    Text(folder.label)
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Color.vaultLabel)
                    Text(L10n.tr("This vault points to storage that no longer exists on this iPhone, so it can no longer sync."))
                        .font(.subheadline)
                        .foregroundStyle(Color.vaultSecondaryLabel)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityElement(children: .combine)
            VaultButtonRow {
                if folder.hasObsidianMapping {
                    Button(L10n.tr("Reconnect to Obsidian")) {
                        showObsidianPicker = true
                    }
                    .buttonStyle(.vault(.primary, compact: true))
                }
                Button(role: .destructive) {
                    vaultPendingRemoval = VaultRemovalTarget(id: folder.id, label: folder.label)
                } label: {
                    Text(L10n.tr("Remove This Vault"))
                }
                .buttonStyle(.vault(.destructive, compact: true))
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(tone: .attention)
    }

    private func rescanFailedVaults() {
        // Don't rescan folders surfaced as unreachable — a rescan can't fix a
        // stale/missing path, so it would be a no-op recovery for those.
        let unreachable = Set(syncthingManager.unreachableFolders.map(\.id))
        rescanFolders(ids: syncthingManager.folderIDsWithErrors.filter { !unreachable.contains($0) })
    }

    private func rescanAllVaults() {
        rescanFolders(ids: syncthingManager.folders.map(\.id))
    }

    private func rescanFolders(ids: [String]) {
        let uniqueIDs = Array(Set(ids)).sorted()
        guard !uniqueIDs.isEmpty else { return }

        var failures: [String] = []
        for id in uniqueIDs {
            if let err = syncthingManager.rescanFolder(id: id) {
                let folderName = syncthingManager.folders.first(where: { $0.id == id })?.label ?? id
                let userError = mappedError(err, fallbackTitle: L10n.tr("Rescan Failed"))
                failures.append(L10n.fmt("%@: %@", folderName, userError.message))
            }
        }

        if !failures.isEmpty {
            alertMessage = failures.joined(separator: "\n")
            showAlert = true
        }
    }

    // MARK: - Cloud Relay on the Home Screen

    private enum RelayDashboardState {
        case hidden
        case promo
        case active
        case needsReactivation
        case wentQuiet
        case oneStepLeft
    }

    /// Which Cloud Relay element the home screen carries — the former
    /// dashboard if/else chain, unchanged, now split by placement: `.active`
    /// becomes a hero chip, the three unfinished states sit at the top as
    /// before, the `.promo` row sits quietly below the vaults. None of them
    /// joins Sync Issues: syncing works without the Relay, so its state is
    /// no input of the hero (decision 012) and must not look like one.
    private var relayDashboardState: RelayDashboardState {
        if subscriptionManager.isRelaySubscribed {
            // A1 — paid-but-never-activated (the "dead sub" cohort: subscribed,
            // never woken, past the grace period). A self-test does NOT clear
            // this — only a real server wake-up does.
            if subscriptionManager.needsRelayReactivation { return .needsReactivation }
            if subscriptionManager.relayDeliveryConfirmed { return .active }
            // Delivered before, but no recent wake-up — setup IS done; the
            // helper just went quiet. Don't tell them to "set up" again.
            if subscriptionManager.lastRelayTriggerReceivedAt != nil { return .wentQuiet }
            // Subscribed but never delivered yet (within grace) — finish the
            // one missing setup step.
            return .oneStepLeft
        }
        if !syncthingManager.folders.isEmpty, !showRelayUpsellCard {
            return .promo
        }
        return .hidden
    }

    @ViewBuilder
    private var relayAttentionCard: some View {
        switch relayDashboardState {
        case .needsReactivation:
            relayNavCard(
                title: L10n.tr("Finish activating Cloud Relay"),
                subtitle: L10n.tr("You’re subscribed, but your server has never woken this iPhone. One step finishes setup."),
                systemImage: "antenna.radiowaves.left.and.right.slash",
                tone: .attention,
                hint: L10n.tr("Opens Cloud Relay setup.")
            )
        case .wentQuiet:
            relayNavCard(
                title: L10n.tr("Cloud Relay went quiet"),
                subtitle: L10n.tr("No wake-up in a while. If nothing changed in your vault, that can be normal — otherwise check that your server is on."),
                systemImage: "antenna.radiowaves.left.and.right",
                tone: .attention
            )
        case .oneStepLeft:
            relayNavCard(
                title: L10n.tr("One step left to activate"),
                subtitle: L10n.tr("Set up the server helper"),
                systemImage: "antenna.radiowaves.left.and.right",
                tone: .attention
            )
        case .hidden, .promo, .active:
            EmptyView()
        }
    }

    @ViewBuilder
    private var relayPromo: some View {
        if relayDashboardState == .promo {
            relayNavCard(
                title: L10n.tr("Get instant updates"),
                subtitle: L10n.tr("Turn on Cloud Relay"),
                systemImage: "antenna.radiowaves.left.and.right",
                tone: nil
            )
        }
    }

    /// One card that routes into the Relay tab — shared by the promo, "finish
    /// setup", recovery, and reactivation states so all four read as the
    /// same kind of row.
    private func relayNavCard(
        title: String,
        subtitle: String,
        systemImage: String,
        tone: VaultTone?,
        hint: String? = nil
    ) -> some View {
        VaultCardGroup(tone: tone) {
            Button {
                selectedTab = .relay
            } label: {
                VaultRow(title, subtitle: subtitle, systemImage: systemImage, iconTint: tone?.tint ?? .vaultAccent) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
            .accessibilityHint(hint ?? "")
        }
    }

    // MARK: - Pending Shares

    @ViewBuilder
    private var pendingSharesSection: some View {
        let pendingFolders = syncthingManager.actionablePendingFolders
        let ignoredFolders = syncthingManager.ignoredPendingFolders
        if !pendingFolders.isEmpty || !ignoredFolders.isEmpty {
            VaultSectionHeader(L10n.tr("Pending Shares"))
            PendingSharesView(
                pendingFolders: pendingFolders,
                ignoredFolders: ignoredFolders,
                failureByFolderID: shareAccept.pendingShareFailures,
                inFlightFolderIDs: shareAccept.pendingShareInFlight,
                obsidianAccessible: vaultManager.isAccessible,
                onAccept: { folder in
                    shareAccept.accept(folder, source: .manual)
                },
                onRetry: { folder in
                    shareAccept.retry(folder)
                },
                onIgnore: { folder in
                    shareAccept.ignore(folder)
                },
                onRestoreIgnored: { folder in
                    syncthingManager.unignorePendingFolder(id: folder.id)
                },
                onChooseTarget: { folder in
                    shareTargetPickerFolder = folder
                },
                onReconnectObsidian: {
                    showObsidianPicker = true
                }
            )
        }
    }

    // MARK: - Vaults

    @ViewBuilder
    private var vaultsSection: some View {
        VaultSectionHeader(L10n.tr("Obsidian Vaults"))
        if syncthingManager.folders.isEmpty && unsyncedVaultNames.isEmpty {
            vaultsEmptyState
        } else {
            let unreachableIDs = unreachableFolderIDs
            VaultCardGroup {
                ForEach(vaultRows) { item in
                    NavigationLink(value: SyncRoute.vault(id: item.id)) {
                        vaultRow(item, unreachableIDs: unreachableIDs)
                    }
                    .buttonStyle(.vaultRow)
                }
                ForEach(unsyncedVaultNames, id: \.self) { name in
                    unsyncedVaultRow(name)
                }
            }
        }
    }

    /// Detected vaults no Syncthing folder syncs yet (#79). Shown as passive
    /// rows so a connected-but-not-yet-shared setup doesn't read as "no
    /// vaults found" — the exact misdiagnosis from the #79 report.
    private var unsyncedVaultNames: [String] {
        UnsyncedVaultsModel.derive(
            detectedVaults: vaultManager.detectedVaults,
            folderPathsCanonLower: Set(syncthingManager.folders.map {
                Self.canonicalPath($0.path).lowercased()
            }),
            rootCanonLower: vaultManager.obsidianBasePath.map {
                Self.canonicalPath($0).lowercased()
            }
        )
    }

    /// Not navigable on purpose: the missing step (sharing) happens on the
    /// desktop, so the row can only explain that — there is no detail screen
    /// that would not be empty.
    private func unsyncedVaultRow(_ name: String) -> some View {
        VaultRow(
            name,
            subtitle: L10n.tr("Not syncing yet — share this vault from your computer to start."),
            systemImage: "folder",
            iconTint: .vaultSecondaryLabel
        ) {
            StatusDot(status: nil)
        }
    }

    /// A designed first-run state instead of a degenerate caption row — this is
    /// the screen a brand-new user stares at the longest. The "connect" case
    /// carries no button of its own: the access card above owns that action.
    @ViewBuilder
    private var vaultsEmptyState: some View {
        if !vaultManager.isAccessible {
            VaultEmptyState(
                systemImage: "folder.badge.gearshape",
                title: L10n.tr("Connect to Obsidian first"),
                message: L10n.tr("VaultSync needs one-time access to your Obsidian folder before it can accept shares.")
            )
        } else if vaultManager.detectedVaults.isEmpty {
            VaultEmptyState(
                systemImage: "folder.badge.questionmark",
                title: L10n.tr("No vaults found"),
                message: L10n.tr("Create a vault in Obsidian first. VaultSync will detect it automatically.")
            )
        } else {
            VaultEmptyState(
                systemImage: "arrow.triangle.2.circlepath",
                title: L10n.tr("No folders syncing yet"),
                message: L10n.tr("Share a folder from your desktop Syncthing — it will be accepted automatically.")
            )
        }
    }

    /// A single row in the "Obsidian Vaults" list. The list is keyed on the
    /// *vaults* the user actually has — matching the section title and what
    /// Obsidian itself shows — not on raw Syncthing sync folders. When one sync
    /// folder covers the whole Obsidian directory (the common setup: pick
    /// "On My iPhone/Obsidian"), it expands into one row per detected vault
    /// inside it; a per-vault sync folder maps 1:1. `vaultSubpath`/`relativePrefix`
    /// are non-nil only for the expanded directory case, where sync status,
    /// filters and devices are shared by the whole directory.
    private struct VaultRowItem: Identifiable {
        let id: String
        let name: String
        let folder: SyncthingManager.FolderInfo
        let vaultSubpath: String?
        let relativePrefix: String?
    }

    /// Build the displayed vault list from the detected vaults inside the synced
    /// Obsidian directory, mapped onto whichever Syncthing folder actually syncs
    /// them. Falls back to the folder itself when it isn't the Obsidian root
    /// (per-vault sync, or the root is itself a single vault).
    private var vaultRows: [VaultRowItem] {
        let base = vaultManager.obsidianBasePath.map(Self.canonicalPath)
        var rows: [VaultRowItem] = []
        for folder in syncthingManager.folders {
            let isWholeDirectory = base != nil && Self.canonicalPath(folder.path) == base
            if isWholeDirectory, !vaultManager.detectedVaults.isEmpty {
                for vault in vaultManager.detectedVaults {
                    rows.append(VaultRowItem(
                        id: "\(folder.id)/\(vault)",
                        name: vault,
                        folder: folder,
                        vaultSubpath: (folder.path as NSString).appendingPathComponent(vault),
                        relativePrefix: vault
                    ))
                }
            } else {
                rows.append(VaultRowItem(
                    id: folder.id,
                    name: folder.label.isEmpty ? folder.id : folder.label,
                    folder: folder,
                    vaultSubpath: nil,
                    relativePrefix: nil
                ))
            }
        }
        return rows
    }

    /// Normalize a path so the Syncthing folder path (stored at accept time) and
    /// the security-scoped bookmark path (resolved at launch) compare equal even
    /// across `/var`↔`/private/var` symlinks or a trailing slash.
    private static func canonicalPath(_ path: String) -> String {
        FolderPathReconciler.canonical(path)
    }

    private var unreachableFolderIDs: Set<String> {
        Set(syncthingManager.unreachableFolders.map(\.id))
    }

    /// Conflicts attributed to one vault: inside the vault's subdirectory for a
    /// directory-sync row, or all of the folder's conflicts for a 1:1 row.
    private func conflicts(for item: VaultRowItem) -> [SyncthingManager.ConflictInfo] {
        let all = syncthingManager.conflictFiles[item.folder.id] ?? []
        guard let vault = item.relativePrefix else { return all }
        return all.filter { $0.belongs(toVault: vault) }
    }

    /// Distinct conflicted files, not copies — same semantics as the
    /// home-screen issue (`SyncthingManager.unresolvedConflictCount`).
    private func conflictFileCount(for item: VaultRowItem) -> Int {
        Set(conflicts(for: item).map(\.originalPath)).count
    }

    /// The vault's one honest status (#187) — see `VaultStatusModel`.
    private func vaultState(_ item: VaultRowItem, unreachableIDs: Set<String>) -> VaultStatusModel.State {
        let status = syncthingManager.folderStatuses[item.folder.id]
        return VaultStatusModel.derive(.init(
            engineState: status?.state,
            folderPaused: item.folder.paused,
            unreachable: unreachableIDs.contains(item.folder.id),
            conflictCount: conflictFileCount(for: item),
            hasCompletedSync: syncthingManager.lastSyncTimeByFolder[item.folder.id] != nil,
            completionPct: status?.completionPct
        ))
    }

    /// A vault row: name, file count and the vault's own status, with the
    /// status dot and — when a transfer or failure outranks the conflict
    /// label — the conflict count.
    private func vaultRow(_ item: VaultRowItem, unreachableIDs: Set<String>) -> some View {
        let state = vaultState(item, unreachableIDs: unreachableIDs)
        let conflictCount = conflictFileCount(for: item)
        return VaultRow(
            item.name,
            subtitle: vaultRowSubtitle(item, state: state),
            systemImage: "folder",
            iconTint: state.status == nil ? .vaultSecondaryLabel : .vaultAccent
        ) {
            if conflictCount > 0, state.label != .conflicts(conflictCount) {
                StatusChip(text: "\(conflictCount)", tone: .attention, systemImage: "exclamationmark.triangle.fill")
                    .accessibilityLabel(VaultStatusModel.Label.conflicts(conflictCount).text)
            }
            StatusDot(status: state.status)
            VaultChevron()
        }
    }

    private func vaultRowSubtitle(_ item: VaultRowItem, state: VaultStatusModel.State) -> String {
        // A directory row shares the folder's totals with its sibling vaults —
        // attributing them to one vault would overstate it.
        guard item.relativePrefix == nil,
              let status = syncthingManager.folderStatuses[item.folder.id],
              status.localFiles > 0 else {
            return state.label.text
        }
        return Self.fileCountText(status.localFiles) + " · " + state.label.text
    }

    private static func fileCountText(_ count: Int) -> String {
        count == 1 ? L10n.tr("1 file") : L10n.fmt("%@ files", count.formatted())
    }

    // MARK: - Vault Detail

    @ViewBuilder
    private func syncDestination(_ route: SyncRoute) -> some View {
        switch route {
        case .vault(let id):
            if let item = vaultRows.first(where: { $0.id == id }) {
                vaultDetailView(item)
            } else {
                // The vault stopped syncing while its screen was open
                // (removed on this iPhone or on the desktop).
                VaultPage {
                    VaultEmptyState(
                        systemImage: "folder.badge.minus",
                        title: L10n.tr("This vault is no longer synced on this iPhone.")
                    )
                }
            }
        case .conflicts(let folderID, let pathPrefix):
            ConflictListView(
                folderID: folderID,
                pathPrefix: pathPrefix,
                syncthingManager: syncthingManager
            )
        case .conflict(let folderID, let conflict):
            ConflictDiffView(
                folderID: folderID,
                conflict: conflict,
                syncthingManager: syncthingManager
            )
        case .filters(let folderID):
            IgnorePatternsView(
                folderID: folderID,
                syncthingManager: syncthingManager
            )
        }
    }

    private func vaultDetailView(_ item: VaultRowItem) -> some View {
        let folder = item.folder
        let status = syncthingManager.folderStatuses[folder.id]
        let state = vaultState(item, unreachableIDs: unreachableFolderIDs)
        let conflictCount = conflictFileCount(for: item)
        return VaultPage {
            StatusChip(
                text: state.label.text,
                tone: state.status?.tone ?? .neutral,
                systemImage: state.status?.symbolName ?? "questionmark.circle"
            )
            vaultSummary(item, status: status)
            if status?.state == "error",
               let folderError = syncthingManager.folderUserError(folderID: folder.id) {
                ActionCard(
                    status: .error,
                    title: folderError.title,
                    message: joinedErrorMessage(folderError.message, folderError.remediation),
                    secondary: troubleshootingSecondary(for: folderError)
                )
            }
            if conflictCount > 0 {
                vaultConflictsCard(item, count: conflictCount)
            }

            VaultSectionHeader(L10n.tr("Shared With"))
            sharedWithCard(folder)

            VaultSectionHeader(L10n.tr("Location"))
            VaultCardGroup {
                VaultRow(
                    locationTitle(for: item),
                    subtitle: item.vaultSubpath ?? folder.path,
                    systemImage: "folder",
                    iconTint: .vaultSecondaryLabel,
                    monospacedSubtitle: true
                )
            }

            VaultSectionHeader(L10n.tr("Options"))
            vaultOptionsCard(folder: folder, isScanning: status?.state == "scanning")

            if item.relativePrefix == nil {
                vaultRemoval(item)
            }
        }
        .navigationTitle(item.name)
        .navigationBarTitleDisplayMode(.large)
        .onAppear {
            // Don't nudge sync filters for a vault that can't sync at all.
            let isUnreachable = syncthingManager.unreachableFolders.contains { $0.id == folder.id }
            if !isUnreachable, !syncthingManager.hasShownRecommendationSheet(folderID: folder.id) {
                pendingFilterSheetFolder = folder
            }
        }
        .sheet(item: $pendingFilterSheetFolder) { folder in
            SyncFilterRecommendationSheet(
                folderID: folder.id,
                syncthingManager: syncthingManager
            )
        }
    }

    /// Files, size and device count for a vault with its own sync folder. A
    /// vault synced as part of the whole Obsidian directory shares those
    /// numbers with its siblings, so it says that instead of attributing the
    /// directory's totals to one vault.
    @ViewBuilder
    private func vaultSummary(_ item: VaultRowItem, status: SyncthingManager.FolderStatusInfo?) -> some View {
        if item.relativePrefix != nil {
            VaultNotice(
                systemImage: "folder",
                text: L10n.tr("Synced as part of your Obsidian directory. Sync filters and devices apply to the whole directory.")
            )
        } else if let status {
            vaultStatsCard(status: status, deviceCount: sharedDeviceCount(item.folder))
        }
    }

    private func vaultStatsCard(status: SyncthingManager.FolderStatusInfo, deviceCount: Int) -> some View {
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: VaultSpacing.m))
            : AnyLayout(HStackLayout(alignment: .top, spacing: VaultSpacing.m))
        return layout {
            statColumn(value: status.localFiles.formatted(), label: L10n.tr("Files"))
            statColumn(
                value: ByteCountFormatter.string(fromByteCount: status.localBytes, countStyle: .file),
                label: L10n.tr("On this iPhone")
            )
            statColumn(value: deviceCount.formatted(), label: L10n.tr("Devices"))
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard()
    }

    private func statColumn(value: String, label: String) -> some View {
        VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
            Text(value)
                .font(.title.weight(.bold))
                .foregroundStyle(Color.vaultLabel)
                .lineLimit(1)
                .minimumScaleFactor(0.6)
            Text(label)
                .font(.footnote)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    private func sharedDeviceCount(_ folder: SyncthingManager.FolderInfo) -> Int {
        syncthingManager.devices.filter { folder.deviceIDs.contains($0.deviceID) }.count
    }

    private func vaultConflictsCard(_ item: VaultRowItem, count: Int) -> some View {
        VaultCardGroup(tone: .attention) {
            NavigationLink(value: SyncRoute.conflicts(folderID: item.folder.id, pathPrefix: item.relativePrefix)) {
                VaultRow(
                    L10n.tr("Conflicts"),
                    subtitle: VaultStatusModel.Label.conflicts(count).text,
                    systemImage: "exclamationmark.triangle.fill",
                    iconTint: .statusAttention
                ) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
        }
    }

    /// Every paired device, each one tap away from sharing or unsharing this
    /// vault — the check carries the state, the subtitle the connection.
    @ViewBuilder
    private func sharedWithCard(_ folder: SyncthingManager.FolderInfo) -> some View {
        if syncthingManager.devices.isEmpty {
            VaultCardGroup {
                VaultRow(
                    L10n.tr("No devices configured"),
                    systemImage: "laptopcomputer",
                    iconTint: .vaultSecondaryLabel
                )
            }
        } else {
            VaultCardGroup {
                ForEach(syncthingManager.devices) { device in
                    sharedDeviceRow(device, folder: folder)
                }
            }
        }
    }

    /// A labeled switch per device: on means this vault is shared with it.
    /// The subtitle only says whether the device is online — a separate
    /// fact, which a bare checkmark next to it made easy to misread (#187
    /// review).
    private func sharedDeviceRow(_ device: SyncthingManager.DeviceInfo, folder: SyncthingManager.FolderInfo) -> some View {
        let isShared = folder.deviceIDs.contains(device.deviceID)
        let name = device.name.isEmpty ? L10n.tr("Unnamed") : device.name
        return Toggle(isOn: Binding(
            get: { isShared },
            set: { newValue in
                guard newValue != isShared else { return }
                toggleDeviceSharing(folderID: folder.id, deviceID: device.deviceID, isShared: isShared)
            }
        )) {
            HStack(spacing: VaultSpacing.m) {
                Image(systemName: "laptopcomputer")
                    .font(.title3)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .frame(width: 28)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                    Text(name)
                        .font(.body)
                        .foregroundStyle(Color.vaultLabel)
                    Text(devicePresence(device).label)
                        .font(.footnote)
                        .foregroundStyle(Color.vaultSecondaryLabel)
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
        .tint(Color.vaultAccent)
        .padding(.horizontal, VaultSpacing.l)
        .padding(.vertical, 10)
        .frame(minHeight: VaultMetrics.rowMinHeight)
        .accessibilityHint(isShared
            ? L10n.tr("Double-tap to stop sharing this vault with this device.")
            : L10n.tr("Double-tap to share this vault with this device."))
    }

    /// "Obsidian › Notes" when the vault lives under the connected Obsidian
    /// directory, the folder name otherwise; the full path stays below it.
    private func locationTitle(for item: VaultRowItem) -> String {
        let path = Self.canonicalPath(item.vaultSubpath ?? item.folder.path)
        if let base = vaultManager.obsidianBasePath.map(Self.canonicalPath),
           path == base || path.hasPrefix(base + "/") {
            let relative = path.dropFirst(base.count).split(separator: "/").map(String.init)
            return ([(base as NSString).lastPathComponent] + relative).joined(separator: " › ")
        }
        return (path as NSString).lastPathComponent
    }

    private func vaultOptionsCard(folder: SyncthingManager.FolderInfo, isScanning: Bool) -> some View {
        VaultCardGroup {
            NavigationLink(value: SyncRoute.filters(folderID: folder.id)) {
                VaultRow(
                    L10n.tr("Sync Filters"),
                    systemImage: "line.3.horizontal.decrease.circle",
                    iconTint: .vaultSecondaryLabel
                ) {
                    VaultChevron()
                }
            }
            .buttonStyle(.vaultRow)
            .accessibilityHint(L10n.tr("Choose what gets synced to this iPhone"))

            // Honest progress: the busy state reflects the folder's REAL
            // scan state from the engine, not a fixed timer.
            Button {
                if let err = syncthingManager.rescanFolder(id: folder.id) {
                    alertMessage = mappedError(err, fallbackTitle: L10n.tr("Rescan Failed")).userVisibleDescription
                    showAlert = true
                }
            } label: {
                VaultRow(
                    isScanning ? L10n.tr("Rescanning…") : L10n.tr("Rescan Vault"),
                    systemImage: "arrow.clockwise",
                    iconTint: .vaultSecondaryLabel,
                    busy: isScanning
                )
            }
            .buttonStyle(.vaultRow)
            .disabled(isScanning)
        }
    }

    /// A 1:1 sync folder maps to exactly one vault, so removing it is
    /// unambiguous. The call site leaves this out for an expanded directory
    /// row: a single folder backs many vaults there, and per-vault removal
    /// would silently drop the whole directory.
    @ViewBuilder
    private func vaultRemoval(_ item: VaultRowItem) -> some View {
        Button(role: .destructive) {
            vaultPendingRemoval = VaultRemovalTarget(id: item.folder.id, label: item.name)
        } label: {
            Label(L10n.tr("Remove Vault"), systemImage: "trash")
        }
        .buttonStyle(.vault(.destructive))
        .padding(.top, VaultSpacing.s)
        sectionFootnote(L10n.tr("Stops syncing this vault on this iPhone. The notes on your other devices are not affected."))
    }

    private func toggleDeviceSharing(folderID: String, deviceID: String, isShared: Bool) {
        let result: String?
        if isShared {
            result = syncthingManager.unshareFolderFromDevice(folderID: folderID, deviceID: deviceID)
        } else {
            result = syncthingManager.shareFolderWithDevice(folderID: folderID, deviceID: deviceID)
        }
        if let err = result {
            alertMessage = mappedError(err).userVisibleDescription
            showAlert = true
        }
    }

    private var removalBinding: Binding<Bool> {
        Binding(get: { vaultPendingRemoval != nil }, set: { if !$0 { vaultPendingRemoval = nil } })
    }

    private var mergeConfirmationBinding: Binding<Bool> {
        Binding(get: { shareAccept.pendingMergeConfirmation != nil }, set: { if !$0 { shareAccept.pendingMergeConfirmation = nil } })
    }

    private func removeVault(id: String) {
        vaultPendingRemoval = nil
        if let err = syncthingManager.removeFolder(id: id) {
            alertMessage = mappedError(err, fallbackTitle: L10n.tr("Could Not Remove Vault")).userVisibleDescription
            showAlert = true
        } else if syncPath.contains(.vault(id: id)) {
            // Its screen could only say "no longer synced" now.
            syncPath.removeAll()
        }
    }

    // MARK: - Devices

    @ViewBuilder
    private var devicesContent: some View {
        if syncthingManager.devices.isEmpty {
            VaultEmptyState(
                systemImage: "laptopcomputer.and.iphone",
                title: L10n.tr("No devices configured"),
                message: L10n.tr("Pair with your VaultSync Hub by the code it printed, or add a device by its Syncthing Device ID — in the Syncthing web UI under Actions > Show ID.")
            ) {
                VaultButtonRow {
                    Button(L10n.tr("Add Hub")) {
                        addHubRequest = AddHubRequest()
                    }
                    .buttonStyle(.vault(.primary, compact: true))
                    Button(L10n.tr("Add Device")) {
                        showAddDevice = true
                    }
                    .buttonStyle(.vault(.neutral, compact: true))
                }
                .disabled(!syncthingManager.isRunning)
            }
        } else {
            VaultCardGroup {
                ForEach(syncthingManager.devices) { device in
                    NavigationLink(value: DeviceRoute.device(id: device.deviceID)) {
                        deviceRow(device)
                    }
                    .buttonStyle(.vaultRow)
                }
            }
        }
    }

    @ViewBuilder
    private func deviceDestination(_ route: DeviceRoute) -> some View {
        switch route {
        case .device(let id):
            if let device = syncthingManager.devices.first(where: { $0.deviceID == id }) {
                DeviceDetailView(device: device, syncthingManager: syncthingManager)
            } else {
                VaultPage {
                    VaultEmptyState(
                        systemImage: "laptopcomputer.slash",
                        title: L10n.tr("This device is no longer paired with this iPhone.")
                    )
                }
            }
        }
    }

    /// One device row. Disconnection is presented in escalating, honest steps:
    /// a spinner + "Connecting…" while the reconnect grace window runs (normal
    /// after a cold start), then a neutral gray "Offline" — never a red ✕,
    /// which reads as failure although disconnected peers are a normal state
    /// for an offline-first sync tool.
    private func deviceRow(_ device: SyncthingManager.DeviceInfo) -> some View {
        let presence = devicePresence(device)
        return VaultRow(
            device.name.isEmpty ? L10n.tr("Unnamed") : device.name,
            subtitle: presence.label,
            systemImage: "laptopcomputer",
            iconTint: .vaultSecondaryLabel,
            busy: presence.busy
        ) {
            StatusDot(status: presence.status)
            VaultChevron()
        }
    }

    private struct DevicePresence {
        let status: SyncStatus
        let label: String
        let busy: Bool
    }

    private func devicePresence(_ device: SyncthingManager.DeviceInfo) -> DevicePresence {
        if device.connected {
            return DevicePresence(status: .synced, label: L10n.tr("Connected"), busy: false)
        }
        if device.paused {
            return DevicePresence(status: .paused, label: L10n.tr("Paused"), busy: false)
        }
        if syncthingManager.isWithinReconnectGrace(deviceID: device.deviceID) {
            return DevicePresence(status: .starting, label: L10n.tr("Connecting…"), busy: true)
        }
        return DevicePresence(status: .paused, label: L10n.tr("Offline"), busy: false)
    }

    // MARK: - Helpers

    /// Message + remediation as one card body, skipping empty parts.
    private func joinedErrorMessage(_ message: String, _ remediation: String) -> String {
        [message, remediation].filter { !$0.isEmpty }.joined(separator: "\n\n")
    }

    /// The "Learn how to fix" link as an ActionCard secondary slot, when the
    /// error maps to a troubleshooting anchor.
    private func troubleshootingSecondary(for error: SyncUserError) -> (() -> AnyView)? {
        guard let url = troubleshootingURL(for: error) else { return nil }
        return {
            AnyView(
                ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                    .font(.footnote)
            )
        }
    }

    private func sectionFootnote(_ text: String) -> some View {
        Text(text)
            .font(.footnote)
            .foregroundStyle(Color.vaultSecondaryLabel)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, VaultSpacing.xs)
    }

    private func mappedError(_ error: String, fallbackTitle: String = L10n.tr("Sync Error")) -> SyncUserError {
        SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle)
    }

    private func troubleshootingURL(for error: SyncUserError) -> URL? {
        SyncUserError.troubleshootingURL(for: error)
    }
}

#Preview {
    let syncthing = SyncthingManager()
    let vault = VaultManager()
    ContentView(
        syncthingManager: syncthing,
        vaultManager: vault,
        subscriptionManager: SubscriptionManager(),
        shareAccept: ShareAcceptCoordinator(
            environment: .live(syncthingManager: syncthing, vaultManager: vault)
        ),
        hubLinkRouter: HubLinkRouter()
    )
}

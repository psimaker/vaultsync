import SwiftUI
import UserNotifications

struct ContentView: View {
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    var shareAccept: ShareAcceptCoordinator
    @State private var showAddDevice = false
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

    private let accent = Color.vaultAccent

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
        .sheet(isPresented: $showSettings, onDismiss: runPendingChecklistAction) {
            SettingsView(
                syncthingManager: syncthingManager,
                vaultManager: vaultManager,
                subscriptionManager: subscriptionManager,
                onChecklistAction: handleChecklistAction
            )
        }
        // Direct checklist entry from the tappable status header (#95) —
        // the same runPendingChecklistAction onDismiss plumbing as Settings,
        // so checklist remediations present after the transition finishes.
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

    /// The Sync tab — the vault's live-status story: the pinned status header,
    /// sync issues, Obsidian connection, pending shares, and the vault list.
    private var syncTab: some View {
        NavigationStack {
            List {
                dashboardSection
                syncIssuesSection
                obsidianStatusSection
                pendingSharesSection
                unreachableVaultsSection
                vaultsSection
            }
            .refreshable {
                // Re-detect vaults created in Obsidian since the last scan
                // (#95). Read-only: republishes detectedVaults only — the
                // accept pass keys on pendingFolders/settlement, never on this.
                vaultManager.scanForVaults()
                await syncthingManager.performForegroundSync()
            }
            .safeAreaInset(edge: .top, spacing: 0) {
                let header = headerState
                let headerView = SyncStatusHeader(
                    status: header.status,
                    title: L10n.tr(header.titleKey),
                    subtitle: headerSubtitle,
                    busy: shouldShowReconnectingUI
                )
                // "Finish Setup" / "Action Needed" name a task whose checklist
                // was three non-obvious hops away (#95) — the header itself is
                // the affordance in those states.
                if SyncHeaderModel.opensChecklist(titleKey: header.titleKey) {
                    Button {
                        showSetupChecklist = true
                    } label: {
                        headerView
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint(L10n.tr("Opens the setup checklist."))
                } else {
                    headerView
                }
            }
            .navigationTitle(L10n.tr("VaultSync"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        showSettings = true
                    } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityLabel("Open Settings")
                    .accessibilityHint("Opens discovery, relay, and notification settings.")
                }
            }
        }
    }

    /// The Devices tab — paired Syncthing peers and the add-device entry point.
    /// "Add" lives in the toolbar (the idiomatic spot), not in a section header.
    private var devicesTab: some View {
        NavigationStack {
            List {
                devicesSection
            }
            .navigationTitle(L10n.tr("Devices"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showAddDevice = true
                    } label: {
                        Image(systemName: "plus")
                    }
                    .disabled(!syncthingManager.isRunning)
                    .accessibilityLabel("Add Device")
                    .accessibilityHint("Opens the form to add a Syncthing device.")
                }
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
        default:
            break
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

    // MARK: - Dashboard Section

    private var dashboardSection: some View {
        Section {
            if showRelayUpsellCard {
                relayUpsellCard
            }
            if showNotificationPrimerCard {
                notificationPrimerCard
            }
            if let staleWarning = syncthingManager.staleSyncWarning {
                Label(staleWarning, systemImage: "clock.badge.exclamationmark")
                    .font(.caption)
                    .foregroundStyle(Color.statusAttention)
                    .accessibilityElement(children: .combine)
            }
            if let backgroundOutcome = syncthingManager.lastBackgroundSyncOutcome,
               backgroundOutcome.result.shouldSurfaceIssue {
                Label(L10n.fmt("Background sync: %@", backgroundOutcome.result.issueTitle), systemImage: "moon.zzz")
                    .font(.caption)
                    .foregroundStyle(Color.statusAttention)
                    .accessibilityElement(children: .combine)
            }

            if subscriptionManager.isRelaySubscribed {
                if subscriptionManager.needsRelayReactivation {
                    // A1 — paid-but-never-activated relay subscription (the "dead
                    // sub" cohort: subscribed, never woken, past the grace period).
                    // A self-test does NOT clear this — only a real server wake-up
                    // does (it keys on the real trigger timestamp).
                    relayNavRow(
                        title: L10n.tr("Finish activating Cloud Relay"),
                        subtitle: L10n.tr("You’re subscribed, but your server has never woken this iPhone. One step finishes setup."),
                        status: .attention,
                        systemImage: "antenna.radiowaves.left.and.right.slash"
                    )
                    .accessibilityHint(L10n.tr("Opens Cloud Relay setup."))
                } else if subscriptionManager.relayDeliveryConfirmed {
                    // "active" means a REAL wake-up has actually reached this
                    // device — not merely "provisioned + reachable" (K1). Same
                    // badge as the Relay tab's steady state, so the two screens
                    // can never disagree about what "active" looks like.
                    StatusBadge(.synced, text: L10n.tr("Cloud Relay active"))
                } else if subscriptionManager.lastRelayTriggerReceivedAt != nil {
                    // Delivered before, but no recent wake-up — setup IS done; the
                    // helper just went quiet. Don't tell them to "set up" again.
                    relayNavRow(
                        title: L10n.tr("Cloud Relay went quiet"),
                        subtitle: L10n.tr("No wake-up in a while. If nothing changed in your vault, that can be normal — otherwise check that your server is on."),
                        status: .attention,
                        systemImage: "antenna.radiowaves.left.and.right"
                    )
                } else {
                    // Subscribed but never delivered yet (within grace, so not the
                    // reactivation card) — finish the one missing setup step.
                    relayNavRow(
                        title: L10n.tr("One step left to activate"),
                        subtitle: L10n.tr("Set up the server helper"),
                        status: .attention,
                        systemImage: "antenna.radiowaves.left.and.right"
                    )
                }
            } else if !syncthingManager.folders.isEmpty, !showRelayUpsellCard {
                relayNavRow(
                    title: L10n.tr("Get instant updates"),
                    subtitle: L10n.tr("Turn on Cloud Relay"),
                    status: nil,
                    systemImage: "antenna.radiowaves.left.and.right"
                )
            }

            if syncthingManager.isRunning {
                let connected = syncthingManager.devices.filter(\.connected).count
                let total = syncthingManager.devices.count
                // While every disconnected device is still inside its reconnect
                // grace window, "0 of N connected" is normal warm-up, not a
                // problem — show a calm connecting state instead of a warning
                // color. The orange treatment is reserved for devices that
                // stayed disconnected beyond the grace period.
                let reconnectingDevices = syncthingManager.devices.filter { !$0.connected && !$0.paused }
                let isWarmingUp = connected == 0 && total > 0
                    && !reconnectingDevices.isEmpty
                    && reconnectingDevices.allSatisfy {
                        syncthingManager.isWithinReconnectGrace(deviceID: $0.deviceID)
                    }
                HStack {
                    if isWarmingUp {
                        ProgressView()
                            .controlSize(.small)
                            .tint(Color.statusStarting)
                            .accessibilityHidden(true)
                    } else {
                        Image(systemName: "network")
                            .foregroundStyle(connected > 0 ? Color.statusSuccess : Color.statusInactive)
                            .accessibilityHidden(true)
                    }
                    if total == 0 {
                        Text("No devices configured")
                            .foregroundStyle(.secondary)
                    } else if isWarmingUp {
                        Text(L10n.tr("Connecting to devices…"))
                            .foregroundStyle(.secondary)
                    } else {
                        Text(L10n.fmt("%d of %d devices connected", connected, total))
                            .foregroundStyle(connected > 0 ? Color.statusSuccess : Color.statusAttention)
                    }
                }
                .font(.subheadline)
                .accessibilityElement(children: .combine)
            }

            if let error = currentSyncError {
                ActionCard(
                    status: .error,
                    title: error.title,
                    message: joinedErrorMessage(error.message, error.remediation),
                    secondary: troubleshootingSecondary(for: error)
                )
            }

            ForEach(foldersWithErrors, id: \.self) { folderID in
                let folder = syncthingManager.folders.first { $0.id == folderID }
                let folderError = syncthingManager.folderUserError(folderID: folderID)
                ActionCard(
                    status: .attention,
                    title: folder?.label ?? folderID,
                    message: joinedErrorMessage(
                        folderError?.message ?? L10n.tr("Folder is currently in an error state."),
                        folderError?.remediation ?? ""
                    ),
                    secondary: folderError.flatMap { troubleshootingSecondary(for: $0) }
                )
            }
        }
    }

    /// Message + remediation as one ActionCard body, skipping empty parts.
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

    /// One dashboard row that routes into the Relay tab — shared by the upsell,
    /// "finish setup", recovery, and reactivation states so all four read as the
    /// same kind of row instead of four hand-built HStacks.
    private func relayNavRow(
        title: String,
        subtitle: String,
        status: SyncStatus?,
        systemImage: String
    ) -> some View {
        Button {
            selectedTab = .relay
        } label: {
            StatusRow(title, subtitle: subtitle, status: status, systemImage: systemImage) {
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.tertiary)
                    .accessibilityHidden(true)
            }
        }
        .tint(.primary)
    }

    /// The one-time Cloud Relay offer, shown as a dismissable dashboard card the
    /// first time a real sync completes (the "aha moment"). Replaces the old
    /// behavior of silently switching the selected tab, which yanked users out
    /// of whatever they were doing mid-celebration.
    private var relayUpsellCard: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            HStack(spacing: VaultSpacing.s) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .foregroundStyle(accent)
                    .accessibilityHidden(true)
                Text(L10n.tr("Get instant updates"))
                    .font(.headline)
            }
            Text(L10n.tr("Your first sync is done. Cloud Relay wakes this iPhone the moment your notes change — even while the app is closed."))
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: VaultSpacing.m) {
                Button(L10n.tr("View Cloud Relay")) {
                    dismissRelayUpsell(openRelay: true)
                }
                .buttonStyle(.borderedProminent)
                Button(L10n.tr("Not now")) {
                    dismissRelayUpsell(openRelay: false)
                }
                .buttonStyle(.bordered)
            }
            .padding(.top, VaultSpacing.xxs)
        }
        .padding(.vertical, VaultSpacing.xs)
        // `.contain`, not `.combine`: the card holds two buttons that must stay
        // independently focusable for VoiceOver.
        .accessibilityElement(children: .contain)
    }

    /// The primed notification ask (#69): explains WHY notifications help
    /// (conflict alerts) before any system prompt appears — replacing the
    /// bare permission dialog that used to fire over the empty main screen
    /// the moment onboarding completed. Only the explicit button triggers
    /// the system prompt.
    private var notificationPrimerCard: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            HStack(spacing: VaultSpacing.s) {
                Image(systemName: "bell.badge")
                    .foregroundStyle(accent)
                    .accessibilityHidden(true)
                Text(L10n.tr("Get notified about conflicts"))
                    .font(.headline)
            }
            Text(L10n.tr("If a note changes on two devices at the same time, VaultSync can alert you so you can choose which version to keep."))
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: VaultSpacing.m) {
                Button(L10n.tr("Enable Notifications")) {
                    dismissNotificationPrimer(enable: true)
                }
                .buttonStyle(.borderedProminent)
                Button(L10n.tr("Not now")) {
                    dismissNotificationPrimer(enable: false)
                }
                .buttonStyle(.bordered)
            }
            .padding(.top, VaultSpacing.xxs)
        }
        .padding(.vertical, VaultSpacing.xs)
        // `.contain`, not `.combine`: two independently focusable buttons.
        .accessibilityElement(children: .contain)
    }

    private var isSyncing: Bool {
        syncthingManager.folderStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }
    }

    private var isReconnecting: Bool {
        !syncthingManager.reconnectingRequiredDeviceIDs.isEmpty
    }

    /// True iff the reconnecting visuals (ProgressView spinner + "Connecting
    /// to…" caption) should actually be shown. Higher-priority states (errors,
    /// "Starting…", folder errors) suppress the indicator instead of competing
    /// with it for visual hierarchy. The header title itself stays positive —
    /// a grace-window reconnect is normal warm-up, not a problem state.
    private var shouldShowReconnectingUI: Bool {
        currentSyncError == nil
            && syncthingManager.isRunning
            && foldersWithErrors.isEmpty
            && isReconnecting
    }

    /// Canonical header state (#66, decision 012): glyph, color, and title
    /// derive from ONE source of truth — the same issue list the "Sync Issues"
    /// section renders — so the header can never claim "All Synced" while an
    /// issue row is visible below it. The cascade itself lives in the pure,
    /// unit-tested `SyncHeaderModel`.
    ///
    /// A reconnect inside its grace window deliberately does NOT change the
    /// status: being briefly disconnected after a cold start is Syncthing's
    /// normal warm-up, so the header keeps its positive state and only the
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
            hasDetectedVaults: !vaultManager.detectedVaults.isEmpty
        ))
    }

    /// Secondary line for the status header — the reconnecting progress or the
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

    // MARK: - Obsidian Status Section

    @ViewBuilder
    private var obsidianStatusSection: some View {
        if !vaultManager.isAccessible {
            Section {
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
                    .font(.caption)
            }

            Text("In the picker, choose \"On My iPhone\" → \"Obsidian\", then tap Open.")
                .font(.caption)
                .foregroundStyle(.tertiary)

            DisclosureGroup("Can't find the Obsidian folder?") {
                Text("Install Obsidian from the App Store and open it once. The folder appears after Obsidian creates it.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.top, VaultSpacing.xs)
            }
            .font(.caption)
            .foregroundStyle(.secondary)
            // The sole path to first-install help — a caption-sized label is
            // ~18pt tall, far under the 44pt minimum tap target (#69).
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .padding(.top, VaultSpacing.xs)
    }

    // MARK: - Pending Shares Section

    @ViewBuilder
    private var pendingSharesSection: some View {
        let pendingFolders = syncthingManager.actionablePendingFolders
        let ignoredFolders = syncthingManager.ignoredPendingFolders
        if !pendingFolders.isEmpty || !ignoredFolders.isEmpty {
            Section("Pending Shares") {
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
    }

    // MARK: - Sync Issues Section

    @ViewBuilder
    private var syncIssuesSection: some View {
        let issues = syncthingManager.unresolvedIssues
        if !issues.isEmpty {
            Section("Sync Issues") {
                SyncIssuesView(
                    issues: issues,
                    syncthingManager: syncthingManager,
                    onRescanFailedFolders: rescanFailedVaults,
                    onOpenAddDevice: { showAddDevice = true },
                    onAcceptFirstPendingShare: acceptFirstPendingShareFromIssues,
                    onRescanAllVaults: rescanAllVaults
                )
            }
        }
    }

    private func acceptFirstPendingShareFromIssues() {
        guard let first = syncthingManager.actionablePendingFolders.first else { return }
        shareAccept.accept(first, source: .manual)
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

    // MARK: - Unreachable Vaults

    /// Surfaces folders the launch-time path reconcile could not heal (a stale
    /// app-container path, issue #25) with a guided way out — reconnect to the
    /// Obsidian directory if the folder maps to it, or remove it outright.
    @ViewBuilder
    private var unreachableVaultsSection: some View {
        let unreachable = syncthingManager.unreachableFolders
        if !unreachable.isEmpty {
            Section {
                ForEach(unreachable) { folder in
                    VStack(alignment: .leading, spacing: 8) {
                        // Texts read as ONE VoiceOver element (name + what is
                        // wrong together); the buttons stay independently
                        // focusable below (#71).
                        VStack(alignment: .leading, spacing: 8) {
                            Label {
                                Text(folder.label).font(.body)
                            } icon: {
                                Image(systemName: "exclamationmark.triangle.fill")
                                    .foregroundStyle(Color.statusAttention)
                            }
                            Text("This vault points to storage that no longer exists on this iPhone, so it can no longer sync.")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        .accessibilityElement(children: .combine)
                        HStack(spacing: 12) {
                            if folder.hasObsidianMapping {
                                Button(L10n.tr("Reconnect to Obsidian")) {
                                    showObsidianPicker = true
                                }
                                .buttonStyle(.bordered)
                            }
                            Button(role: .destructive) {
                                vaultPendingRemoval = VaultRemovalTarget(id: folder.id, label: folder.label)
                            } label: {
                                Text(L10n.tr("Remove This Vault"))
                            }
                            .buttonStyle(.bordered)
                        }
                    }
                    .padding(.vertical, 4)
                }
            } header: {
                Text(L10n.tr("Needs Attention"))
            } footer: {
                Text(L10n.tr("Removing a vault only stops syncing it on this iPhone. The notes on your other devices are not affected."))
            }
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
        }
    }

    // MARK: - Vaults Section

    private var vaultsSection: some View {
        Section("Obsidian Vaults") {
            if syncthingManager.folders.isEmpty && unsyncedVaultNames.isEmpty {
                vaultsEmptyState
            } else {
                ForEach(vaultRows) { item in
                    NavigationLink {
                        vaultDetailView(item)
                    } label: {
                        vaultRow(item)
                    }
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
        StatusRow(
            name,
            subtitle: L10n.tr("Not syncing yet — share this vault from your computer to start."),
            systemImage: "folder",
            glyphTint: .statusInactive
        )
    }

    /// A designed first-run state instead of a degenerate caption row — this is
    /// the screen a brand-new user stares at the longest. The "connect" case
    /// carries no button of its own: the ActionCard above already owns that CTA.
    @ViewBuilder
    private var vaultsEmptyState: some View {
        if !vaultManager.isAccessible {
            ContentUnavailableView {
                Label(L10n.tr("Connect to Obsidian first"), systemImage: "folder.badge.gearshape")
            } description: {
                Text("VaultSync needs one-time access to your Obsidian folder before it can accept shares.")
            }
        } else if vaultManager.detectedVaults.isEmpty {
            ContentUnavailableView {
                Label(L10n.tr("No vaults found"), systemImage: "folder.badge.questionmark")
            } description: {
                Text("Create a vault in Obsidian first. VaultSync will detect it automatically.")
            }
        } else {
            ContentUnavailableView {
                Label(L10n.tr("No folders syncing yet"), systemImage: "arrow.triangle.2.circlepath")
            } description: {
                Text("Share a folder from your desktop Syncthing — it will be accepted automatically.")
            }
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

    /// Conflicts attributed to one vault: inside the vault's subdirectory for a
    /// directory-sync row, or all of the folder's conflicts for a 1:1 row.
    private func conflicts(for item: VaultRowItem) -> [SyncthingManager.ConflictInfo] {
        let all = syncthingManager.conflictFiles[item.folder.id] ?? []
        guard let vault = item.relativePrefix else { return all }
        return all.filter { $0.belongs(toVault: vault) }
    }

    /// Vaults are the app's hero object — give them the same StatusRow treatment
    /// as devices (full-size status glyph, headline title) instead of the old
    /// plain-text row with a tiny trailing caption icon.
    private func vaultRow(_ item: VaultRowItem) -> some View {
        let status = syncthingManager.folderStatuses[item.folder.id]
        // Distinct conflicted files, not copies — same semantics as the
        // home-screen issue banner (SyncthingManager.unresolvedConflictCount).
        let conflictCount = Set(conflicts(for: item).map(\.originalPath)).count
        let syncStatus = folderSyncStatus(status?.state ?? "unknown")

        var subtitle: String?
        if let status {
            subtitle = localizedState(status.state, folderID: item.folder.id)
            if status.completionPct < 100, status.completionPct > 0 {
                subtitle! += " " + L10n.fmt("(%d%%)", Int(status.completionPct))
            }
        }

        return StatusRow(
            item.name,
            subtitle: subtitle,
            status: syncStatus,
            systemImage: syncStatus == nil ? "questionmark.circle" : nil,
            glyphTint: syncStatus == nil ? Color.statusInactive : nil
        ) {
            if conflictCount > 0 {
                StatusTag(text: "\(conflictCount)", filled: true)
                    .accessibilityLabel(L10n.fmt("%d conflicts", conflictCount))
            }
        }
    }

    /// Map a folder's raw engine state onto the canonical `SyncStatus` registry so
    /// the folder row's glyph + color stay identical to the rest of the app (one
    /// source of truth). Unknown states stay neutral rather than being forced to a
    /// misleading "attention".
    private func folderSyncStatus(_ state: String) -> SyncStatus? {
        switch state {
        case "idle": return .synced
        case "scanning", "syncing": return .syncing
        case "error": return .error
        default: return nil
        }
    }

    private func stateIcon(_ state: String) -> String {
        folderSyncStatus(state)?.symbolName ?? "questionmark.circle"
    }

    private func stateColor(_ state: String) -> Color {
        folderSyncStatus(state)?.tint ?? .statusInactive
    }

    // MARK: - Vault Detail

    private func vaultDetailView(_ item: VaultRowItem) -> some View {
        let folder = item.folder
        let status = syncthingManager.folderStatuses[folder.id]
        let conflicts = self.conflicts(for: item)
        return List {
            Section {
                DetailRow(title: L10n.tr("Name"), value: item.name)
                DetailRow(title: L10n.tr("Path"), value: item.vaultSubpath ?? folder.path, monospacedValue: true)
            } header: {
                Text("Vault")
            } footer: {
                if item.relativePrefix != nil {
                    Text("Synced as part of your Obsidian directory. Sync filters and devices apply to the whole directory.")
                }
            }

            Section("Sync Status") {
                LabeledContent("State") {
                    HStack(spacing: 6) {
                        Image(systemName: stateIcon(status?.state ?? "unknown"))
                            .foregroundStyle(stateColor(status?.state ?? "unknown"))
                            .font(.caption2)
                            .accessibilityHidden(true)
                        Text(localizedState(status?.state ?? "unknown", folderID: folder.id))
                    }
                    .accessibilityElement(children: .combine)
                }
                if let status {
                    LabeledContent("Completion", value: "\(Int(status.completionPct))%")
                    LabeledContent("Local Files", value: "\(status.localFiles)")
                    LabeledContent("Global Files", value: "\(status.globalFiles)")
                    if status.state == "error",
                       let folderError = syncthingManager.folderUserError(folderID: folder.id) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(folderError.message)
                                .font(.caption)
                            Text(folderError.remediation)
                                .font(.caption2)
                                .foregroundStyle(.secondary)
                            if let url = troubleshootingURL(for: folderError) {
                                ExternalLinkButton(titleKey: "Learn how to fix", url: url)
                                    .font(.caption2)
                            }
                        }
                    }
                }
            }

            if !conflicts.isEmpty {
                Section {
                    NavigationLink {
                        ConflictListView(
                            folderID: folder.id,
                            pathPrefix: item.relativePrefix,
                            syncthingManager: syncthingManager
                        )
                    } label: {
                        HStack {
                            Label("Conflicts", systemImage: "exclamationmark.triangle")
                                .foregroundStyle(Color.statusAttention)
                            Spacer()
                            Text("\(Set(conflicts.map(\.originalPath)).count)")
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }

            Section {
                NavigationLink {
                    IgnorePatternsView(
                        folderID: folder.id,
                        syncthingManager: syncthingManager
                    )
                } label: {
                    Label(L10n.tr("Sync Filters"), systemImage: "line.3.horizontal.decrease.circle")
                }
                .accessibilityHint(L10n.tr("Choose what gets synced to this iPhone"))
            }

            Section("Shared With") {
                ForEach(syncthingManager.devices) { device in
                    let isShared = folder.deviceIDs.contains(device.deviceID)
                    Button {
                        toggleDeviceSharing(folderID: folder.id, deviceID: device.deviceID, isShared: isShared)
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(device.name.isEmpty ? L10n.tr("Unnamed") : device.name)
                                    .font(.body)
                                Text(device.deviceID)
                                    .font(.vaultMono(.caption2))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                            Spacer()
                            Label(isShared ? L10n.tr("Shared") : L10n.tr("Not Shared"), systemImage: isShared ? "checkmark.circle.fill" : "circle")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(isShared ? Color.vaultAccent : Color.statusInactive)
                                .accessibilityHidden(true)
                        }
                    }
                    .tint(.primary)
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel(device.name.isEmpty ? L10n.tr("Unnamed device") : device.name)
                    .accessibilityValue(isShared ? L10n.tr("Shared") : L10n.tr("Not shared"))
                    .accessibilityHint(isShared ? "Double-tap to stop sharing this vault with this device." : "Double-tap to share this vault with this device.")
                }
                if syncthingManager.devices.isEmpty {
                    Text("No devices configured")
                        .foregroundStyle(.secondary)
                }
            }

            Section {
                // Honest progress: the busy state reflects the folder's REAL
                // scan state from the engine, not a fixed timer.
                let isScanning = status?.state == "scanning"
                Button {
                    if let err = syncthingManager.rescanFolder(id: folder.id) {
                        alertMessage = mappedError(err, fallbackTitle: L10n.tr("Rescan Failed")).userVisibleDescription
                        showAlert = true
                    }
                } label: {
                    HStack {
                        Text(isScanning ? "Rescanning…" : "Rescan Vault")
                        Spacer()
                        if isScanning {
                            ProgressView()
                                .controlSize(.small)
                        }
                    }
                }
                .disabled(isScanning)
            }

            // A 1:1 sync folder maps to exactly one vault, so removing it is
            // unambiguous. For an expanded directory row (relativePrefix != nil)
            // a single folder backs many vaults, so per-vault removal is omitted
            // to avoid silently dropping the whole directory.
            if item.relativePrefix == nil {
                Section {
                    Button(role: .destructive) {
                        vaultPendingRemoval = VaultRemovalTarget(
                            id: folder.id,
                            label: item.name
                        )
                    } label: {
                        Label(L10n.tr("Remove Vault"), systemImage: "trash")
                    }
                } footer: {
                    Text("Stops syncing this vault on this iPhone. The notes on your other devices are not affected.")
                }
            }
        }
        .navigationTitle(item.name)
        .navigationBarTitleDisplayMode(.inline)
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

    // MARK: - Devices Section

    private var devicesSection: some View {
        Section {
            if syncthingManager.devices.isEmpty {
                ContentUnavailableView {
                    Label(L10n.tr("No devices configured"), systemImage: "laptopcomputer.and.iphone")
                } description: {
                    Text("Add a device using its Syncthing Device ID. Find it in the Syncthing web UI under Actions > Show ID.")
                } actions: {
                    Button(L10n.tr("Add Device")) {
                        showAddDevice = true
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(!syncthingManager.isRunning)
                }
            } else {
                ForEach(syncthingManager.devices) { device in
                    NavigationLink {
                        DeviceDetailView(
                            device: device,
                            syncthingManager: syncthingManager
                        )
                    } label: {
                        deviceRow(device)
                    }
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
        let isConnecting = !device.connected && !device.paused
            && syncthingManager.isWithinReconnectGrace(deviceID: device.deviceID)

        let status: SyncStatus
        let label: String
        let glyph: String?
        if device.connected {
            status = .synced
            label = L10n.tr("Connected")
            glyph = "checkmark.circle.fill"
        } else if device.paused {
            status = .paused
            label = L10n.tr("Paused")
            glyph = "pause.circle.fill"
        } else if isConnecting {
            status = .starting
            label = L10n.tr("Connecting…")
            glyph = nil
        } else {
            status = .paused
            label = L10n.tr("Offline")
            glyph = "moon.zzz.fill"
        }

        return StatusRow(
            device.name.isEmpty ? L10n.tr("Unnamed") : device.name,
            status: status,
            systemImage: glyph,
            busy: isConnecting
        ) {
            Text(label)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
        }
    }

    // MARK: - Error Helpers

    private func mappedError(_ error: String, fallbackTitle: String = L10n.tr("Sync Error")) -> SyncUserError {
        SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle)
    }

    private func troubleshootingURL(for error: SyncUserError) -> URL? {
        SyncUserError.troubleshootingURL(for: error)
    }

    /// Folder-aware variant (#94): an idle folder that has never recorded a
    /// successful sync must not read "Up to Date" — before the first exchange
    /// the honest label is that it is still waiting for one.
    private func localizedState(_ state: String, folderID: String) -> String {
        if state.lowercased() == "idle",
           syncthingManager.lastSyncTimeByFolder[folderID] == nil {
            return L10n.tr("Waiting for first sync")
        }
        return localizedState(state)
    }

    private func localizedState(_ state: String) -> String {
        switch state.lowercased() {
        case "idle":
            // "Up to Date", not the engine's "Idle": next to the header's
            // "All Synced" a literal "Idle" read as its contradiction (#71).
            return L10n.tr("Up to Date")
        case "scanning":
            return L10n.tr("Scanning")
        case "syncing":
            return L10n.tr("Syncing")
        case "error":
            return L10n.tr("Error")
        default:
            return L10n.tr("Unknown")
        }
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
        )
    )
}

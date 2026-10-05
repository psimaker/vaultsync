import SwiftUI

struct OnboardingView: View {
    @Binding var hasCompletedOnboarding: Bool
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    var shareAccept: ShareAcceptCoordinator
    var hubLinkRouter: HubLinkRouter

    // Live setup actions — each step launches the real task instead of describing it.
    @State private var showObsidianPicker = false
    @State private var showAddDevice = false
    /// The Add Hub sheet (#174), with the pairing link that opened it, if any.
    @State private var addHubRequest: AddHubRequest?
    @State private var alertMessage: String?
    @State private var showAlert = false
    /// Non-error notice (e.g. "you selected a single vault") — its own alert so
    /// it is not presented under the "Error" title.
    @State private var infoMessage: String?
    @State private var showInfoAlert = false
    /// Set when AddDeviceSheet reports a successful add; presented from the
    /// sheet's onDismiss (#95).
    @State private var showDeviceAddedHint = false
    @Environment(\.openURL) private var openURL

    private var obsidianConnected: Bool { vaultManager.isAccessible }
    private var deviceAdded: Bool { !syncthingManager.devices.isEmpty }
    private var vaultSyncing: Bool { !syncthingManager.folders.isEmpty }
    private var allStepsComplete: Bool { obsidianConnected && deviceAdded && vaultSyncing }
    /// The first unfinished step — its action is the screen's main action.
    private var nextStep: Int? {
        [obsidianConnected, deviceAdded, vaultSyncing].firstIndex(of: false).map { $0 + 1 }
    }

    var body: some View {
        NavigationStack {
            VaultPage {
                setupScreen
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                bottomBar
            }
            .toolbar(.hidden, for: .navigationBar)
            .sheet(isPresented: $showObsidianPicker) {
                FolderPicker(initialDirectoryURL: vaultManager.obsidianDirectoryURL, onCancel: {
                    showObsidianPicker = false
                }) { url in
                    showObsidianPicker = false
                    Task {
                        // Same sequence as the home screen's reconnect flow
                        // (#53/#92): granting access produces no pendingFolders
                        // change event, so an offer that arrived before the
                        // grant would sit untouched. The accept pass runs only
                        // after the reconcile settled paths (#56, decision 008).
                        if let err = await ObsidianReconnectFlow.run(
                            grantAccess: { vaultManager.grantAccess(url: url) },
                            onGrantSucceeded: {
                                shareAccept.clearRecordedFailures()
                                if let advisory = vaultManager.selectionAdvisory {
                                    infoMessage = advisory
                                    showInfoAlert = true
                                    vaultManager.clearSelectionAdvisory()
                                }
                            },
                            reconcile: {
                                await syncthingManager.reconcileFolderPaths(
                                    obsidianRoot: vaultManager.obsidianBasePath
                                ).value
                            },
                            retryPendingShares: {
                                shareAccept.runAutomaticPass()
                            }
                        ) {
                            present(error: err, fallbackTitle: L10n.tr("Obsidian Folder Connection Failed"))
                        }
                    }
                }
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
                // Onboarding shows the Hub's share on its own step 3, so
                // the hand-off has no "Review in Sync".
                AddHubSheet(
                    syncthingManager: syncthingManager,
                    vaultManager: vaultManager,
                    shareAccept: shareAccept,
                    link: request.link
                )
            }
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
        }
        .onAppear {
            // The unit-test host must never manage the process-global engine
            // lifecycle — see TestHost.
            guard !TestHost.isActive else { return }
            #if DEBUG
            // A UI-audit fixture run seeds manager state directly and never
            // manages the engine — see UIAuditFixture.
            if UIAuditFixture.active == UIAuditFixture.designPreview {
                DesignPreviewFixture.seed(syncthingManager: syncthingManager, vaultManager: vaultManager)
            }
            guard !UIAuditFixture.isActive else { return }
            #endif
            // Third consumer of the #60 state (#61): a background handler can
            // have started the engine before onboarding ever renders — a
            // direct start() here raced the scene handler and could flash
            // the Go floor's "already running" as an error mid-onboarding.
            EngineAttach.onForeground(
                syncthingManager: syncthingManager,
                vaultManager: vaultManager
            )
        }
        .onChange(of: syncthingManager.pendingFolders, initial: true) { _, _ in
            // The accept pass must not depend on ContentView being mounted
            // (#92): the same standing triggers ContentView carries, driving
            // the same coordinator with the identical gates (decision 015).
            shareAccept.runAutomaticPass()
        }
        .onChange(of: syncthingManager.pathSettlement.settled) { _, settled in
            if settled {
                shareAccept.runAutomaticPass()
            }
        }
        .onChange(of: shareAccept.alertMessage) { _, message in
            guard let message else { return }
            shareAccept.alertMessage = nil
            alertMessage = message
            showAlert = true
        }
        // A pairing link from the Camera app (#174) opens the Add Hub sheet
        // here too — once no other sheet or alert is up.
        .onChange(of: hubLinkRouter.pending, initial: true) { _, _ in
            presentPendingHubLink()
        }
        .onChange(of: canPresentHubLink) { _, ready in
            if ready { presentPendingHubLink() }
        }
    }

    private var canPresentHubLink: Bool {
        !showObsidianPicker && !showAddDevice && addHubRequest == nil && !showAlert && !showInfoAlert
    }

    private func presentPendingHubLink() {
        guard hubLinkRouter.pending != nil, canPresentHubLink else { return }
        switch hubLinkRouter.take() {
        case .pair(let link):
            addHubRequest = AddHubRequest(link: link)
        case .unusable(let problem):
            alertMessage = HubPairingCopy.scanProblem(problem)
            showAlert = true
        case nil:
            break
        }
    }

    // MARK: - Setup (live, actionable)

    /// The whole first run on one screen (#187 canvas): the three steps,
    /// each launching the real task instead of describing it, turning green
    /// as they complete.
    @ViewBuilder
    private var setupScreen: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.s) {
            Image(systemName: "arrow.triangle.2.circlepath")
                .font(.title2.weight(.semibold))
                .foregroundStyle(Color.vaultAccent)
                .frame(width: 56, height: 56)
                .background(Color.vaultAccentFill, in: Circle())
                .accessibilityHidden(true)
                .padding(.bottom, VaultSpacing.xs)

            Text(L10n.tr("Let’s get your vault synced"))
                .font(.largeTitle.weight(.bold))
                .foregroundStyle(Color.vaultLabel)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)

            Text(L10n.tr("Three steps. They turn green as you go; you can finish later from the home screen."))
                .font(.body)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.top, VaultSpacing.xl)
        .padding(.bottom, VaultSpacing.s)

        if let engineError {
            ActionCard(
                status: .error,
                title: engineError.title,
                message: engineError.message
            )
        }

        stepCard(
            number: 1,
            isComplete: obsidianConnected,
            title: L10n.tr("Connect your Obsidian folder"),
            description: L10n.tr("Give VaultSync one-time access to your local Obsidian folder so it can sync your notes."),
            actionTitle: L10n.tr("Connect Obsidian Folder"),
            action: { showObsidianPicker = true }
        )

        stepCard(
            number: 2,
            isComplete: deviceAdded,
            title: L10n.tr("Add your Hub or computer"),
            description: L10n.tr("Type the code your Hub printed, or scan its QR code. Using Syncthing on a computer instead? Add it by its Device ID."),
            actionTitle: L10n.tr("Add Hub"),
            action: { addHubRequest = AddHubRequest() },
            secondaryActionTitle: L10n.tr("Add Device"),
            secondaryAction: { showAddDevice = true }
        )

        stepCard(
            number: 3,
            isComplete: vaultSyncing,
            title: L10n.tr("Sync your first vault"),
            description: L10n.tr("Pick a vault when you add your Hub, or share one from Syncthing on your computer. VaultSync accepts it automatically — this turns green the moment it arrives."),
            actionTitle: nil,
            action: nil,
            // The only step that happens on ANOTHER machine — without a
            // pointer to the desktop-side steps it is a dead end (#69).
            linkTitleKey: "How to share from your computer",
            linkURL: DocURL.desktopShareHelp
        )

        if !vaultSyncing {
            ForEach(syncthingManager.actionablePendingFolders) { folder in
                offerStatusRow(folder)
            }
        }

        VaultNotice(
            systemImage: "antenna.radiowaves.left.and.right",
            text: L10n.tr("Optional: turn on Cloud Relay later for instant updates — you’ll find it on the Relay tab."),
            tone: .neutral
        )
    }

    private func stepCard(
        number: Int,
        isComplete: Bool,
        title: String,
        description: String,
        actionTitle: String?,
        action: (() -> Void)?,
        secondaryActionTitle: String? = nil,
        secondaryAction: (() -> Void)? = nil,
        linkTitleKey: LocalizedStringKey? = nil,
        linkURL: URL? = nil
    ) -> some View {
        HStack(alignment: .top, spacing: 14) {
            stepIndicator(number: number, isComplete: isComplete)

            VStack(alignment: .leading, spacing: 6) {
                // Group only the text into one VoiceOver element (so title +
                // description are read together with the completion status) while
                // leaving the action as its own focusable, activatable element —
                // combining the whole card would swallow it.
                VStack(alignment: .leading, spacing: 6) {
                    Text(title)
                        .font(.body.weight(.semibold))
                        .foregroundStyle(isComplete ? Color.vaultSecondaryLabel : Color.vaultLabel)
                    // A finished step needs no explanation any more: the
                    // check and the title say it, and the open steps move up
                    // (#187 review).
                    if !isComplete {
                        Text(description)
                            .font(.subheadline)
                            .foregroundStyle(Color.vaultSecondaryLabel)
                    }
                }
                // Without this the title truncates ("Deinen Obsidian-O…")
                // instead of wrapping at accessibility Dynamic Type (#67).
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityElement(children: .combine)
                .accessibilityValue(isComplete ? L10n.tr("Done") : "")

                if !isComplete, let linkTitleKey, let linkURL {
                    if number == nextStep {
                        // The step that happens on the other machine: its
                        // guide is the main action while it is next.
                        Button {
                            openURL(linkURL)
                        } label: {
                            HStack(spacing: VaultSpacing.xs) {
                                Text(linkTitleKey)
                                Image(systemName: "arrow.up.right")
                                    .imageScale(.small)
                                    .accessibilityHidden(true)
                            }
                        }
                        .buttonStyle(.vault(.primary, compact: true))
                        .padding(.top, VaultSpacing.xs)
                    } else {
                        ExternalLinkButton(titleKey: linkTitleKey, url: linkURL)
                            .font(.subheadline.weight(.semibold))
                    }
                }

                if !isComplete, let actionTitle, let action {
                    if number == nextStep {
                        // The next unfinished step carries the screen's main
                        // action; leaving setup is the quieter choice below.
                        VaultButtonRow {
                            Button(actionTitle, action: action)
                                .buttonStyle(.vault(.primary, compact: true))
                            if let secondaryActionTitle, let secondaryAction {
                                Button(secondaryActionTitle, action: secondaryAction)
                                    .buttonStyle(.vault(.neutral, compact: true))
                            }
                        }
                        .padding(.top, VaultSpacing.xs)
                    } else {
                        VaultFlowLayout(spacing: VaultSpacing.l) {
                            stepLink(actionTitle, action: action)
                            if let secondaryActionTitle, let secondaryAction {
                                stepLink(secondaryActionTitle, action: secondaryAction)
                            }
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard()
    }

    private func stepLink(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: VaultSpacing.xs) {
                Text(title)
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .accessibilityHidden(true)
            }
        }
        .buttonStyle(.vaultLink)
    }

    /// The canvas's step marker: the step number in an outlined circle, a
    /// filled green check once done. Decorative — the text group carries
    /// "Done" for VoiceOver.
    private func stepIndicator(number: Int, isComplete: Bool) -> some View {
        ZStack {
            if isComplete {
                Circle()
                    .fill(Color.statusSuccess)
                Image(systemName: "checkmark")
                    .font(.footnote.weight(.bold))
                    .foregroundStyle(Color.vaultOnAccent)
            } else {
                Circle()
                    .strokeBorder(Color.vaultHairline, lineWidth: 2)
                Text(number.formatted())
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Color.vaultSecondaryLabel)
            }
        }
        .frame(width: 28, height: 28)
        .accessibilityHidden(true)
    }

    /// Live status for a share offer that arrives during onboarding (#92):
    /// the accept pass runs right here with the home screen's gates, and this
    /// row keeps step 3 honest while it does — including the cases the pass
    /// deliberately parks (no Obsidian access yet; a decision only the full
    /// pending-shares UI can take, e.g. a non-empty target — #54).
    private func offerStatusRow(_ folder: SyncthingManager.PendingFolderInfo) -> some View {
        let name = folder.label.isEmpty ? folder.id : folder.label
        let needsAttention = shareAccept.pendingShareFailures[folder.id] != nil
            || !syncthingManager.autoAcceptEligiblePendingFolders.contains(where: { $0.id == folder.id })
        return HStack(alignment: .top, spacing: VaultSpacing.m) {
            if needsAttention {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Color.statusAttention)
                    .accessibilityHidden(true)
                Text(L10n.fmt("Offer “%@” needs your attention. Tap “Finish Setup Later” below to review it on the home screen.", name))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
            } else if !obsidianConnected {
                Image(systemName: "folder.badge.questionmark")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Color.statusAttention)
                    .accessibilityHidden(true)
                Text(L10n.fmt("Offer “%@” received — connect your Obsidian folder first.", name))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                ProgressView()
                    .controlSize(.small)
                    .accessibilityHidden(true)
                Text(L10n.fmt("Offer “%@” received — accepting…", name))
                    .font(.subheadline)
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .vaultCard(tone: needsAttention || !obsidianConnected ? .attention : .info)
        .accessibilityElement(children: .combine)
    }

    // MARK: - Error presentation

    private func present(error: String, fallbackTitle: String) {
        alertMessage = SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle).userVisibleDescription
        showAlert = true
    }

    /// Post-add guidance (#95) — same hint as the main app's device flow.
    private func presentDeviceAddedHintIfNeeded() {
        guard showDeviceAddedHint else { return }
        showDeviceAddedHint = false
        infoMessage = L10n.tr("Device added. Now confirm this iPhone in Syncthing on your computer — a confirmation prompt appears there. Then share your vault to start syncing.")
        showInfoAlert = true
    }

    /// Engine start failure was invisible during onboarding (#95): userError
    /// renders only on the ContentView dashboard, so a failed start left the
    /// "Sync your first vault" step waiting forever with no explanation.
    private var engineError: SyncUserError? {
        if let userError = syncthingManager.userError { return userError }
        return syncthingManager.error.map {
            SyncUserError.from(rawMessage: $0, fallbackTitle: L10n.tr("Could Not Start Sync"))
        }
    }

    // MARK: - Bottom bar

    /// The exit stays honest (#69): "Open VaultSync" only once every step is
    /// actually done — otherwise the button says what really happens (setup
    /// continues later from the home screen). Until then it is the quiet
    /// choice: the next step's own action is the main one (#187 review).
    private var bottomBar: some View {
        VStack(spacing: VaultSpacing.s) {
            Button {
                hasCompletedOnboarding = true
            } label: {
                Text(allStepsComplete
                    ? L10n.tr("onboarding.cta.openVaultSync")
                    : L10n.tr("onboarding.cta.finishLater"))
            }
            .buttonStyle(.vault(allStepsComplete ? .primary : .neutral))

            Text(L10n.tr("onboarding.welcome.benefit.noCloud"))
                .font(.footnote)
                .foregroundStyle(Color.vaultSecondaryLabel)
        }
        .padding(.horizontal, VaultSpacing.gutter)
        .padding(.top, VaultSpacing.m)
        .padding(.bottom, VaultSpacing.s)
        .frame(maxWidth: VaultMetrics.readableWidth)
        .frame(maxWidth: .infinity)
        .background(Color.vaultBackground.ignoresSafeArea(edges: .bottom))
    }
}

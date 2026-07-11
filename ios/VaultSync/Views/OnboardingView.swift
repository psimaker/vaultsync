import SwiftUI

struct OnboardingView: View {
    @Binding var hasCompletedOnboarding: Bool
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager
    var shareAccept: ShareAcceptCoordinator

    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    @State private var page = 0

    // Live setup actions — each step launches the real task instead of describing it.
    @State private var showObsidianPicker = false
    @State private var showAddDevice = false
    @State private var alertMessage: String?
    @State private var showAlert = false
    /// Non-error notice (e.g. "you selected a single vault") — its own alert so
    /// it is not presented under the "Error" title.
    @State private var infoMessage: String?
    @State private var showInfoAlert = false
    /// Set when AddDeviceSheet reports a successful add; presented from the
    /// sheet's onDismiss (#95).
    @State private var showDeviceAddedHint = false

    private let slate = Color.vaultSlate
    private let teal = Color.vaultTeal

    private var obsidianConnected: Bool { vaultManager.isAccessible }
    private var deviceAdded: Bool { !syncthingManager.devices.isEmpty }
    private var vaultSyncing: Bool { !syncthingManager.folders.isEmpty }
    private var allStepsComplete: Bool { obsidianConnected && deviceAdded && vaultSyncing }

    var body: some View {
        NavigationStack {
            ZStack {
                backgroundView

                VStack(spacing: 0) {
                    TabView(selection: $page) {
                        page(welcomeScreen).tag(0)
                        page(setupScreen).tag(1)
                    }
                    .tabViewStyle(.page(indexDisplayMode: .never))

                    if let engineError {
                        engineErrorBanner(engineError)
                            .padding(.horizontal, VaultSpacing.l)
                            .padding(.top, VaultSpacing.m)
                    }

                    bottomBar
                        .padding(.horizontal, VaultSpacing.l)
                        .padding(.top, VaultSpacing.m)
                        .padding(.bottom, VaultSpacing.s)
                        .background(.bar)
                }
                .toolbar(.hidden, for: .navigationBar)
            }
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
    }

    private func page<Content: View>(_ content: Content) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: VaultSpacing.xl) {
                content
            }
            .padding(.horizontal, VaultSpacing.l)
            .padding(.vertical, VaultSpacing.xl)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
    }

    // MARK: - Welcome

    private var welcomeScreen: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.l) {
            VStack(alignment: .leading, spacing: VaultSpacing.m) {
                topAccentBar

                Text(L10n.tr("onboarding.welcome.title"))
                    .font(titleFont)
                    .foregroundStyle(primaryHeadingColor)
                    .fixedSize(horizontal: false, vertical: true)

                Text(L10n.tr("onboarding.welcome.subtitle"))
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(VaultSpacing.xl)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.hero, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.hero, style: .continuous)))

            VStack(alignment: .leading, spacing: VaultSpacing.l) {
                benefitRow(icon: "lock.shield.fill", textKey: "onboarding.welcome.benefit.private")
                benefitRow(icon: "books.vertical.fill", textKey: "onboarding.welcome.benefit.obsidian")
                benefitRow(icon: "icloud.slash.fill", textKey: "onboarding.welcome.benefit.noCloud")
            }
            .padding(VaultSpacing.l)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
        }
    }

    // MARK: - Setup (live, actionable)

    private var setupScreen: some View {
        VStack(alignment: .leading, spacing: VaultSpacing.l) {
            VStack(alignment: .leading, spacing: VaultSpacing.s) {
                Text(L10n.tr("Let’s get your vault synced"))
                    .font(titleFont)
                    .foregroundStyle(primaryHeadingColor)

                Text(L10n.tr("Complete these steps right here. They light up green as you go — and you can always finish them later from the home screen."))
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(VaultSpacing.xl)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.hero, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.hero, style: .continuous)))

            stepCard(
                isComplete: obsidianConnected,
                icon: "folder.badge.plus",
                title: L10n.tr("Connect your Obsidian folder"),
                description: L10n.tr("Give VaultSync one-time access to your local Obsidian folder so it can sync your notes."),
                actionTitle: L10n.tr("Connect Obsidian Folder"),
                action: { showObsidianPicker = true }
            )

            stepCard(
                isComplete: deviceAdded,
                icon: "laptopcomputer.and.iphone",
                title: L10n.tr("Add your computer or server"),
                description: L10n.tr("Pair this iPhone with the Syncthing device that hosts your vault, by Device ID or QR code."),
                actionTitle: L10n.tr("Add Device"),
                action: { showAddDevice = true }
            )

            stepCard(
                isComplete: vaultSyncing,
                icon: "arrow.triangle.2.circlepath",
                title: L10n.tr("Sync your first vault"),
                description: L10n.tr("Share your Obsidian vault from Syncthing on your computer. VaultSync accepts it automatically — this turns green the moment it arrives."),
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

            HStack(alignment: .top, spacing: VaultSpacing.m) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(teal)
                    .accessibilityHidden(true)
                Text(L10n.tr("Optional: turn on Cloud Relay later for instant updates — you’ll find it on the Relay tab."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(VaultSpacing.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
            .accessibilityElement(children: .combine)
        }
    }

    private func stepCard(
        isComplete: Bool,
        icon: String,
        title: String,
        description: String,
        actionTitle: String?,
        action: (() -> Void)?,
        linkTitleKey: LocalizedStringKey? = nil,
        linkURL: URL? = nil
    ) -> some View {
        HStack(alignment: .top, spacing: VaultSpacing.m) {
            ZStack {
                Circle()
                    .fill(isComplete ? Color.statusSuccess : Color.vaultAccentFill)
                    .frame(width: 34, height: 34)
                Image(systemName: isComplete ? "checkmark" : icon)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(isComplete ? .white : teal)
            }
            .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 6) {
                // Group only the text into one VoiceOver element (so title +
                // description are read together with the completion status) while
                // leaving the action button as its own focusable, activatable
                // element — combining the whole card would swallow the button.
                VStack(alignment: .leading, spacing: 6) {
                    Text(title)
                        .font(.body.weight(.semibold))
                        .foregroundStyle(primaryHeadingColor)
                        // Without this the title truncates ("Deinen Obsidian-O…")
                        // instead of wrapping at accessibility Dynamic Type (#67).
                        .fixedSize(horizontal: false, vertical: true)
                    Text(description)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityValue(isComplete ? L10n.tr("Done") : "")

                if !isComplete, let linkTitleKey, let linkURL {
                    ExternalLinkButton(titleKey: linkTitleKey, url: linkURL)
                        .font(.subheadline)
                }

                if !isComplete, let actionTitle, let action {
                    Button(actionTitle, action: action)
                        .buttonStyle(.borderedProminent)
                        .controlSize(.regular)
                        .tint(teal)
                        .padding(.top, VaultSpacing.xs)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
        .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
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
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else if !obsidianConnected {
                Image(systemName: "folder.badge.questionmark")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Color.statusAttention)
                    .accessibilityHidden(true)
                Text(L10n.fmt("Offer “%@” received — connect your Obsidian folder first.", name))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                ProgressView()
                    .controlSize(.small)
                    .accessibilityHidden(true)
                Text(L10n.fmt("Offer “%@” received — accepting…", name))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(VaultSpacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
        .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
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

    private func engineErrorBanner(_ error: SyncUserError) -> some View {
        HStack(alignment: .top, spacing: VaultSpacing.s) {
            Image(systemName: "exclamationmark.octagon.fill")
                .foregroundStyle(Color.statusError)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: VaultSpacing.xxs) {
                Text(error.title)
                    .font(.subheadline.weight(.semibold))
                    .fixedSize(horizontal: false, vertical: true)
                Text(error.message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .padding(VaultSpacing.m)
        .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
        .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
        .accessibilityElement(children: .combine)
    }

    // MARK: - Bottom bar

    private var bottomBar: some View {
        VStack(spacing: VaultSpacing.m) {
            pageDots

            Button {
                handlePrimaryAction()
            } label: {
                // The exit stays honest (#69): "Open VaultSync" only once every
                // step is actually done — otherwise the button says what really
                // happens (setup continues later from the home screen).
                Text(page == 0
                    ? L10n.tr("onboarding.cta.continue")
                    : allStepsComplete
                        ? L10n.tr("onboarding.cta.openVaultSync")
                        : L10n.tr("onboarding.cta.finishLater"))
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .tint(teal)
            .frame(maxWidth: .infinity)
        }
    }

    private var pageDots: some View {
        HStack(spacing: VaultSpacing.s) {
            ForEach(0..<2, id: \.self) { index in
                Circle()
                    .fill(index == page ? teal : Color.vaultSlateFill)
                    .frame(width: 8, height: 8)
            }
        }
        // Padding BEFORE the a11y element so the announced frame is bigger
        // than the bare 8pt dots (#71) — the dots are not interactive, this
        // only widens the VoiceOver target.
        .padding(.vertical, VaultSpacing.s)
        .padding(.horizontal, VaultSpacing.m)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.fmt("onboarding.accessibility.page", page + 1))
    }

    private func handlePrimaryAction() {
        if page == 0 {
            withAnimation(.easeInOut(duration: 0.25)) { page = 1 }
        } else {
            hasCompletedOnboarding = true
        }
    }

    // MARK: - Shared chrome

    private var backgroundView: some View {
        ZStack(alignment: .top) {
            Color(uiColor: .systemGroupedBackground)
                .ignoresSafeArea()

            Circle()
                .fill(Color.vaultAccentFillSubtle)
                .frame(width: 220, height: 220)
                .blur(radius: 18)
                .offset(x: -120, y: -70)

            Circle()
                .fill(Color.vaultSlateFillSubtle)
                .frame(width: 240, height: 240)
                .blur(radius: 22)
                .offset(x: 130, y: -110)
        }
        .accessibilityHidden(true)
    }

    private func benefitRow(icon: String, textKey: String) -> some View {
        HStack(alignment: .top, spacing: VaultSpacing.m) {
            ZStack {
                RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous)
                    .fill(Color.vaultAccentFill)
                    .frame(width: 36, height: 36)
                Image(systemName: icon)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(teal)
                    .accessibilityHidden(true)
            }
            Text(L10n.tr(textKey))
                .font(.body.weight(.semibold))
                .foregroundStyle(primaryHeadingColor)
                .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
    }

    private var topAccentBar: some View {
        HStack(spacing: VaultSpacing.s) {
            Capsule()
                .fill(teal)
                .frame(width: 64, height: 8)
            Capsule()
                .fill(Color.vaultSlateFill)
                .frame(width: 24, height: 8)
        }
        .accessibilityHidden(true)
    }

    private var titleFont: Font {
        dynamicTypeSize.isAccessibilitySize ? .title.weight(.bold) : .largeTitle.weight(.bold)
    }

    private var primaryHeadingColor: Color {
        colorScheme == .dark ? .white : slate
    }

    private var cardBackground: Color {
        Color(uiColor: colorScheme == .dark ? .secondarySystemBackground : .systemBackground)
    }

    private func cardStroke<S: Shape>(in shape: S) -> some View {
        shape.stroke(Color.vaultHairline, lineWidth: 1)
    }
}

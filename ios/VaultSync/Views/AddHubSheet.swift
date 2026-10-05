import SwiftUI

/// The Add Hub sheet (#174) — the approved canvas (#187, version 2): the code
/// from the Hub in three fields with the network search beside it, the vault
/// choice, then a hand-off that reports where the Hub's share stands. Built
/// from the DesignSystem kit; text never takes a status or brand color
/// (decision 044). The flow itself lives in `HubPairingModel`.
///
/// A link (QR code, Camera app) only prefills the sheet; pairing takes the
/// user's tap. The vault is accepted by the existing pending-share flow, never
/// here.
struct AddHubSheet: View {
    let syncthingManager: SyncthingManager
    let vaultManager: VaultManager
    let shareAccept: ShareAcceptCoordinator
    var link: HubPairingLink?
    /// Opens the Sync tab's pending shares; nil where there is none
    /// (onboarding shows the offer on its own screen).
    var onReviewShares: (() -> Void)?

    @State private var model: HubPairingModel
    @State private var showScanner = false
    @State private var scannedText: String?
    @State private var scanProblem: HubPairingLink.ParseError?
    @FocusState private var codeFocus: HubCodeField?
    @Environment(\.dismiss) private var dismiss

    init(
        syncthingManager: SyncthingManager,
        vaultManager: VaultManager,
        shareAccept: ShareAcceptCoordinator,
        link: HubPairingLink? = nil,
        onReviewShares: (() -> Void)? = nil,
        model: HubPairingModel? = nil
    ) {
        self.syncthingManager = syncthingManager
        self.vaultManager = vaultManager
        self.shareAccept = shareAccept
        self.link = link
        self.onReviewShares = onReviewShares
        _model = State(initialValue: model ?? HubPairingModel(
            environment: .live(syncthingManager: syncthingManager)
        ))
    }

    var body: some View {
        NavigationStack(path: $model.path) {
            HubCodeStep(model: model, focus: $codeFocus, onScan: { showScanner = true })
                .navigationTitle(L10n.tr("Add Hub"))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    cancelItem
                    // The number pad has no return key and covers the
                    // buttons. In a second .toolbar inside the step this
                    // item never showed (iOS 26), so it shares this one.
                    ToolbarItemGroup(placement: .keyboard) {
                        Spacer()
                        Button(L10n.tr("Done")) { codeFocus = nil }
                    }
                }
                .navigationDestination(for: HubPairingModel.Step.self) { step in
                    switch step {
                    case .vault:
                        HubVaultStep(model: model, vaultManager: vaultManager, syncthingManager: syncthingManager)
                            .navigationTitle(L10n.tr("Choose a vault"))
                            .navigationBarTitleDisplayMode(.inline)
                            // No way back while the Hub is being asked: its
                            // answer belongs to this step.
                            .navigationBarBackButtonHidden(model.isWorking)
                            .toolbar { cancelItem }
                    case .done:
                        HubDoneStep(
                            model: model,
                            syncthingManager: syncthingManager,
                            vaultManager: vaultManager,
                            shareAccept: shareAccept,
                            onReviewShares: onReviewShares.map { review in
                                { dismiss(); review() }
                            },
                            onDone: { dismiss() }
                        )
                        .navigationTitle(L10n.tr("Add Hub"))
                        .navigationBarTitleDisplayMode(.inline)
                        .navigationBarBackButtonHidden(true)
                    }
                }
        }
        // Toolbar text (Cancel, Back) in the text token, not the brand teal:
        // text never takes the brand color (decision 044).
        .tint(Color.vaultAccentText)
        .onAppear { model.start(link: link) }
        .onDisappear { model.stop() }
        .onChange(of: model.path) { _, _ in model.returnedToCode() }
        .sheet(isPresented: $showScanner, onDismiss: applyScan) {
            QRScannerView(
                title: L10n.tr("Scan Hub QR Code"),
                deniedMessage: L10n.tr("VaultSync needs camera access to scan the QR code your Hub printed. Please enable it in Settings."),
                unavailableMessage: L10n.tr("The camera could not be started on this device. Type the code instead — your Hub printed it above the QR code."),
                manualButtonTitle: L10n.tr("Enter Code Manually")
            ) { scanned in
                scannedText = scanned
            }
        }
        // Presented from the scanner's onDismiss: an alert raised while the
        // scanner sheet is still dismissing would be dropped.
        .alert(
            L10n.tr("Not a Hub QR Code"),
            isPresented: Binding(
                get: { scanProblem != nil },
                set: { if !$0 { scanProblem = nil } }
            ),
            presenting: scanProblem
        ) { _ in
            Button(L10n.tr("OK")) { scanProblem = nil }
        } message: { problem in
            Text(HubPairingCopy.scanProblem(problem))
        }
    }

    private var cancelItem: some ToolbarContent {
        ToolbarItem(placement: .cancellationAction) {
            Button(L10n.tr("Cancel")) { dismiss() }
        }
    }

    private func applyScan() {
        guard let text = scannedText else { return }
        scannedText = nil
        switch HubPairingLink.parse(text) {
        case .success(let link):
            model.apply(link: link)
        case .failure(let problem):
            scanProblem = problem
        }
    }
}

// MARK: - Step 1: the code

private struct HubCodeStep: View {
    let model: HubPairingModel
    var focus: FocusState<HubCodeField?>.Binding
    var onScan: () -> Void

    @AccessibilityFocusState private var failureFocused: Bool

    var body: some View {
        VaultPage {
            VStack(alignment: .leading, spacing: VaultSpacing.s) {
                Text(L10n.tr("Enter the code from your Hub"))
                    .font(.title.weight(.bold))
                    .foregroundStyle(Color.vaultLabel)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
                Text(L10n.tr("Your Hub printed it when you set it up. Codes last 24 hours and work only on the Hub’s local network."))
                    .font(.body)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .fixedSize(horizontal: false, vertical: true)
            }
            // Full width, not the widest line: placed at its own line width, a
            // hyphenated German subtitle re-wrapped into one line more than it
            // was measured for and got cut off.
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, VaultSpacing.m)
            .padding(.bottom, VaultSpacing.xs)

            HubCodeEntry(model: model, focus: focus)
                .padding(.vertical, VaultSpacing.s)

            if model.codeLooksWrong {
                VaultNotice(
                    systemImage: "exclamationmark.circle",
                    text: L10n.tr("That doesn’t look like a code from your Hub. Check the two words and the number."),
                    tone: .attention
                )
            }

            if let failure = model.failure {
                VaultNotice(
                    systemImage: HubPairingCopy.symbol(for: failure.kind),
                    text: HubPairingCopy.message(for: failure.kind),
                    tone: HubPairingCopy.tone(for: failure.kind)
                )
                .accessibilityFocused($failureFocused)
            }

            HubDiscoveryLine(model: model)

            if case .found(let hubs) = model.discovery, hubs.count > 1 {
                HubChooser(model: model, hubs: hubs)
            }

            Button {
                focus.wrappedValue = nil
                model.pair()
            } label: {
                HStack(spacing: VaultSpacing.s) {
                    if model.isWorking {
                        ProgressView()
                            .tint(Color.vaultOnAccent)
                            .accessibilityHidden(true)
                        Text(L10n.tr("Pairing…"))
                    } else {
                        Text(L10n.tr("Pair with Hub"))
                    }
                }
            }
            .buttonStyle(.vault(.primary))
            .disabled(!model.canPair)
            .padding(.top, VaultSpacing.xs)

            Button(action: onScan) {
                Label(L10n.tr("Scan QR instead"), systemImage: "qrcode.viewfinder")
            }
            .buttonStyle(.vault(.tinted))
            .disabled(model.isWorking)

            HStack(alignment: .top, spacing: 10) {
                Image(systemName: "lock")
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .accessibilityHidden(true)
                Text(L10n.tr("The code turns into a key only your iPhone and your Hub share. Nobody on the network can listen in or pretend to be your Hub."))
                    .font(.footnote)
                    .foregroundStyle(Color.vaultSecondaryLabel)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, VaultSpacing.xs)
            .padding(.vertical, VaultSpacing.s)
            .accessibilityElement(children: .combine)
        }
        // A drag on the page puts the keyboard away too.
        .scrollDismissesKeyboard(.interactively)
        .onChange(of: model.failure) { _, failure in
            if failure != nil { failureFocused = true }
        }
        #if DEBUG
        .task {
            // LAB: the design preview's code-keyboard step (#174).
            guard UIAuditFixture.active == UIAuditFixture.designPreview,
                  DesignPreviewFixture.addHubShowsKeyboard else { return }
            try? await Task.sleep(for: .milliseconds(600))
            focus.wrappedValue = .number
        }
        #endif
    }
}

/// WORD – WORD – NN: three fields that grow with Dynamic Type and stack at
/// the accessibility sizes. A separator moves to the next field; a full code
/// pasted anywhere fills all three. Never submits on its own.
private struct HubCodeEntry: View {
    let model: HubPairingModel
    var focus: FocusState<HubCodeField?>.Binding

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(spacing: VaultSpacing.s))
            : AnyLayout(HStackLayout(alignment: .center, spacing: VaultSpacing.s))
        layout {
            field(.first, text: model.fields.first, label: L10n.tr("First word"))
            separator
            field(.second, text: model.fields.second, label: L10n.tr("Second word"))
            separator
            field(.number, text: model.fields.number, label: L10n.tr("Number"))
                .frame(maxWidth: dynamicTypeSize.isAccessibilitySize ? .infinity : 84)
        }
        .frame(maxWidth: .infinity)
    }

    @ViewBuilder
    private var separator: some View {
        if !dynamicTypeSize.isAccessibilitySize {
            Text("–")
                .font(.title3)
                .foregroundStyle(Color.vaultSecondaryLabel)
                .accessibilityHidden(true)
        }
    }

    private func field(_ which: HubCodeField, text: String, label: String) -> some View {
        let isNumber = which == .number
        let active = focus.wrappedValue == which || !text.isEmpty
        return TextField(
            "",
            text: Binding(
                get: { text },
                set: { newValue in
                    if let next = model.update(which, with: newValue) {
                        focus.wrappedValue = next == HubCodeField.none ? nil : next
                    }
                }
            )
        )
        .focused(focus, equals: which)
        .font(.vaultMono(.title3, weight: .semibold))
        .tracking(1)
        .multilineTextAlignment(.center)
        .foregroundStyle(Color.vaultLabel)
        .keyboardType(isNumber ? .asciiCapableNumberPad : .asciiCapable)
        .textInputAutocapitalization(.characters)
        .autocorrectionDisabled()
        .submitLabel(.next)
        .onSubmit {
            switch which {
            case .first: focus.wrappedValue = .second
            case .second: focus.wrappedValue = .number
            default: focus.wrappedValue = nil
            }
        }
        .padding(.horizontal, VaultSpacing.s)
        .frame(maxWidth: .infinity, minHeight: 56)
        .background(Color.vaultSurface, in: RoundedRectangle(cornerRadius: VaultRadius.button, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: VaultRadius.button, style: .continuous)
                .strokeBorder(active ? Color.vaultAccent : Color.vaultHairline, lineWidth: 1.5)
        }
        .contentShape(RoundedRectangle(cornerRadius: VaultRadius.button, style: .continuous))
        .onTapGesture { focus.wrappedValue = which }
        .accessibilityLabel(label)
    }
}

/// The search status beside the code (canvas: spinner on the accent wash).
private struct HubDiscoveryLine: View {
    let model: HubPairingModel

    var body: some View {
        switch model.discovery {
        case .idle:
            EmptyView()
        case .searching:
            line(tone: .accent, busy: true, symbol: nil, text: L10n.tr("Looking for your Hub on this network…"))
        case .found(let hubs):
            if hubs.count == 1 {
                line(tone: .success, busy: false, symbol: "checkmark.circle.fill",
                     text: L10n.fmt("Found “%@” on this network.", HubPairingCopy.hubName(hubs[0].name)))
            } else {
                line(tone: .accent, busy: false, symbol: "server.rack",
                     text: L10n.fmt("Found %d Hubs on this network. Choose yours.", hubs.count))
            }
        case .noneFound:
            VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                line(tone: .attention, busy: false, symbol: "wifi.exclamationmark",
                     text: L10n.tr("No Hub answered on this network. Make sure this iPhone is on the same network as your Hub, or scan the QR code your Hub printed."))
                searchAgain
            }
        case .failed:
            VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                line(tone: .error, busy: false, symbol: "wifi.slash",
                     text: L10n.tr("VaultSync could not search this network. Turn on Wi‑Fi and allow VaultSync in Settings → Privacy & Security → Local Network."))
                searchAgain
            }
        case .fromLink:
            VStack(alignment: .leading, spacing: VaultSpacing.xs) {
                line(tone: .accent, busy: false, symbol: "qrcode",
                     text: model.linkAddressUnreachable
                        ? L10n.tr("The Hub address from the QR code did not answer.")
                        : L10n.tr("Using the Hub address from the QR code."))
                if model.linkAddressUnreachable {
                    Button(L10n.tr("Search the Network Instead"), action: model.search)
                        .buttonStyle(.vaultLink)
                        .padding(.horizontal, VaultSpacing.xs)
                }
            }
        }
    }

    private var searchAgain: some View {
        Button(L10n.tr("Search Again"), action: model.search)
            .buttonStyle(.vaultLink)
            .padding(.horizontal, VaultSpacing.xs)
            .disabled(model.isWorking)
    }

    private func line(tone: VaultTone, busy: Bool, symbol: String?, text: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Group {
                if busy {
                    ProgressView()
                        .tint(tone.tint)
                } else if let symbol {
                    Image(systemName: symbol)
                        .foregroundStyle(tone.tint)
                }
            }
            .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
                .foregroundStyle(Color.vaultLabel)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, VaultSpacing.m)
        .vaultCard(tone: tone, radius: VaultRadius.button)
        .accessibilityElement(children: .combine)
    }
}

/// Several Hubs answered: the user picks one — a code is never tried on a Hub
/// the user did not choose.
private struct HubChooser: View {
    let model: HubPairingModel
    let hubs: [HubCandidate]

    var body: some View {
        VaultCardGroup {
            ForEach(hubs, id: \.self) { hub in
                Button {
                    model.chosenHub = hub
                } label: {
                    VaultRow(
                        HubPairingCopy.hubName(hub.name),
                        subtitle: hub.address,
                        systemImage: "server.rack",
                        monospacedSubtitle: true
                    ) {
                        HubRadioMark(selected: model.chosenHub == hub)
                    }
                }
                .buttonStyle(.vaultRow)
                .accessibilityAddTraits(model.chosenHub == hub ? .isSelected : [])
            }
        }
    }
}

// MARK: - Step 2: the vault

private struct HubVaultStep: View {
    let model: HubPairingModel
    let vaultManager: VaultManager
    let syncthingManager: SyncthingManager

    @AccessibilityFocusState private var failureFocused: Bool

    var body: some View {
        VaultPage {
            if let hello = model.hello {
                connectedLine(hello.hubName)
                content(hello)
            }
        }
        .onChange(of: model.failure) { _, failure in
            if failure != nil { failureFocused = true }
        }
    }

    private func connectedLine(_ hubName: String) -> some View {
        let name = HubPairingCopy.hubName(hubName)
        var text = AttributedString(L10n.fmt("Connected to %@", name))
        if let range = text.range(of: name, options: .backwards) {
            text[range].font = .body.weight(.semibold)
        }
        return HStack(spacing: VaultSpacing.m) {
            Image(systemName: "checkmark")
                .font(.title3.weight(.bold))
                .foregroundStyle(Color.statusSuccess)
                .accessibilityHidden(true)
            Text(text)
                .font(.body)
                .foregroundStyle(Color.vaultLabel)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.top, VaultSpacing.xs)
        .padding(.bottom, VaultSpacing.s)
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder
    private func content(_ hello: HubHello) -> some View {
        if !hello.catalogAvailable {
            VaultNotice(
                systemImage: "exclamationmark.triangle",
                text: L10n.tr("Your Hub could not list its vaults just now. Go back and pair again in a moment."),
                tone: .attention
            )
        } else if hello.vaults.isEmpty {
            VaultEmptyState(
                systemImage: "tray",
                title: L10n.tr("Your Hub has no vaults yet"),
                message: L10n.tr("Create one on your Hub with “vaultsync-hub vault create NAME”, then pair again — your code stays valid for 24 hours.")
            )
        } else {
            VaultSectionHeader(L10n.tr("On your Hub"))
            VaultCardGroup {
                ForEach(hello.vaults) { vault in
                    vaultRow(vault)
                }
            }

            if let vault = model.selectedVault {
                destinationNotice(for: vault)
            }

            if let failure = model.failure {
                VaultNotice(
                    systemImage: HubPairingCopy.symbol(for: failure.kind),
                    text: HubPairingCopy.message(for: failure.kind, vault: model.selectedVault?.label),
                    tone: HubPairingCopy.tone(for: failure.kind)
                )
                .accessibilityFocused($failureFocused)
            }

            if model.everyVaultIsHere {
                VaultNotice(
                    systemImage: "checkmark.circle",
                    text: L10n.tr("Every vault on your Hub is already on this iPhone. Reconnect so your Hub and this iPhone know each other again."),
                    tone: .info
                )
                Button(action: model.reconnect) {
                    HStack(spacing: VaultSpacing.s) {
                        if model.isWorking {
                            ProgressView()
                                .tint(Color.vaultOnAccent)
                                .accessibilityHidden(true)
                            Text(L10n.tr("Asking your Hub…"))
                        } else {
                            Text(L10n.tr("Reconnect with Hub"))
                        }
                    }
                }
                .buttonStyle(.vault(.primary))
                .disabled(!model.canReconnect)
                .padding(.top, VaultSpacing.xs)
            } else {
                syncButton
            }
        }
    }

    private var syncButton: some View {
            Button(action: model.syncSelectedVault) {
                HStack(spacing: VaultSpacing.s) {
                    if model.isWorking {
                        ProgressView()
                            .tint(Color.vaultOnAccent)
                            .accessibilityHidden(true)
                        Text(L10n.tr("Asking your Hub…"))
                    } else if let vault = model.selectedVault {
                        Text(L10n.fmt("Sync “%@”", vault.label))
                    } else {
                        Text(L10n.tr("Choose a Vault to Sync"))
                    }
                }
            }
            .buttonStyle(.vault(.primary))
            .disabled(!model.canSync)
            .padding(.top, VaultSpacing.xs)
    }

    private func vaultRow(_ vault: HubVault) -> some View {
        let here = model.isAlreadyHere(vault)
        let selected = model.selectedVaultID == vault.id
        return Button {
            model.selectedVaultID = vault.id
        } label: {
            VaultRow(
                vault.label,
                subtitle: here ? L10n.tr("Already on this iPhone") : HubPairingCopy.vaultFacts(vault),
                systemImage: "folder",
                iconTint: here ? .vaultSecondaryLabel : .vaultAccent
            ) {
                if !here {
                    HubRadioMark(selected: selected)
                }
            }
        }
        .buttonStyle(.vaultRow)
        .disabled(here || model.isWorking)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    /// Where the vault will land, decided by the same rule the accept uses —
    /// advisory: the accept decides again when the share arrives.
    @ViewBuilder
    private func destinationNotice(for vault: HubVault) -> some View {
        switch vaultManager.previewShareDestination(folderID: vault.id, label: vault.label, syncthingManager: syncthingManager) {
        case nil:
            VaultNotice(
                systemImage: "folder.badge.questionmark",
                text: L10n.fmt("Connect your Obsidian folder first — VaultSync adds “%@” there once it’s connected.", vault.label),
                tone: .attention
            )
        case .path(let path):
            let folder = (path as NSString).lastPathComponent
            VaultNotice(
                systemImage: "folder",
                text: folder == vault.label
                    ? L10n.fmt("“%@” will be created inside your Obsidian folder. VaultSync never merges into a folder that already has files without asking you.", vault.label)
                    : L10n.fmt("“%@” will be created inside your Obsidian folder as “%@”. VaultSync never merges into a folder that already has files without asking you.", vault.label, folder),
                tone: .info
            )
        case .requiresMergeConfirmation(_, let targetName):
            VaultNotice(
                systemImage: "folder.badge.questionmark",
                text: L10n.fmt("The folder “%@” in your Obsidian folder already has files. When the vault arrives, VaultSync asks you before it combines them.", targetName),
                tone: .attention
            )
        case .refused(let message):
            VaultNotice(systemImage: "exclamationmark.triangle", text: message, tone: .attention)
        }
    }
}

/// The canvas's selection mark: a filled accent disc with a check, or an
/// empty ring.
private struct HubRadioMark: View {
    let selected: Bool

    /// The disc and its check scale together with Dynamic Type.
    @ScaledMetric(relativeTo: .body) private var size: CGFloat = 24

    var body: some View {
        ZStack {
            if selected {
                Circle().fill(Color.vaultAccentProminent)
                Image(systemName: "checkmark")
                    .font(.system(size: size * 0.5, weight: .bold))
                    .foregroundStyle(Color.vaultOnAccent)
            } else {
                Circle().strokeBorder(Color.vaultHairline, lineWidth: 2)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

// MARK: - Step 3: the hand-off

/// After the Hub agreed to share: the live state of that share on this
/// iPhone. Pairing asked for the share; the pending-share flow accepts it.
private struct HubDoneStep: View {
    let model: HubPairingModel
    let syncthingManager: SyncthingManager
    let vaultManager: VaultManager
    let shareAccept: ShareAcceptCoordinator
    var onReviewShares: (() -> Void)?
    var onDone: () -> Void

    private var handoff: HubShareHandoff {
        guard let vault = model.provisioned else { return .waitingForShare }
        return HubShareHandoff.status(
            folderID: vault.id,
            configuredFolderIDs: Set(syncthingManager.folders.map(\.id)),
            pendingFolderIDs: Set(syncthingManager.pendingFolders.map(\.id)),
            autoAcceptEligibleIDs: Set(syncthingManager.autoAcceptEligiblePendingFolders.map(\.id)),
            recordedFailureIDs: Set(shareAccept.pendingShareFailures.keys),
            obsidianConnected: vaultManager.isAccessible
        )
    }

    var body: some View {
        let label = model.provisioned?.label ?? ""
        let state = handoff
        VaultPage {
            if model.reconnected {
                VaultHeroHeader(
                    tone: model.reconnectUncertain ? .attention : .success,
                    systemImage: model.reconnectUncertain ? "exclamationmark" : "checkmark",
                    title: model.reconnectUncertain
                        ? L10n.tr("VaultSync couldn’t confirm the reconnect")
                        : L10n.fmt("Connected to %@", HubPairingCopy.hubName(model.hello?.hubName ?? "")),
                    subtitle: model.reconnectUncertain
                        ? L10n.tr("If a vault does not sync with your Hub, pair again.")
                        : L10n.tr("Your Hub and this iPhone are paired. To sync a vault, make sure sharing is enabled on both devices.")
                )
                .padding(.vertical, VaultSpacing.s)
                .accessibilityElement(children: .combine)
            } else {
                VaultHeroHeader(
                    tone: state == .syncing ? .success : (state == .needsDecision || state == .waitingForObsidian ? .attention : .accent),
                    systemImage: state == .syncing ? "checkmark" : (state == .needsDecision || state == .waitingForObsidian ? "exclamationmark" : "arrow.down"),
                    title: title(state, label: label),
                    subtitle: subtitle(state, label: label),
                    busy: state == .waitingForShare || state == .accepting
                )
                .padding(.vertical, VaultSpacing.s)
                .accessibilityElement(children: .combine)

                if state == .needsDecision, let onReviewShares {
                    Button(L10n.tr("Review in Sync"), action: onReviewShares)
                        .buttonStyle(.vault(.tinted))
                }
            }
            Button(L10n.tr("Done"), action: onDone)
                .buttonStyle(.vault(.primary))
        }
    }

    private func title(_ state: HubShareHandoff, label: String) -> String {
        if state == .syncing {
            return L10n.fmt("“%@” is on this iPhone", label)
        }
        // A lost answer is never reported as a share.
        if model.provisionUncertain, state == .waitingForShare {
            return L10n.fmt("Your Hub may be sharing “%@”", label)
        }
        return L10n.fmt("Your Hub is sharing “%@”", label)
    }

    private func subtitle(_ state: HubShareHandoff, label: String) -> String {
        switch state {
        case .waitingForShare:
            if model.provisionUncertain {
                return HubPairingCopy.message(for: .outcomeUnknown)
            }
            return L10n.tr("It arrives in a moment. VaultSync then adds it to your Obsidian folder — or asks you first when it needs your decision. You can close this screen.")
        case .waitingForObsidian:
            return L10n.tr("Connect your Obsidian folder so VaultSync can add it.")
        case .accepting:
            return L10n.tr("Adding it to your Obsidian folder…")
        case .needsDecision:
            return onReviewShares == nil
                ? L10n.tr("It needs your decision. Tap “Finish Setup Later” and review it under Pending Shares.")
                : L10n.tr("It needs your decision — review it under Pending Shares on the Sync tab.")
        case .syncing:
            return L10n.tr("It syncs with your Hub from now on.")
        }
    }
}

// MARK: - Copy

/// User-facing words for the flow. The bridge's diagnostic messages are never
/// shown; the failure kind picks the explanation.
enum HubPairingCopy {
    static func hubName(_ name: String) -> String {
        name.isEmpty ? L10n.tr("VaultSync Hub") : name
    }

    /// "1,284 files · 2 devices" — counts only when positive: the Hub reports
    /// zero files both for an empty vault and for one it could not read.
    static func vaultFacts(_ vault: HubVault) -> String? {
        var facts: [String] = []
        if vault.files > 0 {
            facts.append(vault.files == 1 ? L10n.tr("1 file") : L10n.fmt("%@ files", vault.files.formatted()))
        }
        if vault.devices > 0 {
            facts.append(vault.devices == 1 ? L10n.tr("1 device") : L10n.fmt("%d devices", vault.devices))
        }
        return facts.isEmpty ? nil : facts.joined(separator: " · ")
    }

    static func message(for kind: HubPairingFailureKind, vault: String? = nil) -> String {
        switch kind {
        case .codeRejected:
            return L10n.tr("Your Hub didn’t accept this code. Check the two words and the number — too many wrong tries lock the code.")
        case .authenticationFailed:
            return L10n.tr("This Hub could not prove that it knows the code. Make sure you’re pairing with your own Hub, then try again.")
        case .codeExpired:
            return L10n.tr("This code has expired. Create a new one on your Hub with “vaultsync-hub code”.")
        case .codeLocked:
            return L10n.tr("This code is locked after too many wrong tries. Create a new one on your Hub with “vaultsync-hub code”.")
        case .noCode:
            return L10n.tr("Your Hub has no active code. Create one on your Hub with “vaultsync-hub code”.")
        case .sessionExpired, .noSession:
            return L10n.tr("Pairing took too long. Tap “Pair with Hub” to connect again.")
        case .rateLimited:
            return L10n.tr("Too many pairing attempts from this iPhone. Wait a minute, then try again.")
        case .busy:
            return L10n.tr("Your Hub is busy pairing other devices. Try again in a moment.")
        case .notLocal:
            return L10n.tr("Your Hub pairs only on its local network. Connect this iPhone to the same network as your Hub.")
        case .unreachable:
            return L10n.tr("Your Hub did not answer. Check that it is running and on the same network as this iPhone.")
        case .incompatible:
            return L10n.tr("This Hub uses a different version of pairing. Update VaultSync and your Hub, then try again.")
        case .engine:
            return L10n.tr("Sync isn’t running right now, so VaultSync can’t add your Hub. Wait a moment, then try again.")
        case .unknownVault:
            return L10n.tr("This vault is no longer on your Hub. Pair again to see the current list.")
        case .hubRefused:
            if let vault {
                return L10n.fmt("Your Hub could not share “%@”. Check the Hub with “vaultsync-hub status”, then try again.", vault)
            }
            return L10n.tr("Your Hub could not share the vault. Check the Hub with “vaultsync-hub status”, then try again.")
        case .outcomeUnknown:
            return L10n.tr("VaultSync couldn’t confirm whether your Hub shared the vault. If it did, it appears here in a moment — otherwise pair again.")
        case .badCode:
            return L10n.tr("That doesn’t look like a code from your Hub. Check the two words and the number.")
        case .badAddress, .noNetwork, .staleFlow, .inProgress, .cancelled, .protocolError, .other:
            return L10n.tr("Something went wrong while pairing. Try again.")
        }
    }

    static func tone(for kind: HubPairingFailureKind) -> VaultTone {
        switch kind {
        case .authenticationFailed, .incompatible, .protocolError, .other, .badAddress, .noNetwork, .engine, .hubRefused:
            return .error
        default:
            return .attention
        }
    }

    static func symbol(for kind: HubPairingFailureKind) -> String {
        switch kind {
        case .authenticationFailed:
            return "exclamationmark.shield"
        case .codeExpired, .sessionExpired, .noSession:
            return "clock.badge.exclamationmark"
        case .codeLocked:
            return "lock.trianglebadge.exclamationmark"
        case .unreachable, .notLocal:
            return "wifi.exclamationmark"
        default:
            return "exclamationmark.circle"
        }
    }

    static func scanProblem(_ problem: HubPairingLink.ParseError) -> String {
        switch problem {
        case .deviceID:
            return L10n.tr("This QR code is a Syncthing Device ID. To pair with a Hub, scan the QR code your Hub printed with “vaultsync-hub code”.")
        case .invalidCode:
            return L10n.tr("This QR code carries no valid pairing code. Create a new code on your Hub with “vaultsync-hub code”.")
        case .hubNotLocal:
            return L10n.tr("This QR code points to an address outside your local network. VaultSync pairs only with a Hub on your own network.")
        case .hubMalformed, .notAPairingLink:
            return L10n.tr("This is not a Hub pairing QR code. Scan the QR code your Hub printed with “vaultsync-hub code”.")
        }
    }
}

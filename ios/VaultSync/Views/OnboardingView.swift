import SwiftUI

struct OnboardingView: View {
    @Binding var hasCompletedOnboarding: Bool
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager

    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    @State private var page = 0

    // Live setup actions — each step launches the real task instead of describing it.
    @State private var showObsidianPicker = false
    @State private var showAddDevice = false
    @State private var alertMessage: String?
    @State private var showAlert = false

    private let slate = Color.vaultSlate
    private let teal = Color.vaultTeal

    private var obsidianConnected: Bool { vaultManager.isAccessible }
    private var deviceAdded: Bool { !syncthingManager.devices.isEmpty }
    private var vaultSyncing: Bool { !syncthingManager.folders.isEmpty }

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

                    bottomBar
                        .padding(.horizontal, 20)
                        .padding(.top, 12)
                        .padding(.bottom, 8)
                        .background(.bar)
                }
                .toolbar(.hidden, for: .navigationBar)
            }
            .sheet(isPresented: $showObsidianPicker) {
                FolderPicker(initialDirectoryURL: vaultManager.obsidianDirectoryURL, onCancel: {
                    showObsidianPicker = false
                }) { url in
                    showObsidianPicker = false
                    if let err = vaultManager.grantAccess(url: url) {
                        present(error: err, fallbackTitle: L10n.tr("Obsidian Folder Connection Failed"))
                    }
                }
            }
            .sheet(isPresented: $showAddDevice) {
                AddDeviceSheet(syncthingManager: syncthingManager) { message in
                    alertMessage = message
                    showAlert = true
                }
            }
            .alert("Error", isPresented: $showAlert) {
                Button("OK") { }
            } message: {
                Text(alertMessage ?? "")
            }
        }
        .onAppear {
            vaultManager.restoreAccess()
            syncthingManager.start()
        }
    }

    private func page<Content: View>(_ content: Content) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                content
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 28)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
    }

    // MARK: - Welcome

    private var welcomeScreen: some View {
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 12) {
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
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.hero, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.hero, style: .continuous)))

            VStack(alignment: .leading, spacing: 14) {
                benefitRow(icon: "lock.shield.fill", textKey: "onboarding.welcome.benefit.private")
                benefitRow(icon: "books.vertical.fill", textKey: "onboarding.welcome.benefit.obsidian")
                benefitRow(icon: "icloud.slash.fill", textKey: "onboarding.welcome.benefit.noCloud")
            }
            .padding(20)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
        }
    }

    // MARK: - Setup (live, actionable)

    private var setupScreen: some View {
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 10) {
                Text(L10n.tr("Let’s get your vault synced"))
                    .font(titleFont)
                    .foregroundStyle(primaryHeadingColor)

                Text(L10n.tr("Complete these steps right here. They light up green as you go — and you can always finish them later from the home screen."))
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(24)
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
                action: nil
            )

            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "antenna.radiowaves.left.and.right")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(teal)
                    .accessibilityHidden(true)
                Text(L10n.tr("Optional: turn on Cloud Relay later for instant updates — you’ll find it on the Relay tab."))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(18)
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
        action: (() -> Void)?
    ) -> some View {
        HStack(alignment: .top, spacing: 14) {
            ZStack {
                Circle()
                    .fill(isComplete ? Color.statusSuccess : teal.opacity(colorScheme == .dark ? 0.22 : 0.14))
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
                    Text(description)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityValue(isComplete ? L10n.tr("Done") : "")

                if !isComplete, let actionTitle, let action {
                    Button(actionTitle, action: action)
                        .buttonStyle(.borderedProminent)
                        .controlSize(.regular)
                        .tint(teal)
                        .padding(.top, 4)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground, in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous))
        .overlay(cardStroke(in: RoundedRectangle(cornerRadius: VaultRadius.card, style: .continuous)))
    }

    // MARK: - Error presentation

    private func present(error: String, fallbackTitle: String) {
        alertMessage = SyncUserError.from(rawMessage: error, fallbackTitle: fallbackTitle).userVisibleDescription
        showAlert = true
    }

    // MARK: - Bottom bar

    private var bottomBar: some View {
        VStack(spacing: 14) {
            pageDots

            Button {
                handlePrimaryAction()
            } label: {
                Text(page == 0 ? L10n.tr("onboarding.cta.continue") : L10n.tr("onboarding.cta.openVaultSync"))
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .tint(teal)
            .frame(maxWidth: .infinity)
        }
    }

    private var pageDots: some View {
        HStack(spacing: 10) {
            ForEach(0..<2, id: \.self) { index in
                Circle()
                    .fill(index == page ? teal : slate.opacity(colorScheme == .dark ? 0.34 : 0.20))
                    .frame(width: 8, height: 8)
            }
        }
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
                .fill(teal.opacity(colorScheme == .dark ? 0.14 : 0.08))
                .frame(width: 220, height: 220)
                .blur(radius: 18)
                .offset(x: -120, y: -70)

            Circle()
                .fill(slate.opacity(colorScheme == .dark ? 0.10 : 0.05))
                .frame(width: 240, height: 240)
                .blur(radius: 22)
                .offset(x: 130, y: -110)
        }
        .accessibilityHidden(true)
    }

    private func benefitRow(icon: String, textKey: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            ZStack {
                RoundedRectangle(cornerRadius: 11, style: .continuous)
                    .fill(teal.opacity(colorScheme == .dark ? 0.22 : 0.14))
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
        HStack(spacing: 10) {
            Capsule()
                .fill(teal)
                .frame(width: 64, height: 8)
            Capsule()
                .fill(slate.opacity(colorScheme == .dark ? 0.42 : 0.20))
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
        shape.stroke(
            Color(uiColor: .separator).opacity(colorScheme == .dark ? 0.45 : 0.18),
            lineWidth: 1
        )
    }
}

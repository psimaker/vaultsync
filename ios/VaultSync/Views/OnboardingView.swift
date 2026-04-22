import SwiftUI

struct OnboardingView: View {
    @Binding var hasCompletedOnboarding: Bool
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager

    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var currentScreen: Screen = .welcome

    private enum Screen: Int {
        case welcome = 0
        case overview = 1

        var pageIndex: Int { rawValue }
    }

    private struct OverviewStep: Identifiable {
        let number: Int
        let titleKey: String
        let descriptionKey: String

        var id: Int { number }
    }

    private let slate = Color(red: 38 / 255, green: 50 / 255, blue: 56 / 255)
    private let teal = Color(red: 0 / 255, green: 137 / 255, blue: 123 / 255)

    private var overviewSteps: [OverviewStep] {
        [
            OverviewStep(
                number: 1,
                titleKey: "onboarding.overview.step1.title",
                descriptionKey: "onboarding.overview.step1.description"
            ),
            OverviewStep(
                number: 2,
                titleKey: "onboarding.overview.step2.title",
                descriptionKey: "onboarding.overview.step2.description"
            ),
            OverviewStep(
                number: 3,
                titleKey: "onboarding.overview.step3.title",
                descriptionKey: "onboarding.overview.step3.description"
            ),
            OverviewStep(
                number: 4,
                titleKey: "onboarding.overview.step4.title",
                descriptionKey: "onboarding.overview.step4.description"
            ),
        ]
    }

    var body: some View {
        NavigationStack {
            GeometryReader { geometry in
                ZStack {
                    backgroundView

                    ScrollView {
                        VStack(alignment: .leading, spacing: 28) {
                            if currentScreen == .welcome {
                                welcomeScreen
                            } else {
                                overviewScreen
                            }

                            Spacer(minLength: currentScreen == .welcome ? 32 : 12)

                            primaryActionSection
                        }
                        .padding(.horizontal, 20)
                        .padding(.vertical, 28)
                        .frame(maxWidth: .infinity, minHeight: geometry.size.height, alignment: .topLeading)
                    }
                }
                .toolbar(.hidden, for: .navigationBar)
            }
        }
        .onAppear {
            vaultManager.restoreAccess()
            syncthingManager.start()
        }
    }

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
    }

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
            .background(cardBackground, in: RoundedRectangle(cornerRadius: 28, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: 28, style: .continuous)))

            VStack(alignment: .leading, spacing: 14) {
                benefitRow(icon: "lock.shield.fill", textKey: "onboarding.welcome.benefit.private")
                benefitRow(icon: "books.vertical.fill", textKey: "onboarding.welcome.benefit.obsidian")
                benefitRow(icon: "icloud.slash.fill", textKey: "onboarding.welcome.benefit.noCloud")
            }
            .padding(20)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: 24, style: .continuous)))
        }
    }

    private var overviewScreen: some View {
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 10) {
                Text(L10n.tr("onboarding.overview.title"))
                    .font(titleFont)
                    .foregroundStyle(primaryHeadingColor)

                Text(L10n.tr("onboarding.overview.subtitle"))
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: 28, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: 28, style: .continuous)))

            VStack(alignment: .leading, spacing: 14) {
                ForEach(overviewSteps) { step in
                    overviewStepRow(step)
                }
            }

            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "gearshape.2.fill")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(teal)
                    .accessibilityHidden(true)

                Text(L10n.tr("onboarding.overview.cloudRelay"))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(18)
            .background(cardBackground, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .overlay(cardStroke(in: RoundedRectangle(cornerRadius: 22, style: .continuous)))
            .accessibilityElement(children: .combine)
        }
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

    private func overviewStepRow(_ step: OverviewStep) -> some View {
        HStack(alignment: .top, spacing: 14) {
            Text("\(step.number)")
                .font(.headline.weight(.bold).monospacedDigit())
                .foregroundStyle(.white)
                .frame(width: 34, height: 34)
                .background(teal, in: Circle())
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 6) {
                Text(L10n.tr(step.titleKey))
                    .font(.body.weight(.semibold))
                    .foregroundStyle(primaryHeadingColor)

                Text(L10n.tr(step.descriptionKey))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .overlay(cardStroke(in: RoundedRectangle(cornerRadius: 22, style: .continuous)))
        .accessibilityElement(children: .combine)
    }

    private var pageIndicator: some View {
        HStack(spacing: 10) {
            pageDot(isActive: currentScreen == .welcome)
            pageDot(isActive: currentScreen == .overview)
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.fmt("onboarding.accessibility.page", currentScreen.pageIndex + 1))
    }

    private func pageDot(isActive: Bool) -> some View {
        Circle()
            .fill(isActive ? teal : slate.opacity(colorScheme == .dark ? 0.34 : 0.20))
            .frame(width: 10, height: 10)
            .overlay(
                Circle()
                    .stroke(teal.opacity(isActive ? 0 : 0.35), lineWidth: 1)
            )
    }

    private var primaryActionSection: some View {
        VStack(spacing: 16) {
            pageIndicator
                .frame(maxWidth: .infinity)

            Button {
                handlePrimaryAction()
            } label: {
                Text(actionButtonTitle)
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .tint(teal)
            .frame(maxWidth: .infinity)
            .accessibilityHint(actionButtonHint)
        }
    }

    private var actionButtonTitle: String {
        currentScreen == .welcome
            ? L10n.tr("onboarding.cta.continue")
            : L10n.tr("onboarding.cta.openVaultSync")
    }

    private var actionButtonHint: String {
        currentScreen == .welcome
            ? L10n.tr("onboarding.accessibility.continueHint")
            : L10n.tr("onboarding.accessibility.openVaultSyncHint")
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

    private func handlePrimaryAction() {
        switch currentScreen {
        case .welcome:
            withAnimation(.easeInOut(duration: 0.25)) {
                currentScreen = .overview
            }
        case .overview:
            completeOnboarding()
        }
    }

    private func completeOnboarding() {
        hasCompletedOnboarding = true
    }
}

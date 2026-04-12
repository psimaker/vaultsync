import SwiftUI

struct OnboardingView: View {
    @Binding var hasCompletedOnboarding: Bool
    var syncthingManager: SyncthingManager
    var vaultManager: VaultManager
    var subscriptionManager: SubscriptionManager

    @State private var checklistViewModel: SetupChecklistViewModel
    @State private var showWelcome = true

    init(
        hasCompletedOnboarding: Binding<Bool>,
        syncthingManager: SyncthingManager,
        vaultManager: VaultManager,
        subscriptionManager: SubscriptionManager
    ) {
        _hasCompletedOnboarding = hasCompletedOnboarding
        self.syncthingManager = syncthingManager
        self.vaultManager = vaultManager
        self.subscriptionManager = subscriptionManager
        _checklistViewModel = State(
            initialValue: SetupChecklistViewModel(
                syncthingManager: syncthingManager,
                vaultManager: vaultManager,
                subscriptionManager: subscriptionManager
            )
        )
    }

    var body: some View {
        NavigationStack {
            if showWelcome {
                welcomeScreen
            } else {
                setupFlow
            }
        }
        .onAppear {
            vaultManager.restoreAccess()
            syncthingManager.start()
        }
    }

    // MARK: - Welcome Screen

    private var welcomeScreen: some View {
        ScrollView {
            VStack(spacing: 32) {
                Spacer(minLength: 20)

                VStack(spacing: 12) {
                    Image(systemName: "arrow.triangle.2.circlepath.circle.fill")
                        .font(.largeTitle)
                        .imageScale(.large)
                        .foregroundStyle(Color.accentColor)
                        .accessibilityHidden(true)
                    Text("Welcome to VaultSync")
                        .font(.largeTitle.bold())
                    Text("Sync your Obsidian vaults privately with Syncthing — no cloud required.")
                        .font(.body)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)

                VStack(alignment: .leading, spacing: 8) {
                    featureRow(icon: "lock.shield", text: "End-to-end encrypted")
                    featureRow(icon: "server.rack", text: "No cloud required")
                    featureRow(icon: "bolt", text: "Fast Markdown sync")
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemBackground))
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))

                VStack(alignment: .leading, spacing: 16) {
                    Text("Before you start")
                        .font(.title3.bold())

                    prerequisiteRow(
                        icon: "desktopcomputer",
                        title: "Syncthing on your desktop",
                        description: "Install and run Syncthing on the computer you sync with."
                    )

                    Link(destination: URL(string: "https://syncthing.net/downloads/")!) {
                        Label("Download Syncthing", systemImage: "arrow.down.circle")
                            .font(.subheadline)
                    }
                    .padding(.leading, 36)

                    prerequisiteRow(
                        icon: "books.vertical",
                        title: "Obsidian on both devices",
                        description: "Install Obsidian on this iPhone and on your desktop."
                    )

                    prerequisiteRow(
                        icon: "wifi",
                        title: "Same network or global discovery",
                        description: "Both devices should be on the same WiFi, or have global discovery enabled in Syncthing."
                    )
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                VStack(alignment: .leading, spacing: 12) {
                    Text("How pairing works")
                        .font(.title3.bold())

                    pairingStepRow(number: "1", text: "Both devices exchange Device IDs")
                    pairingStepRow(number: "2", text: "Your desktop shares a vault folder")
                    pairingStepRow(number: "3", text: "VaultSync keeps everything in sync")
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                Button {
                    withAnimation(.easeInOut(duration: 0.3)) {
                        showWelcome = false
                    }
                } label: {
                    Text("Let's Get Started")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .padding(.top, 8)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
        }
        .navigationTitle("VaultSync")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func prerequisiteRow(icon: String, title: LocalizedStringKey, description: LocalizedStringKey) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: icon)
                .font(.title3)
                .foregroundStyle(Color.accentColor)
                .frame(width: 24, alignment: .center)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.subheadline.bold())
                Text(description)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func pairingStepRow(number: String, text: LocalizedStringKey) -> some View {
        HStack(spacing: 12) {
            Text(number)
                .font(.caption.bold().monospacedDigit())
                .foregroundStyle(.white)
                .frame(width: 22, height: 22)
                .background(Color.accentColor, in: Circle())
                .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
        }
        .accessibilityElement(children: .combine)
    }

    // MARK: - Setup Flow

    private var setupFlow: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                SetupChecklistView(viewModel: checklistViewModel)

                Button("Open VaultSync") {
                    completeOnboarding()
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .frame(maxWidth: .infinity)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
        }
        .navigationTitle("VaultSync Setup")
        .navigationBarTitleDisplayMode(.inline)
    }

    // MARK: - Helpers

    private func featureRow(icon: String, text: LocalizedStringKey) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .foregroundStyle(Color.accentColor)
                .frame(width: 20)
                .accessibilityHidden(true)
            Text(text)
                .font(.subheadline)
        }
        .accessibilityElement(children: .combine)
    }

    private func completeOnboarding() {
        hasCompletedOnboarding = true
    }
}

import SwiftUI
import UIKit

/// Guides the user through the one mandatory server-side step for Cloud Relay:
/// running the `vaultsync-notify` helper on their homeserver. Cloud Relay does
/// nothing until this helper is sending wake-ups, so this screen is surfaced
/// right after purchase and from the Cloud Relay settings section.
struct RelayServerSetupView: View {
    /// Whether the relay has already delivered a wake-up to this device. When
    /// true the helper is confirmed running and we show a success banner.
    var isDelivering: Bool = false

    @State private var installerCopied = false
    @State private var commandCopied = false

    /// The primary setup path: a one-line installer that finds config.xml on the
    /// server, runs the helper as the uid:gid owning it (the #1 setup failure),
    /// and picks Docker or a prebuilt-binary service automatically. Nothing in
    /// it is user-specific — identity comes from the server's own Syncthing at
    /// runtime — so it is a stable constant, short enough to type by hand when
    /// copy-paste can't reach the server shell.
    private static let installerCommand = "curl -fsSL https://vaultsync.eu/notify.sh | sh"

    /// Self-contained command for users who prefer to run the container
    /// themselves. The relay URL is pre-filled and there is no API key to copy —
    /// the helper reads the Syncthing key (and address) straight from the
    /// mounted config.xml. `--network host` is kept so the address auto-inferred
    /// from config.xml (typically 127.0.0.1:8384) reaches a same-host Syncthing.
    /// We interpolate `productionRelayURL` (never the DEBUG-overridable
    /// `relayURL`) so a lab build pointed at a mock can't leak that URL into the
    /// command shown to the user.
    private var dockerCommand: String {
        """
        docker run -d --name vaultsync-notify --restart unless-stopped \\
          --network host \\
          -v /PATH/TO/syncthing:/config:ro \\
          -e SYNCTHING_CONFIG=/config/config.xml \\
          -e RELAY_URL=\(RelayService.productionRelayURL) \\
          ghcr.io/psimaker/vaultsync-notify:latest
        """
    }

    var body: some View {
        List {
            if isDelivering {
                Section {
                    Label(L10n.tr("Your server helper is running — wake-ups are being delivered."), systemImage: "checkmark.seal.fill")
                        .foregroundStyle(Color.statusSuccess)
                        .font(.subheadline)
                }
            }

            Section {
                Text(L10n.tr("Cloud Relay needs a small helper on your server. It watches Syncthing for changes and sends VaultSync a wake-up signal — it never sees your notes. Without it, the subscription has nothing to wake the app with."))
                    .font(.subheadline)
            } header: {
                Text(L10n.tr("Why this step"))
            }

            Section {
                commandBox(Self.installerCommand, accessibilityLabelKey: "Installer command")
                copyButton(for: Self.installerCommand, copied: $installerCopied)
            } header: {
                Text(L10n.tr("One step — run this on your server"))
            } footer: {
                Text(L10n.tr("That’s the whole setup — nothing to edit, no API key to copy. The installer finds your Syncthing config, sets the right permissions, and starts the helper. The helper then wakes this iPhone once on its own, and VaultSync flips to “Cloud Relay active” as soon as that first wake-up arrives. Keep it running on a machine that stays on (server or NAS)."))
            }

            // The manual path stays collapsed by default: the one-liner above is
            // the whole setup for most users, and surfacing Docker flags up front
            // made it read as required homework.
            Section {
                DisclosureGroup {
                    Text(L10n.tr("The installer is open source — add --dry-run to preview every action without changing anything. Prefer to run the container yourself? Use this command:"))
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                    commandBox(dockerCommand, accessibilityLabelKey: "Server setup command")
                    copyButton(for: dockerCommand, copied: $commandCopied)
                    Text(L10n.tr("No API key needed — the helper reads it from Syncthing’s config.xml. Replace /PATH/TO/syncthing with your Syncthing config folder (often ~/.local/state/syncthing or ~/.config/syncthing). Permission error? Add -u <uid>:<gid> for the user that owns config.xml."))
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                    ExternalLinkButton(titleKey: "Full setup guide (Docker Compose, prebuilt binaries, NAS notes)", url: DocURL.serverSetupGuide)
                        .font(.subheadline)
                } label: {
                    Label(L10n.tr("Manual & advanced setup"), systemImage: "wrench.and.screwdriver")
                }
            } footer: {
                Text(L10n.tr("Prefer Docker Compose, a NAS package, or a plain binary? The full guide covers them all."))
            }
        }
        .navigationTitle(L10n.tr("Set Up Your Server"))
        .navigationBarTitleDisplayMode(.inline)
    }

    private func commandBox(_ command: String, accessibilityLabelKey: String) -> some View {
        ScrollView(.horizontal, showsIndicators: true) {
            Text(command)
                .font(.vaultMono(.caption))
                .textSelection(.enabled)
                .padding(VaultSpacing.m)
        }
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous))
        .accessibilityLabel(L10n.tr(accessibilityLabelKey))
        .accessibilityValue(command)
    }

    private func copyButton(for command: String, copied: Binding<Bool>) -> some View {
        Button {
            UIPasteboard.general.string = command
            UINotificationFeedbackGenerator().notificationOccurred(.success)
            copied.wrappedValue = true
            Task {
                try? await Task.sleep(for: .seconds(1.5))
                copied.wrappedValue = false
            }
        } label: {
            Label(
                copied.wrappedValue ? L10n.tr("Copied") : L10n.tr("Copy Command"),
                systemImage: copied.wrappedValue ? "checkmark.circle" : "doc.on.doc"
            )
        }
    }
}

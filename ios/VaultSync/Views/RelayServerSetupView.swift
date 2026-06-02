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

    @State private var commandCopied = false

    /// Self-contained command a user can paste into their server shell. The relay
    /// URL is pre-filled and there is no API key to copy — the helper reads the
    /// Syncthing key (and address) straight from the mounted config.xml. `--network
    /// host` is kept so the address auto-inferred from config.xml (typically
    /// 127.0.0.1:8384) reaches a same-host Syncthing. We interpolate
    /// `productionRelayURL` (never the DEBUG-overridable `relayURL`) so a lab build
    /// pointed at a mock can't leak that URL into the command shown to the user.
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
                commandBox
                Button {
                    UIPasteboard.general.string = dockerCommand
                    UINotificationFeedbackGenerator().notificationOccurred(.success)
                    commandCopied = true
                    Task {
                        try? await Task.sleep(for: .seconds(1.5))
                        commandCopied = false
                    }
                } label: {
                    Label(
                        commandCopied ? L10n.tr("Copied") : L10n.tr("Copy Command"),
                        systemImage: commandCopied ? "checkmark.circle" : "doc.on.doc"
                    )
                }
            } header: {
                Text(L10n.tr("Step 1 — Run this on your server"))
            } footer: {
                Text(L10n.tr("No API key to copy — the helper reads it straight from Syncthing’s config.xml. Replace /PATH/TO/syncthing with your Syncthing config folder (often ~/.local/state/syncthing or ~/.config/syncthing). If you get a permission error, add -u <uid>:<gid> for the user that owns config.xml."))
            }

            Section {
                Text(L10n.tr("That’s it. As soon as the helper starts, it wakes this iPhone once on its own — VaultSync flips to “Cloud Relay active” the moment that first wake-up arrives, with no change needed. After that, changes from your other devices wake the app in the background. Keep the helper running on a machine that stays on (your server or NAS)."))
                    .font(.subheadline)
            } header: {
                Text(L10n.tr("Step 2 — It activates itself"))
            }

            Section {
                ExternalLinkButton(titleKey: "Full setup guide (Docker Compose, one-command install)", url: DocURL.serverSetupGuide)
                    .font(.subheadline)
            } footer: {
                Text(L10n.tr("Prefer Docker Compose or a guided one-command installer? The full guide covers both."))
            }
        }
        .navigationTitle(L10n.tr("Set Up Your Server"))
        .navigationBarTitleDisplayMode(.inline)
    }

    private var commandBox: some View {
        ScrollView(.horizontal, showsIndicators: true) {
            Text(dockerCommand)
                .font(.system(.caption, design: .monospaced))
                .textSelection(.enabled)
                .padding(10)
        }
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: VaultRadius.control, style: .continuous))
        .accessibilityLabel(L10n.tr("Server setup command"))
        .accessibilityValue(dockerCommand)
    }
}

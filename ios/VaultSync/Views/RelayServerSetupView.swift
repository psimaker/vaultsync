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

    /// Self-contained command a user can paste into their server shell. The
    /// relay URL is pre-filled; only the Syncthing API key is left as a
    /// placeholder because it lives on the user's server, not in the app.
    private var dockerCommand: String {
        """
        docker run -d --name vaultsync-notify --restart unless-stopped \\
          --network host \\
          -e SYNCTHING_API_URL=http://localhost:8384 \\
          -e SYNCTHING_API_KEY=PASTE_YOUR_KEY \\
          -e RELAY_URL=\(RelayService.relayURL) \\
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
                Text(L10n.tr("Open the Syncthing web UI on your server, then Actions → Settings → GUI → API Key, and copy the key."))
                    .font(.subheadline)
            } header: {
                Text(L10n.tr("Step 1 — Get your Syncthing API key"))
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
                Text(L10n.tr("Step 2 — Run this on your server"))
            } footer: {
                Text(L10n.tr("Replace PASTE_YOUR_KEY with the API key from Step 1. If Syncthing runs in another container, set SYNCTHING_API_URL to its address instead of localhost."))
            }

            Section {
                Text(L10n.tr("That’s it. As soon as the helper starts, it wakes this iPhone once on its own — VaultSync flips to “Cloud Relay active” automatically, with no change needed. After that, every edit on your other devices arrives instantly. Keep the helper running on a machine that stays on (your server or NAS)."))
                    .font(.subheadline)
            } header: {
                Text(L10n.tr("Step 3 — It activates itself"))
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

import SwiftUI

/// Static URLs used in multiple UI surfaces. Centralizing them removes
/// force-unwrapped string literals from view code.
enum DocURL {
    static let privacyPolicy = URL(string: "https://github.com/psimaker/vaultsync/blob/main/PRIVACY.md")!
    static let termsOfUse = URL(string: "https://github.com/psimaker/vaultsync/blob/main/TERMS.md")!
    static let syncthingIgnoring = URL(string: "https://docs.syncthing.net/users/ignoring.html")!
    static let serverSetupGuide = URL(string: "https://github.com/psimaker/vaultsync/blob/main/notify/README.md")!
    /// Desktop-side steps for offering a folder to this iPhone — the one
    /// onboarding step that happens on another machine (#69).
    static let desktopShareHelp = URL(string: "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md#no-pending-shares-appear")!
}

/// A link-styled button that opens a URL externally without inheriting the
/// row-wide tap behavior of a sibling `NavigationLink`. Use instead of `Link`
/// inside `List`/`Form` rows that also contain navigation or action controls.
struct ExternalLinkButton: View {
    let titleKey: LocalizedStringKey
    let url: URL

    @Environment(\.openURL) private var openURL

    var body: some View {
        Button {
            openURL(url)
        } label: {
            HStack(spacing: 3) {
                Text(titleKey)
                Image(systemName: "arrow.up.right")
                    .imageScale(.small)
                    .accessibilityHidden(true)
            }
            .contentShape(Rectangle())
            .padding(.vertical, 4)
        }
        .buttonStyle(.borderless)
    }
}

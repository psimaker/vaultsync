import Foundation

/// The title of a vault's Location row: the place the vault syncs in, named
/// the way the Files app places it (#307). Pure, so every path form is
/// unit-testable.
///
/// An app's own folder is `Documents` on disk — the form in which Obsidian's
/// folder reaches the app (#270) — but the Files app lists it under the app's
/// name, "On My iPhone" or "iCloud Drive" → the app. The path cannot tell
/// which app the folder belongs to, so the title names it "App Folder" for
/// any app, never "Documents" and never "Obsidian": a folder the user made
/// and named "Obsidian" is a place Obsidian never lists, and it keeps its own
/// name, so the two never read alike.
enum VaultLocationTitle {

    /// `path` — the vault's directory; `base` — the connected Obsidian folder.
    /// Both canonical.
    static func title(path: String, base: String?) -> String {
        if let (place, relative) = appFolderPlace(of: path) {
            return (place + relative).joined(separator: " › ")
        }
        if let base, path == base || path.hasPrefix(base + "/") {
            let relative = path.dropFirst(base.count).split(separator: "/").map(String.init)
            return ([(base as NSString).lastPathComponent] + relative).joined(separator: " › ")
        }
        return (path as NSString).lastPathComponent
    }

    /// The app's own folder at or above `path`, as the Files app places it,
    /// and the levels below it. Nil when there is none.
    private static func appFolderPlace(of path: String) -> (place: [String], relative: [String])? {
        let parts = (path as NSString).pathComponents.filter { $0 != "/" }
        // The outermost one is the place Files shows; anything below it that
        // happens to look like an app's folder is a folder inside it.
        for end in stride(from: 1, through: parts.count, by: 1) {
            let prefix = Array(parts[..<end])
            guard VaultManager.isAppDocumentsRoot("/" + prefix.joined(separator: "/")) else { continue }
            let storage = prefix[prefix.count - 3] == "Application"
                ? L10n.tr("On My iPhone")
                : L10n.tr("iCloud Drive")
            return ([storage, L10n.tr("App Folder")], Array(parts[end...]))
        }
        return nil
    }
}

import Foundation

struct SyncEventItem: Identifiable, Hashable, Sendable {
    enum Kind: String, Sendable {
        case scanStarted
        case scanCompleted
        case syncStarted
        case syncCompleted
        case fileSynced
        case deviceConnected
        case deviceDisconnected
        case folderError
        case fileError
        case summary
    }

    let id: Int
    let kind: Kind
    let date: Date
    let title: String
    let detail: String
    let folderID: String?
    let deviceID: String?
    let filePath: String?

    var isError: Bool {
        switch kind {
        case .folderError, .fileError, .deviceDisconnected:
            return true
        default:
            return false
        }
    }

    var symbolName: String {
        switch kind {
        case .scanStarted:
            return "magnifyingglass.circle.fill"
        case .scanCompleted:
            return "magnifyingglass.circle"
        case .syncStarted:
            return "arrow.triangle.2.circlepath.circle.fill"
        case .syncCompleted:
            return "checkmark.circle.fill"
        case .fileSynced:
            return "doc.badge.arrow.up"
        case .deviceConnected:
            return "link.circle.fill"
        case .deviceDisconnected:
            return "link.circle"
        case .folderError, .fileError:
            return "exclamationmark.triangle.fill"
        case .summary:
            return "ellipsis.circle"
        }
    }
}

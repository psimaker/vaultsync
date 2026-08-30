/// Pure generation gate for a filter scan whose synchronous bridge work may
/// continue after its Swift task is cancelled (#150).
///
/// The detached scanner is deliberately outside this type. Callers may commit
/// its value only when `complete` returns `.commit`; stale generations never
/// change the retry state of a newer scan.
struct FilterScanTaskID: Equatable, Sendable {
    let folderID: String
    let safetyState: ConflictSafetyPolicy.State
}

struct FilterScanGeneration: Sendable {
    struct Token: Equatable, Sendable {
        fileprivate let generation: UInt64
        fileprivate let folderID: String
    }

    enum Completion: Equatable, Sendable {
        case commit
        case retry
        case stale
    }

    private var generation: UInt64 = 0
    private var scanFolderID: String?
    private(set) var activeToken: Token?
    private(set) var needsScan = true

    mutating func begin(
        folderID: String,
        safetyState: ConflictSafetyPolicy.State
    ) -> Token? {
        if scanFolderID != folderID {
            invalidate()
            scanFolderID = folderID
        }
        guard safetyState == .clear else {
            invalidate()
            return nil
        }
        // A newly attached task must supersede unfinished detached work. Once
        // a scan commits, no active token and `needsScan == false` suppresses
        // needless rescans for the same clear folder.
        guard needsScan || activeToken != nil else { return nil }

        generation &+= 1
        let token = Token(generation: generation, folderID: folderID)
        activeToken = token
        needsScan = false
        return token
    }

    mutating func invalidate() {
        generation &+= 1
        activeToken = nil
        needsScan = true
    }

    mutating func complete(
        token: Token,
        currentFolderID: String,
        currentSafetyState: ConflictSafetyPolicy.State,
        taskCancelled: Bool,
        scanComplete: Bool
    ) -> Completion {
        guard activeToken == token else { return .stale }
        guard !taskCancelled,
              token.folderID == currentFolderID,
              currentSafetyState == .clear,
              scanComplete else {
            generation &+= 1
            activeToken = nil
            needsScan = true
            return .retry
        }

        activeToken = nil
        needsScan = false
        return .commit
    }
}

import Foundation
import Testing
@testable import VaultSync

@Suite("PathCollisionGuard — detect & pause vaults merged onto one path (#45)")
struct PathCollisionGuardTests {

    /// Production passes `FolderPathReconciler.canonical`; tests inject identity
    /// so the grouping logic is exercised without touching the filesystem. The
    /// function lowercases internally (case-folding APFS).
    private let identity: (String) -> String = { $0 }

    private func folders(_ pairs: (String, String)...) -> [(id: String, path: String)] {
        pairs.map { (id: $0.0, path: $0.1) }
    }

    // MARK: - Detection

    @Test("Distinct paths never collide")
    func distinctPathsNoCollision() {
        let groups = PathCollisionGuard.collidingFolderGroups(
            folders(("a", "/Obsidian/A"), ("b", "/Obsidian/B")),
            canonicalize: identity
        )
        #expect(groups.isEmpty)
    }

    @Test("A path held by a single folder is not a collision (no false positives)")
    func singleFolderPerPathNoCollision() {
        let groups = PathCollisionGuard.collidingFolderGroups(
            folders(("a", "/Obsidian")),
            canonicalize: identity
        )
        #expect(groups.isEmpty)
    }

    @Test("Two folders on one path form a single collision group")
    func twoFoldersOnePathCollide() {
        let groups = PathCollisionGuard.collidingFolderGroups(
            folders(("a", "/Obsidian"), ("b", "/Obsidian")),
            canonicalize: identity
        )
        #expect(groups.count == 1)
        #expect(groups.first == Set(["a", "b"]))
    }

    @Test("Collision detection is case-insensitive (case-folding APFS)")
    func collisionIsCaseInsensitive() {
        // Same rule the accept-time guard uses: canonical, then lowercased.
        let groups = PathCollisionGuard.collidingFolderGroups(
            folders(("a", "/Obsidian/Vault"), ("b", "/obsidian/vault")),
            canonicalize: identity
        )
        #expect(groups.count == 1)
        #expect(groups.first == Set(["a", "b"]))
    }

    @Test("Canonicalization is applied before comparing")
    func canonicalizationIsApplied() {
        // Inject a canonicalizer that drops a trailing slash; the two paths then
        // resolve to the same directory and must collide.
        let dropTrailingSlash: (String) -> String = { $0.hasSuffix("/") ? String($0.dropLast()) : $0 }
        let groups = PathCollisionGuard.collidingFolderGroups(
            folders(("a", "/Obsidian/"), ("b", "/Obsidian")),
            canonicalize: dropTrailingSlash
        )
        #expect(groups.count == 1)
        #expect(groups.first == Set(["a", "b"]))
    }

    @Test("Only the shared path is flagged; the distinct folder is left out")
    func mixedSetFlagsOnlyTheCollision() {
        let groups = PathCollisionGuard.collidingFolderGroups(
            folders(("a", "/Obsidian"), ("b", "/Obsidian"), ("c", "/Obsidian/Other")),
            canonicalize: identity
        )
        #expect(groups.count == 1)
        #expect(groups.first == Set(["a", "b"]))
    }

    @Test("collidingFolderIDs flattens every collision group")
    func collidingIDsFlattensGroups() {
        let ids = PathCollisionGuard.collidingFolderIDs(
            folders(("a", "/X"), ("b", "/X"), ("c", "/Y"), ("d", "/Y"), ("e", "/Z")),
            canonicalize: identity
        )
        #expect(ids == Set(["a", "b", "c", "d"]))
    }

    // MARK: - Pause-once core

    /// In-memory backing for an injected `Environment`, recording every pause and
    /// mark call. Mirrors `FolderPathReconcilerTests.Spy`.
    final class Spy {
        var autoPaused: Set<String>
        var setPausedCalls: [String] = []
        var markCalls: [String] = []
        /// IDs for which `setPaused` should fail (returns an error message).
        var failPauseFor: Set<String>

        init(autoPaused: Set<String> = [], failPauseFor: Set<String> = []) {
            self.autoPaused = autoPaused
            self.failPauseFor = failPauseFor
        }
    }

    private func makeEnv(
        spy: Spy,
        canonicalize: @escaping (String) -> String = { $0 }
    ) -> PathCollisionGuard.Environment {
        PathCollisionGuard.Environment(
            canonicalize: canonicalize,
            loadAutoPaused: { spy.autoPaused },
            markAutoPaused: { id in
                spy.markCalls.append(id)
                spy.autoPaused.insert(id)
            },
            setPaused: { id in
                spy.setPausedCalls.append(id)
                return spy.failPauseFor.contains(id) ? "pause failed" : nil
            }
        )
    }

    private func folders(_ triples: (String, String, Bool)...) -> [(id: String, path: String, paused: Bool)] {
        triples.map { (id: $0.0, path: $0.1, paused: $0.2) }
    }

    @Test("Both colliding folders are paused once and recorded")
    func pausesBothCollidingFolders() {
        let spy = Spy()
        let newly = PathCollisionGuard.pauseCollisions(
            folders: folders(("a", "/Obsidian", false), ("b", "/Obsidian", false)),
            env: makeEnv(spy: spy)
        )
        #expect(newly == ["a", "b"])
        #expect(spy.setPausedCalls == ["a", "b"])
        #expect(spy.markCalls == ["a", "b"])
    }

    @Test("An already-paused colliding folder is recorded but not paused again")
    func alreadyPausedIsRecordedNotRepaused() {
        let spy = Spy()
        let newly = PathCollisionGuard.pauseCollisions(
            folders: folders(("a", "/Obsidian", true), ("b", "/Obsidian", false)),
            env: makeEnv(spy: spy)
        )
        #expect(newly == ["b"])
        #expect(spy.setPausedCalls == ["b"])        // A was already paused — no bridge call
        #expect(spy.markCalls == ["a", "b"])        // but A is still recorded as handled
    }

    @Test("A folder we auto-paused before is never touched again (deliberate resume is respected)")
    func sidecaredFolderIsNotRepaused() {
        let spy = Spy(autoPaused: ["a"])            // user resumed A after we paused it
        let newly = PathCollisionGuard.pauseCollisions(
            folders: folders(("a", "/Obsidian", false), ("b", "/Obsidian", false)),
            env: makeEnv(spy: spy)
        )
        #expect(newly == ["b"])
        #expect(spy.setPausedCalls == ["b"])        // A is left alone — not fought
        #expect(spy.markCalls == ["b"])
    }

    @Test("No collision pauses nothing")
    func noCollisionNoPause() {
        let spy = Spy()
        let newly = PathCollisionGuard.pauseCollisions(
            folders: folders(("a", "/Obsidian/A", false), ("b", "/Obsidian/B", false)),
            env: makeEnv(spy: spy)
        )
        #expect(newly.isEmpty)
        #expect(spy.setPausedCalls.isEmpty)
        #expect(spy.markCalls.isEmpty)
    }

    @Test("A failed pause is not recorded, so it is retried next launch")
    func failedPauseIsNotRecorded() {
        let spy = Spy(failPauseFor: ["a"])
        let newly = PathCollisionGuard.pauseCollisions(
            folders: folders(("a", "/Obsidian", false), ("b", "/Obsidian", false)),
            env: makeEnv(spy: spy)
        )
        #expect(newly == ["b"])
        #expect(spy.setPausedCalls == ["a", "b"])   // both attempted
        #expect(spy.markCalls == ["b"])             // only the successful one recorded
        #expect(!spy.autoPaused.contains("a"))      // A stays unrecorded → retried
    }

    @Test("Every collision group is paused, distinct folders are untouched")
    func pausesAcrossMultipleGroups() {
        let spy = Spy()
        let newly = PathCollisionGuard.pauseCollisions(
            folders: folders(
                ("a", "/X", false), ("b", "/X", false),
                ("c", "/Y", false), ("d", "/Y", false),
                ("e", "/Z", false)
            ),
            env: makeEnv(spy: spy)
        )
        #expect(newly == ["a", "b", "c", "d"])
        #expect(!spy.setPausedCalls.contains("e"))
        #expect(!spy.markCalls.contains("e"))
    }
}

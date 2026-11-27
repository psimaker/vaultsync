import Foundation
import os
import SQLite3
import Testing
@testable import VaultSync

/// The record of shares set aside with "Ignore for Now" was cut down to every
/// list of offers the app applied, and the bridge answered a stopped engine
/// and a failed read with the empty list: the app forgot every share set
/// aside, and once the list was read again the automatic accept took them
/// (#464, decision 039). The rule: only a list read in full by one running
/// engine, the manager's own, changes the list or the record.
@Suite("Which read of the offered shares is a list (#464)")
struct Issue464PendingFolderReadTests {

    private static let offer = SyncthingManager.PendingFolderInfo(id: "issue464-offer", label: "Set Aside", offeredBy: [])
    private static let offerJSON = #"[{"id":"issue464-offer","label":"Set Aside","offeredBy":[]}]"#

    typealias Case = (before: Int64, json: String, after: Int64, expected: SyncthingManager.PendingFolderRead)

    static let cases: [Case] = [
        // The same engine across the read.
        (7, "[]", 7, .list([])),
        (7, offerJSON, 7, .list([offer])),
        (7, #"{"error":"read pending folder offers: database is closed"}"#, 7, .unreadable),
        (7, "", 7, .unreadable),
        // No engine, or not one engine, across the read — whatever it
        // answered, also the "[]" a bridge before #464 answered.
        (0, #"{"error":"syncthing not running"}"#, 0, .noEngine),
        (0, "[]", 0, .noEngine),
        (7, "[]", 0, .noEngine),
        (7, offerJSON, 0, .noEngine),
        (7, "[]", 8, .noEngine),
        (0, "[]", 8, .noEngine),
    ]

    @Test("Only one running engine's list is the list", arguments: cases)
    func classification(before: Int64, json: String, after: Int64, expected: SyncthingManager.PendingFolderRead) {
        #expect(SyncthingManager.pendingFolderRead(generationBefore: before, json: json, generationAfter: after) == expected)
    }
}

extension EngineBridgeSuites {
    @MainActor
    @Suite("A share set aside is kept when the list of offers cannot be read (#464)")
    struct Issue464IgnoredShareTests {

        private static let removedKey = "syncthing.userRemovedFolderIDs"

        private static func offer(_ id: String, _ label: String = "Set Aside") -> SyncthingManager.PendingFolderInfo {
            SyncthingManager.PendingFolderInfo(id: id, label: label, offeredBy: [])
        }

        /// The shares the automatic pass accepts while `manager` holds them.
        private func automaticallyAccepted(by manager: SyncthingManager) -> [String] {
            var accepted: [String] = []
            let coordinator = ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
                settled: { true },
                vaultAccessible: { true },
                pendingFolders: { manager.pendingFolders },
                autoAcceptEligible: { manager.autoAcceptEligiblePendingFolders },
                accept: { folder, _ in
                    accepted.append(folder.id)
                    return .accepted
                },
                acceptIntoTarget: { _, _ in nil },
                unignorePendingFolder: { _ in },
                ignorePendingFolder: { _ in }
            ))
            coordinator.runAutomaticPass()
            return accepted
        }

        private func waitUntil(seconds: Double = 20, _ condition: () -> Bool) async -> Bool {
            let deadline = Date().addingTimeInterval(seconds)
            while !condition() {
                if Date() >= deadline { return false }
                try? await Task.sleep(for: .milliseconds(100))
            }
            return true
        }

        private func configuredFolderIDs() -> Set<String> {
            guard let data = SyncBridgeService.getFoldersJSON().data(using: .utf8),
                  let folders = try? JSONDecoder().decode([SyncthingManager.FolderInfo].self, from: data) else { return [] }
            return Set(folders.map(\.id))
        }

        /// Writes offers into the stopped engine's database the way the engine
        /// keeps a connected device's announcement of a folder: the key
        /// `folder/<device>/<folder>`, the value an `ObservedFolder` with its
        /// label (field 2). The test host has no peer to announce them; the
        /// engine lists them only for a device it knows.
        private static func recordOffers(_ offers: [(id: String, label: String)], from device: String) throws {
            let path = TestSupport.syncthingConfigPath() + "/data/index-v2/main.db"
            try #require(FileManager.default.fileExists(atPath: path), "the engine's database is not where the bridge keeps it")
            var db: OpaquePointer?
            try #require(sqlite3_open_v2(path, &db, SQLITE_OPEN_READWRITE, nil) == SQLITE_OK)
            defer { sqlite3_close(db) }
            let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)
            for offer in offers {
                let label = Array(offer.label.utf8)
                try #require(label.count < 128)
                let value: [UInt8] = [0x12, UInt8(label.count)] + label
                var statement: OpaquePointer?
                try #require(sqlite3_prepare_v2(db, "INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)", -1, &statement, nil) == SQLITE_OK)
                defer { sqlite3_finalize(statement) }
                sqlite3_bind_text(statement, 1, "folder/\(device)/\(offer.id)", -1, transient)
                _ = value.withUnsafeBytes { bytes in
                    sqlite3_bind_blob(statement, 2, bytes.baseAddress, Int32(bytes.count), transient)
                }
                try #require(sqlite3_step(statement) == SQLITE_DONE)
            }
        }

        @Test("An unreadable list and a read no engine answered change neither the list, nor the shares set aside, nor the removed ones")
        func failedReadsChangeNothing() {
            TestSupport.resetSyncthingState()
            defer { TestSupport.resetSyncthingState() }
            let removedDefaults = TestSupport.makeIsolatedDefaults(label: "issue464")
            let manager = SyncthingManager()
            manager._testUseRemoval(bridge: { _ in nil }, defaults: removedDefaults)

            let setAside = Self.offer("issue464-\(UUID().uuidString)")
            let removed = Self.offer("issue464-removed-\(UUID().uuidString)", "Removed")
            #expect(manager.removeFolder(id: removed.id) == nil)
            manager._testSetPendingFolders([setAside, removed])
            manager.ignorePendingFolder(id: setAside.id)
            let listed = manager.pendingFolders

            for read in [SyncthingManager.PendingFolderRead.noEngine, .unreadable] {
                manager._testApplyPolledPendingRead(read)
                #expect(manager.pendingFolders == listed)
                #expect(manager.ignoredPendingFolderIDs.contains(setAside.id))
                #expect(manager.userRemovedFolderIDs.contains(removed.id))
                #expect(removedDefaults.stringArray(forKey: Self.removedKey)?.contains(removed.id) == true)
                #expect(automaticallyAccepted(by: manager).isEmpty)
            }
            #expect(manager.pendingFoldersUnreadable)

            // A new process reads the record as it was left.
            let next = SyncthingManager()
            next._testSetPendingFolders([setAside])
            #expect(next.ignoredPendingFolderIDs.contains(setAside.id))
            #expect(automaticallyAccepted(by: next).isEmpty)

            // The next list with the share keeps it set aside and ends the
            // unreadable state; only a list without it cuts the record.
            manager._testApplyPolledPendingRead(.list([setAside, removed]))
            #expect(!manager.pendingFoldersUnreadable)
            #expect(manager.ignoredPendingFolderIDs.contains(setAside.id))
            #expect(automaticallyAccepted(by: manager).isEmpty)
            manager._testApplyPolledPendingRead(.list([removed]))
            #expect(!manager.ignoredPendingFolderIDs.contains(setAside.id))
        }

        @Test("A read that began before a stop or a reset is not applied after it")
        func readFromBeforeAStopIsNotApplied() {
            TestSupport.resetSyncthingState()
            defer { TestSupport.resetSyncthingState() }
            let manager = SyncthingManager()
            let setAside = Self.offer("issue464-\(UUID().uuidString)")
            manager._testSetPendingFolders([setAside])
            manager.ignorePendingFolder(id: setAside.id)

            let beforeStop = manager._testEngineAttachment()
            manager.stop()
            manager._testApplyPolledPendingRead(.list([]), attachment: beforeStop)
            manager._testApplyPolledPendingRead(.unreadable, attachment: beforeStop)
            #expect(manager.ignoredPendingFolderIDs.contains(setAside.id))
            #expect(manager.pendingFolders.isEmpty)
            #expect(!manager.pendingFoldersUnreadable)

            manager._testSetPendingFolders([setAside])
            let beforeReset = manager._testEngineAttachment()
            manager.resetForRestart()
            manager._testApplyPolledPendingRead(.list([]), attachment: beforeReset)
            #expect(manager.ignoredPendingFolderIDs.contains(setAside.id))
        }

        @Test("The setup checklist does not say that no share is offered while the offers cannot be read")
        func checklistSaysTheListCouldNotBeRead() {
            TestSupport.resetSyncthingState()
            TestSupport.resetRelayState()
            defer { TestSupport.resetSyncthingState() }
            let manager = SyncthingManager()
            let viewModel = SetupChecklistViewModel(
                syncthingManager: manager,
                vaultManager: VaultManager(),
                subscriptionManager: SubscriptionManager()
            )
            manager._testApplyPolledPendingRead(.unreadable)
            let item = viewModel.items.first { $0.requirement == .firstShareDetectedOrAccepted }
            #expect(item?.isComplete == false)
            #expect(item?.description == SyncthingManager.pendingFoldersUnreadableNote)
            #expect(item?.remediation.contains("Share your Obsidian vault") != true)
        }

        @Test("refreshPendingFolders: an engine that stops during the read, and a failed read, keep the share set aside; a running engine's list cuts it")
        func refreshKeepsTheRecordOnAFailedRead() throws {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.releaseForeground() }
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            let sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue464-\(UUID().uuidString)", isDirectory: true)
            let root = sandbox.appendingPathComponent("root", isDirectory: true)
            defer {
                TestSupport.resetSyncthingState()
                try? FileManager.default.removeItem(at: sandbox)
            }
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)

            // Not attached: no poll reads the offers, only the refresh after
            // each accept does.
            let manager = SyncthingManager()
            let setAside = Self.offer("issue464-\(UUID().uuidString)")
            manager._testSetPendingFolders([setAside])
            manager.ignorePendingFolder(id: setAside.id)
            var answer: () -> String = { "[]" }
            manager._testUsePendingRead { answer() }

            func acceptAnother() -> String? {
                let id = "issue464-other-\(UUID().uuidString)"
                return manager.acceptPendingFolder(
                    folderID: id,
                    label: id,
                    path: root.appendingPathComponent(id, isDirectory: true).path,
                    root: root.path,
                    allowNonEmpty: false
                )
            }

            // A failed read of a running engine.
            answer = { #"{"error":"read pending folder offers: database is closed"}"# }
            #expect(acceptAnother() == nil)
            #expect(manager.ignoredPendingFolderIDs.contains(setAside.id))
            #expect(manager.pendingFolders.map(\.id) == [setAside.id])
            #expect(manager.pendingFoldersUnreadable)

            // The engine stops between the accept and the read — and answers
            // what a bridge before #464 answered then.
            answer = {
                _ = SyncBridgeService.stopSyncthing()
                return "[]"
            }
            #expect(acceptAnother() == nil)
            #expect(!SyncBridgeService.isRunning())
            #expect(manager.ignoredPendingFolderIDs.contains(setAside.id))
            #expect(manager.pendingFolders.map(\.id) == [setAside.id])
            #expect(automaticallyAccepted(by: manager).isEmpty)

            // A running engine that lists no such share: the record is cut.
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            answer = { "[]" }
            #expect(acceptAnother() == nil)
            #expect(!manager.ignoredPendingFolderIDs.contains(setAside.id))
            #expect(!manager.pendingFoldersUnreadable)
        }

        @Test("A running engine's list without the share cuts the record, through the poll and through refreshPendingFolders")
        func runningEnginesListCutsTheRecord() async throws {
            TestSupport.resetSyncthingState()
            let manager = SyncthingManager()
            await manager.start()
            let sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue464-\(UUID().uuidString)", isDirectory: true)
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
                try? FileManager.default.removeItem(at: sandbox)
            }
            #expect(manager.isRunning)

            let offer = Self.offer("issue464-\(UUID().uuidString)")

            // Through refreshPendingFolders: an accept of another share reads
            // the offers right after it, on this actor, with no poll between.
            manager._testSetPendingFolders([offer])
            manager.ignorePendingFolder(id: offer.id)
            #expect(manager.ignoredPendingFolderIDs.contains(offer.id))
            let root = sandbox.appendingPathComponent("root", isDirectory: true)
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            let other = "issue464-other-\(UUID().uuidString)"
            #expect(manager.acceptPendingFolder(
                folderID: other,
                label: "Other",
                path: root.appendingPathComponent("Other", isDirectory: true).path,
                root: root.path,
                allowNonEmpty: false
            ) == nil)
            #expect(!manager.ignoredPendingFolderIDs.contains(offer.id), "refreshPendingFolders cut the record")
            manager._testSetPendingFolders([offer])
            #expect(automaticallyAccepted(by: manager) == [offer.id])

            // Through the poll.
            manager.ignorePendingFolder(id: offer.id)
            #expect(manager.ignoredPendingFolderIDs.contains(offer.id))
            #expect(await waitUntil(seconds: 10) { !manager.ignoredPendingFolderIDs.contains(offer.id) }, "the poll cut the record")
            manager._testSetPendingFolders([offer])
            #expect(automaticallyAccepted(by: manager) == [offer.id])
        }

        /// The whole chain as a user meets it: a share is set aside; the
        /// scene goes to the background and the poll goes on; a background
        /// run ends and stops the engine while a poll is between its check
        /// and its reads; the poll applies what it read; back in the
        /// foreground the engine starts again, the paths settle and the
        /// automatic pass runs over the shares the engine offers. The offers
        /// are the engine's own, read from its database; the app reads the
        /// real bridge throughout. The one hook only times the stop.
        @Test("A background run's stop between the poll's check and its reads forgets no share set aside, and the automatic pass leaves it alone")
        func backgroundStopBetweenThePollsCheckAndItsReads() async throws {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.releaseForeground() }
            let device = TestSupport.samplePeerDeviceID
            let setAside = "issue464-aside-\(UUID().uuidString)"
            let wanted = "issue464-wanted-\(UUID().uuidString)"
            let sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue464-\(UUID().uuidString)", isDirectory: true)
            // An app's own folder, as the first folder an install connects (#270).
            let root = sandbox
                .appendingPathComponent("Containers/Data/Application/\(UUID().uuidString)/Documents", isDirectory: true)
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            let manager = SyncthingManager()
            defer {
                SyncthingManager._testAfterPollCheck.withLock { $0 = nil }
                manager.stop()
                TestSupport.resetSyncthingState()
                FolderPathReconciler.removeRel(forFolder: wanted)
                try? FileManager.default.removeItem(at: sandbox)
            }

            // The engine knows the device and holds its offers of two vaults.
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            #expect(SyncBridgeService.addDevice(deviceID: device, name: "Peer") == nil)
            _ = SyncBridgeService.stopSyncthing()
            try Self.recordOffers([(setAside, "Set Aside"), (wanted, "Wanted")], from: device)

            await manager.start()
            try #require(manager.isRunning)
            try #require(await waitUntil { Set(manager.pendingFolders.map(\.id)) == [setAside, wanted] },
                         "the engine lists the offers it holds")
            manager.ignorePendingFolder(id: setAside)

            // The scene goes to the background: the lifecycle is released and
            // the poll goes on. A background run ends and stops the engine
            // while a poll is between its check and its reads.
            BackgroundSyncService.releaseForegroundLifecycleLock()
            let stops = OSAllocatedUnfairLock(initialState: 0)
            SyncthingManager._testAfterPollCheck.withLock {
                $0 = {
                    let first = stops.withLock { count in
                        count += 1
                        return count == 1
                    }
                    if first {
                        BackgroundSyncService.stopEngineIfBackgroundOwned(context: "Issue464 test")
                    }
                }
            }
            // The poll applies what it read; the next one finds the engine
            // gone and detaches, as without the foreground it does not restart.
            try #require(await waitUntil(seconds: 30) { !manager.isRunning })
            SyncthingManager._testAfterPollCheck.withLock { $0 = nil }
            #expect(stops.withLock { $0 } >= 1)
            #expect(manager.ignoredPendingFolderIDs.contains(setAside))

            // Back in the foreground: the engine starts again, the paths
            // settle, the offers are read again.
            await manager.start()
            try #require(manager.isRunning)
            await manager.reconcileFolderPaths(obsidianRoot: root.path).value
            try #require(manager.pathSettlement.settled)
            try #require(await waitUntil { Set(manager.pendingFolders.map(\.id)) == [setAside, wanted] })

            let vaultManager = VaultManager()
            vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: root)
            let coordinator = ShareAcceptCoordinator(environment: .live(syncthingManager: manager, vaultManager: vaultManager))
            coordinator.runAutomaticPass()
            #expect(coordinator.pendingShareFailures.isEmpty, "\(coordinator.pendingShareFailures.mapValues(\.title))")

            let configured = configuredFolderIDs()
            #expect(configured.contains(wanted), "the pass ran: the share not set aside was accepted")
            #expect(!configured.contains(setAside), "the share set aside was accepted by itself")
            #expect(manager.ignoredPendingFolderIDs.contains(setAside))
        }
    }
}

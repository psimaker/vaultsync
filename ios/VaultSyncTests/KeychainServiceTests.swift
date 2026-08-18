import Foundation
import Security
import Testing
@testable import VaultSync

@Suite("Keychain update preservation (#148)")
struct KeychainServiceTests {
    @Test("An update failure preserves the existing credential and never deletes")
    func issue148UpdateFailurePreservesExistingCredential() {
        let spy = SecuritySpy(storedData: Data("old-value".utf8))
        spy.updateStatuses = [errSecInteractionNotAllowed]

        let stored = KeychainService.set(
            key: "credential",
            value: "replacement-value",
            environment: spy.environment()
        )

        #expect(!stored)
        #expect(
            KeychainService.get(key: "credential", environment: spy.environment())
                == "old-value"
        )
        #expect(spy.operations == [.update, .copy])
        #expect(!spy.operations.contains(.delete))
    }

    @Test("An existing item is updated using value-only attributes")
    func issue148ExistingItemUpdatesValueOnly() {
        let spy = SecuritySpy(storedData: Data("old-value".utf8))

        let stored = KeychainService.set(
            key: "credential",
            value: "replacement-value",
            environment: spy.environment()
        )

        #expect(stored)
        #expect(spy.storedData == Data("replacement-value".utf8))
        #expect(spy.operations == [.update])
        #expect(Set(spy.updateQueries[0].keys) == Set([
            kSecClass as String,
            kSecAttrService as String,
            kSecAttrAccount as String,
        ]))
        #expect(Set(spy.updateAttributes[0].keys) == Set([kSecValueData as String]))
        #expect(spy.updateAttributes[0][kSecAttrAccessible as String] == nil)
    }

    @Test("A confirmed missing item is added with the existing accessibility policy")
    func issue148NotFoundAddsNewItem() {
        let spy = SecuritySpy()
        spy.updateStatuses = [errSecItemNotFound]
        spy.addStatuses = [errSecSuccess]

        let stored = KeychainService.set(
            key: "credential",
            value: "new-value",
            environment: spy.environment()
        )

        #expect(stored)
        #expect(spy.storedData == Data("new-value".utf8))
        #expect(spy.operations == [.update, .add])
        let insertion = spy.addAttributes[0]
        #expect(insertion[kSecAttrService as String] as? String == "eu.vaultsync.app")
        #expect(insertion[kSecAttrAccount as String] as? String == "credential")
        #expect(
            insertion[kSecAttrAccessible as String] as? String
                == kSecAttrAccessibleAfterFirstUnlock as String
        )
    }

    @Test("An add failure returns failure without a delete")
    func issue148AddFailureDoesNotDelete() {
        let spy = SecuritySpy()
        spy.updateStatuses = [errSecItemNotFound]
        spy.addStatuses = [errSecNotAvailable]

        let stored = KeychainService.set(
            key: "credential",
            value: "new-value",
            environment: spy.environment()
        )

        #expect(!stored)
        #expect(spy.storedData == nil)
        #expect(spy.operations == [.update, .add])
        #expect(!spy.operations.contains(.delete))
    }

    @Test("A duplicate add race retries update once without deleting")
    func issue148DuplicateRaceRetriesUpdateOnce() {
        let spy = SecuritySpy()
        spy.updateStatuses = [errSecItemNotFound, errSecSuccess]
        spy.addStatuses = [errSecDuplicateItem]
        spy.duplicateRaceData = Data("competing-value".utf8)

        let stored = KeychainService.set(
            key: "credential",
            value: "replacement-value",
            environment: spy.environment()
        )

        #expect(stored)
        #expect(spy.storedData == Data("replacement-value".utf8))
        #expect(spy.operations == [.update, .add, .update])
        #expect(!spy.operations.contains(.delete))
    }

    @Test("A failed duplicate update retry preserves the competing valid value")
    func issue148DuplicateRetryFailurePreservesCredential() {
        let spy = SecuritySpy()
        spy.updateStatuses = [errSecItemNotFound, errSecInteractionNotAllowed]
        spy.addStatuses = [errSecDuplicateItem]
        spy.duplicateRaceData = Data("competing-value".utf8)

        let stored = KeychainService.set(
            key: "credential",
            value: "replacement-value",
            environment: spy.environment()
        )

        #expect(!stored)
        #expect(spy.storedData == Data("competing-value".utf8))
        #expect(spy.operations == [.update, .add, .update])
        #expect(!spy.operations.contains(.delete))
    }

    @Test("Encoding failure performs no Security operation")
    func issue148EncodingFailureDoesNotMutateKeychain() {
        let spy = SecuritySpy(storedData: Data("old-value".utf8))

        let stored = KeychainService.set(
            key: "credential",
            value: "replacement-value",
            environment: spy.environment(encodeUTF8: { _ in nil })
        )

        #expect(!stored)
        #expect(spy.storedData == Data("old-value".utf8))
        #expect(spy.operations.isEmpty)
    }

    @Test("Corrupt stored UTF-8 is rejected without mutation")
    func issue148CorruptReadDoesNotMutateKeychain() {
        let corrupt = Data([0xFF, 0xFE])
        let spy = SecuritySpy(storedData: corrupt)

        let result = KeychainService.read(
            key: "credential",
            environment: spy.environment()
        )

        #expect(result == .corrupt)
        #expect(spy.storedData == corrupt)
        #expect(spy.operations == [.copy])
    }

    private final class SecuritySpy {
        enum Operation: Equatable {
            case copy
            case update
            case add
            case delete
        }

        var storedData: Data?
        var operations: [Operation] = []
        var updateStatuses: [OSStatus] = []
        var addStatuses: [OSStatus] = []
        var duplicateRaceData: Data?
        var updateQueries: [[String: Any]] = []
        var updateAttributes: [[String: Any]] = []
        var addAttributes: [[String: Any]] = []

        init(storedData: Data? = nil) {
            self.storedData = storedData
        }

        func environment(
            encodeUTF8: @escaping (String) -> Data? = { $0.data(using: .utf8) }
        ) -> KeychainService.Environment {
            KeychainService.Environment(
                encodeUTF8: encodeUTF8,
                copyMatching: { query, result in
                    self.operations.append(.copy)
                    guard let storedData = self.storedData else {
                        return errSecItemNotFound
                    }
                    result = storedData as NSData
                    return errSecSuccess
                },
                update: { query, attributes in
                    self.operations.append(.update)
                    let queryDictionary = self.dictionary(query)
                    let attributeDictionary = self.dictionary(attributes)
                    self.updateQueries.append(queryDictionary)
                    self.updateAttributes.append(attributeDictionary)
                    let status = self.updateStatuses.isEmpty
                        ? (self.storedData == nil ? errSecItemNotFound : errSecSuccess)
                        : self.updateStatuses.removeFirst()
                    if status == errSecSuccess,
                       let data = attributeDictionary[kSecValueData as String] as? Data {
                        self.storedData = data
                    }
                    return status
                },
                add: { attributes in
                    self.operations.append(.add)
                    let attributeDictionary = self.dictionary(attributes)
                    self.addAttributes.append(attributeDictionary)
                    let status = self.addStatuses.isEmpty
                        ? (self.storedData == nil ? errSecSuccess : errSecDuplicateItem)
                        : self.addStatuses.removeFirst()
                    if status == errSecSuccess,
                       let data = attributeDictionary[kSecValueData as String] as? Data {
                        self.storedData = data
                    } else if status == errSecDuplicateItem,
                              let duplicateRaceData = self.duplicateRaceData {
                        self.storedData = duplicateRaceData
                    }
                    return status
                },
                delete: { _ in
                    self.operations.append(.delete)
                    let existed = self.storedData != nil
                    self.storedData = nil
                    return existed ? errSecSuccess : errSecItemNotFound
                }
            )
        }

        private func dictionary(_ value: CFDictionary) -> [String: Any] {
            value as NSDictionary as? [String: Any] ?? [:]
        }
    }
}

@MainActor
@Suite("APNs credential persistence (#148)")
struct APNsDeviceTokenRegistrationTests {
    @Test("Persistence failure has no registration success or token-change effects")
    func issue148APNsPersistenceFailureHasNoFalseSuccess() {
        var attemptedTokens: [String] = []
        var registeredCount = 0
        var failedReasons: [String] = []
        var notificationCount = 0
        var logEvents: [APNsDeviceTokenRegistration.LogEvent] = []

        let stored = APNsDeviceTokenRegistration.handle(
            token: "replacement-token",
            failureReason: "not ready",
            environment: APNsDeviceTokenRegistration.Environment(
                loadPreviousToken: { "old-token" },
                persistToken: { token in
                    attemptedTokens.append(token)
                    return false
                },
                markRegistered: { registeredCount += 1 },
                markFailed: { failedReasons.append($0) },
                postTokenDidChange: { notificationCount += 1 },
                log: { logEvents.append($0) }
            )
        )

        #expect(!stored)
        #expect(attemptedTokens == ["replacement-token"])
        #expect(registeredCount == 0)
        #expect(failedReasons == ["not ready"])
        #expect(notificationCount == 0)
        #expect(logEvents == [.persistenceFailed])
    }

    @Test("A stored changed token marks success and notifies exactly once")
    func issue148StoredAPNsChangeNotifiesAfterSuccess() {
        var registeredCount = 0
        var notificationCount = 0
        var logEvents: [APNsDeviceTokenRegistration.LogEvent] = []

        let stored = APNsDeviceTokenRegistration.handle(
            token: "replacement-token",
            failureReason: "not ready",
            environment: APNsDeviceTokenRegistration.Environment(
                loadPreviousToken: { "old-token" },
                persistToken: { _ in true },
                markRegistered: { registeredCount += 1 },
                markFailed: { _ in Issue.record("success must not mark APNs failed") },
                postTokenDidChange: { notificationCount += 1 },
                log: { logEvents.append($0) }
            )
        )

        #expect(stored)
        #expect(registeredCount == 1)
        #expect(notificationCount == 1)
        #expect(logEvents == [.stored, .changed(first: false)])
    }
}

@MainActor
@Suite("Relay device ID persistence (#148)")
struct RelayDeviceIDStorageTests {
    @Test("Encoding failure never calls the Keychain writer")
    func issue148RelayEncodingFailureSkipsKeychain() {
        var writeCalls: [String] = []
        let outcome = RelayDeviceIDStorage.persist(
            ["device-b"],
            environment: RelayDeviceIDStorage.Environment(
                load: { .loaded(["device-a"]) },
                encode: { _ in throw InjectedError.encoding },
                write: { value in
                    writeCalls.append(value)
                    return true
                }
            )
        )

        #expect(outcome == .failed(.encoding))
        #expect(writeCalls.isEmpty)
    }

    @Test("Successful persistence merges, deduplicates, and sorts device IDs")
    func issue148RelayPersistenceStoresMergedIDs() throws {
        var writtenValue: String?
        let outcome = RelayDeviceIDStorage.persist(
            ["device-b", "device-a"],
            environment: RelayDeviceIDStorage.Environment(
                load: { .loaded(["device-c", "device-b"]) },
                encode: { ids in
                    String(decoding: try JSONEncoder().encode(ids), as: UTF8.self)
                },
                write: { value in
                    writtenValue = value
                    return true
                }
            )
        )

        let persistedIDs: Set<String> = ["device-a", "device-b", "device-c"]
        #expect(outcome == .stored(persistedIDs))
        let value = try #require(writtenValue)
        let decoded = try JSONDecoder().decode([String].self, from: Data(value.utf8))
        #expect(decoded == ["device-a", "device-b", "device-c"])
    }

    @Test("A failed read cannot encode or overwrite the stored payload")
    func issue148RelayReadFailureSkipsMutation() {
        var encodeCount = 0
        var writeCount = 0
        let outcome = RelayDeviceIDStorage.persist(
            ["device-a"],
            environment: RelayDeviceIDStorage.Environment(
                load: { .failed },
                encode: { _ in
                    encodeCount += 1
                    return "encoded"
                },
                write: { _ in
                    writeCount += 1
                    return true
                }
            )
        )

        #expect(outcome == .failed(.read))
        #expect(encodeCount == 0)
        #expect(writeCount == 0)
    }

    @Test("Corrupt JSON storage is rejected instead of treated as empty")
    func issue148CorruptRelayJSONFailsClosed() {
        #expect(RelayDeviceIDStorage.decodeStoredValue("[not-json") == .failed)
    }

    @Test("Every caller context blocks its production target after storage failure")
    func issue148EveryRelayCallerHandlesPersistenceFailure() {
        #expect(RelayDeviceIDStorage.Context.allCases.count == 5)

        for context in RelayDeviceIDStorage.Context.allCases {
            var persistedTargets: [[String]] = []
            var loadCount = 0
            let preparation = RelayDeviceIDStorage.prepareTarget(
                source: .persist(["device-a"], context: context),
                persist: { target in
                    persistedTargets.append(target)
                    return .failed(.keychain)
                },
                load: {
                    loadCount += 1
                    return .loaded(["unexpected"])
                }
            )

            let disposition: RelayDeviceIDStorage.FailureDisposition
            switch context {
            case .pendingPurchase, .manualRetry:
                disposition = .stop
            case .restore:
                disposition = .returnRestoreFailure
            case .diagnosticsRefresh:
                disposition = .continueWithoutProvisioning
            case .verifiedTransaction:
                disposition = .finishTransactionWithoutProvisioning
            }
            #expect(preparation == .skip(disposition))
            #expect(persistedTargets == [["device-a"]])
            #expect(loadCount == 0)
        }
    }

    @Test("Only the exact successfully persisted target is authorized")
    func issue148SuccessfulStorageAuthorizesExactTarget() {
        let preparation = RelayDeviceIDStorage.prepareTarget(
            source: .persist(
                ["device-b", "device-a", "device-b"],
                context: .diagnosticsRefresh
            ),
            persist: { target in .stored(Set(target + ["previous-device"])) },
            load: { .loaded(["unexpected"]) }
        )

        #expect(preparation == .ready(["device-a", "device-b"]))
    }

    @Test("An incomplete stored outcome cannot authorize its target")
    func issue148IncompleteStoredOutcomeFailsClosed() {
        let preparation = RelayDeviceIDStorage.prepareTarget(
            source: .persist(["device-a", "device-b"], context: .manualRetry),
            persist: { _ in .stored(["device-a"]) },
            load: { .notFound }
        )

        #expect(preparation == .skip(.stop))
    }

    @Test("Token rotation uses only an exact typed Keychain load")
    func issue148TokenRotationUsesExactStoredTarget() {
        var persistCount = 0
        let loaded = RelayDeviceIDStorage.prepareTarget(
            source: .storedDeviceIDs,
            persist: { _ in
                persistCount += 1
                return .stored([])
            },
            load: { .loaded(["device-b", "device-a", "device-b"]) }
        )
        let failed = RelayDeviceIDStorage.prepareTarget(
            source: .storedDeviceIDs,
            persist: { _ in
                persistCount += 1
                return .stored([])
            },
            load: { .failed }
        )
        let missing = RelayDeviceIDStorage.prepareTarget(
            source: .storedDeviceIDs,
            persist: { _ in
                persistCount += 1
                return .stored([])
            },
            load: { .notFound }
        )

        #expect(loaded == .ready(["device-a", "device-b"]))
        #expect(failed == .skip(nil))
        #expect(missing == .ready([]))
        #expect(persistCount == 0)
    }

    @Test("Recovery uses every ID in the successfully persisted merged payload")
    func issue148MergedRewriteClearsPersistedFailures() {
        let failedIDs: Set<String> = ["device-a", "device-b"]

        let remaining = RelayDeviceIDStorage.remainingFailedIDs(
            afterStoring: ["device-a", "device-b", "device-c"],
            from: failedIDs
        )

        #expect(remaining.isEmpty)
    }

    @Test("Failures for removed devices do not leave a stale global error")
    func issue148RemovedDevicePrunesStorageFailure() {
        let relevant = RelayDeviceIDStorage.relevantFailedIDs(
            currentIDs: ["device-b"],
            storedIDs: ["device-b", "device-c"],
            from: ["device-a", "device-b"]
        )

        #expect(relevant == Set(["device-b"]))
    }

    @Test("An empty current list cannot clear failures after an ambiguous read")
    func issue148AmbiguousEmptyReadKeepsStorageFailure() {
        let relevant = RelayDeviceIDStorage.relevantFailedIDs(
            currentIDs: [],
            storedIDs: [],
            from: ["device-a"]
        )

        #expect(relevant == Set(["device-a"]))
    }

    private enum InjectedError: Error {
        case encoding
    }
}

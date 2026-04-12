import Foundation
import os

private let backgroundUploadLogger = Logger(subsystem: "eu.vaultsync.app", category: "background-upload")

struct BackgroundUploadConfiguration: Codable, Equatable, Sendable {
    var isEnabled: Bool
    var endpointURL: String
    var authToken: String

    var trimmedEndpointURL: String { endpointURL.trimmingCharacters(in: .whitespacesAndNewlines) }
    var trimmedAuthToken: String { authToken.trimmingCharacters(in: .whitespacesAndNewlines) }

    var isValid: Bool {
        isEnabled && URL(string: trimmedEndpointURL) != nil && !trimmedAuthToken.isEmpty
    }

    static let disabled = BackgroundUploadConfiguration(isEnabled: false, endpointURL: "", authToken: "")
}

@MainActor
final class BackgroundUploadService: NSObject {
    static let shared = BackgroundUploadService()

    private static let configDefaultsKey = "background-upload-config.v1"
    private static let manifestDefaultsKey = "background-upload-manifest.v1"
    private static let pendingDefaultsKey = "background-upload-pending.v1"
    private static let sessionIdentifier = "eu.vaultsync.app.background-upload"
    private static let taskEventsDidChangeNotification = Notification.Name("BackgroundUploadTaskEventsDidChange")
    private static let candidateWindow: TimeInterval = 10 * 60
    private static let stabilizationDelay: Duration = .seconds(2)

    struct EnqueueSummary: Sendable {
        let isConfigured: Bool
        let scannedFiles: Int
        let changedFiles: Int
        let enqueuedUploads: Int
        let detail: String
    }

    private struct FileSnapshot: Codable, Equatable {
        let relativePath: String
        let modifiedAt: Date
        let size: Int64
    }

    private struct PendingUpload: Codable {
        let relativePath: String
        let modifiedAt: Date
        let size: Int64
        let stagedFilePath: String
    }

    private lazy var session: URLSession = {
        let configuration = URLSessionConfiguration.background(withIdentifier: Self.sessionIdentifier)
        configuration.sessionSendsLaunchEvents = true
        configuration.isDiscretionary = false
        configuration.timeoutIntervalForRequest = 30
        configuration.timeoutIntervalForResource = 15 * 60
        return URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
    }()

    private var backgroundCompletionHandler: (() -> Void)?

    static func configuration() -> BackgroundUploadConfiguration {
        guard let data = UserDefaults.standard.data(forKey: configDefaultsKey),
              let decoded = try? JSONDecoder().decode(BackgroundUploadConfiguration.self, from: data) else {
            return .disabled
        }
        return decoded
    }

    static func saveConfiguration(_ configuration: BackgroundUploadConfiguration) {
        let sanitized = BackgroundUploadConfiguration(
            isEnabled: configuration.isEnabled,
            endpointURL: configuration.trimmedEndpointURL,
            authToken: configuration.trimmedAuthToken
        )
        guard let data = try? JSONEncoder().encode(sanitized) else { return }
        UserDefaults.standard.set(data, forKey: configDefaultsKey)
    }

    static func clearConfiguration() {
        UserDefaults.standard.removeObject(forKey: configDefaultsKey)
    }

    func handleEventsForBackgroundURLSession(identifier: String, completionHandler: @escaping () -> Void) {
        guard identifier == Self.sessionIdentifier else { return }
        backgroundCompletionHandler = completionHandler
        _ = session
    }

    func enqueueChangedMarkdownFilesIfConfigured(trigger: String) async -> EnqueueSummary {
        let scanStartedAt = Date()
        let configuration = Self.configuration()
        guard configuration.isValid, let endpointURL = URL(string: configuration.trimmedEndpointURL) else {
            return EnqueueSummary(
                isConfigured: false,
                scannedFiles: 0,
                changedFiles: 0,
                enqueuedUploads: 0,
                detail: "Background upload is disabled or incomplete."
            )
        }

        guard let (rootURL, _) = BookmarkService.resolveBookmark(identifier: "obsidian-root") else {
            return EnqueueSummary(
                isConfigured: true,
                scannedFiles: 0,
                changedFiles: 0,
                enqueuedUploads: 0,
                detail: "No Obsidian bookmark available for background upload."
            )
        }
        guard BookmarkService.startAccessing(url: rootURL) else {
            return EnqueueSummary(
                isConfigured: true,
                scannedFiles: 0,
                changedFiles: 0,
                enqueuedUploads: 0,
                detail: "Bookmark access failed for background upload."
            )
        }
        defer { BookmarkService.stopAccessing(url: rootURL) }

        let previousManifest = loadManifest()
        let firstPass = detectMarkdownSnapshots(rootURL: rootURL)
        traceScanPass(label: "pass-1", rootURL: rootURL, snapshots: firstPass)

        try? await Task.sleep(for: Self.stabilizationDelay)

        let secondPass = detectMarkdownSnapshots(rootURL: rootURL)
        traceScanPass(label: "pass-2", rootURL: rootURL, snapshots: secondPass)

        let changed = determineChangedCandidates(
            baseline: previousManifest,
            firstPass: firstPass,
            secondPass: secondPass,
            scanStartedAt: scanStartedAt
        )
        traceCandidates(changed, label: "changed-candidates")

        if previousManifest.isEmpty {
            saveManifest(dictionary(for: secondPass))
            if changed.isEmpty {
                return EnqueueSummary(
                    isConfigured: true,
                    scannedFiles: secondPass.count,
                    changedFiles: 0,
                    enqueuedUploads: 0,
                    detail: "Upload manifest primed from \(secondPass.count) markdown file(s); no recent candidates yet."
                )
            }
        }

        guard !changed.isEmpty else {
            return EnqueueSummary(
                isConfigured: true,
                scannedFiles: secondPass.count,
                changedFiles: 0,
                enqueuedUploads: 0,
                detail: "No changed markdown files detected for background upload."
            )
        }

        var pending = loadPendingUploads()
        var enqueued = 0
        let deviceID = SyncBridgeService.deviceID()
        let stageRoot = stagedUploadsRoot()
        try? FileManager.default.createDirectory(at: stageRoot, withIntermediateDirectories: true)

        for snapshot in changed {
            let sourceURL = rootURL.appendingPathComponent(snapshot.relativePath)
            let stagedURL = stageRoot.appendingPathComponent(UUID().uuidString + "-" + sourceURL.lastPathComponent)
            do {
                if FileManager.default.fileExists(atPath: stagedURL.path) {
                    try FileManager.default.removeItem(at: stagedURL)
                }
                try FileManager.default.copyItem(at: sourceURL, to: stagedURL)
            } catch {
                BackgroundDebugStore().record(
                    area: "upload",
                    message: "Failed staging \(snapshot.relativePath): \(error.localizedDescription)"
                )
                continue
            }

            var request = URLRequest(url: endpointURL)
            request.httpMethod = "PUT"
            request.setValue("Bearer \(configuration.trimmedAuthToken)", forHTTPHeaderField: "Authorization")
            request.setValue(snapshot.relativePath, forHTTPHeaderField: "X-VaultSync-Relative-Path")
            request.setValue(deviceID, forHTTPHeaderField: "X-VaultSync-Device-ID")
            request.setValue("text/markdown; charset=utf-8", forHTTPHeaderField: "Content-Type")
            request.setValue(trigger, forHTTPHeaderField: "X-VaultSync-Trigger")

            let task = session.uploadTask(with: request, fromFile: stagedURL)
            BackgroundDebugStore().record(
                area: "upload",
                message: "Queued upload task \(task.taskIdentifier) for \(snapshot.relativePath)."
            )
            pending[String(task.taskIdentifier)] = PendingUpload(
                relativePath: snapshot.relativePath,
                modifiedAt: snapshot.modifiedAt,
                size: snapshot.size,
                stagedFilePath: stagedURL.path
            )
            task.resume()
            enqueued += 1
        }

        savePendingUploads(pending)
        return EnqueueSummary(
            isConfigured: true,
            scannedFiles: secondPass.count,
            changedFiles: changed.count,
            enqueuedUploads: enqueued,
            detail: "Queued \(enqueued) background upload(s) from \(changed.count) changed markdown file(s)."
        )
    }

    private func detectMarkdownSnapshots(rootURL: URL) -> [FileSnapshot] {
        let resourceKeys: [URLResourceKey] = [.isRegularFileKey, .contentModificationDateKey, .fileSizeKey]
        guard let enumerator = FileManager.default.enumerator(
            at: rootURL,
            includingPropertiesForKeys: resourceKeys,
            options: [.skipsHiddenFiles, .skipsPackageDescendants]
        ) else {
            return []
        }

        var snapshots: [FileSnapshot] = []
        for case let fileURL as URL in enumerator {
            guard fileURL.pathExtension.lowercased() == "md",
                  let values = try? fileURL.resourceValues(forKeys: Set(resourceKeys)),
                  values.isRegularFile == true else {
                continue
            }

            let relativePath = fileURL.path.replacingOccurrences(of: rootURL.path + "/", with: "")
            snapshots.append(FileSnapshot(
                relativePath: relativePath,
                modifiedAt: values.contentModificationDate ?? .distantPast,
                size: Int64(values.fileSize ?? 0)
            ))
        }
        return snapshots
    }

    private func determineChangedCandidates(
        baseline: [String: FileSnapshot],
        firstPass: [FileSnapshot],
        secondPass: [FileSnapshot],
        scanStartedAt: Date
    ) -> [FileSnapshot] {
        let firstByPath = dictionary(for: firstPass)
        let recentThreshold = scanStartedAt.addingTimeInterval(-Self.candidateWindow)

        let candidates = secondPass.filter { snapshot in
            let baselineSnapshot = baseline[snapshot.relativePath]
            let firstPassSnapshot = firstByPath[snapshot.relativePath]
            let changedSinceBaseline = baselineSnapshot != snapshot
            let changedDuringStabilization = firstPassSnapshot != snapshot
            let isRecent = snapshot.modifiedAt >= recentThreshold

            if baseline.isEmpty {
                return isRecent
            }

            return changedSinceBaseline || changedDuringStabilization || isRecent
        }

        return candidates.sorted { lhs, rhs in
            if lhs.modifiedAt == rhs.modifiedAt {
                return lhs.relativePath < rhs.relativePath
            }
            return lhs.modifiedAt > rhs.modifiedAt
        }
    }

    private func dictionary(for snapshots: [FileSnapshot]) -> [String: FileSnapshot] {
        Dictionary(uniqueKeysWithValues: snapshots.map { ($0.relativePath, $0) })
    }

    private func traceScanPass(label: String, rootURL: URL, snapshots: [FileSnapshot]) {
        let recent = snapshots
            .sorted { $0.modifiedAt > $1.modifiedAt }
            .prefix(8)
            .map {
                "\($0.relativePath) [mtime=\(iso8601String(from: $0.modifiedAt)), size=\($0.size)]"
            }
            .joined(separator: " | ")

        BackgroundDebugStore().record(
            area: "upload",
            message: "Scan \(label): root=\(rootURL.lastPathComponent), files=\(snapshots.count), recent=\(recent.isEmpty ? "none" : recent)"
        )
    }

    private func traceCandidates(_ snapshots: [FileSnapshot], label: String) {
        let summary = snapshots
            .prefix(8)
            .map {
                "\($0.relativePath) [mtime=\(iso8601String(from: $0.modifiedAt)), size=\($0.size)]"
            }
            .joined(separator: " | ")

        BackgroundDebugStore().record(
            area: "upload",
            message: "\(label): count=\(snapshots.count), files=\(summary.isEmpty ? "none" : summary)"
        )
    }

    private func stagedUploadsRoot() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("background-upload-staging", isDirectory: true)
    }

    private func loadManifest() -> [String: FileSnapshot] {
        guard let data = UserDefaults.standard.data(forKey: Self.manifestDefaultsKey),
              let decoded = try? JSONDecoder().decode([String: FileSnapshot].self, from: data) else {
            return [:]
        }
        return decoded
    }

    private func saveManifest(_ manifest: [String: FileSnapshot]) {
        guard let data = try? JSONEncoder().encode(manifest) else { return }
        UserDefaults.standard.set(data, forKey: Self.manifestDefaultsKey)
    }

    private func loadPendingUploads() -> [String: PendingUpload] {
        guard let data = UserDefaults.standard.data(forKey: Self.pendingDefaultsKey),
              let decoded = try? JSONDecoder().decode([String: PendingUpload].self, from: data) else {
            return [:]
        }
        return decoded
    }

    private func savePendingUploads(_ pending: [String: PendingUpload]) {
        guard let data = try? JSONEncoder().encode(pending) else { return }
        UserDefaults.standard.set(data, forKey: Self.pendingDefaultsKey)
        NotificationCenter.default.post(name: Self.taskEventsDidChangeNotification, object: nil)
    }

    private func iso8601String(from date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }
}

extension BackgroundUploadService: URLSessionTaskDelegate, URLSessionDataDelegate {
    nonisolated func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        Task { @MainActor in
            var pending = loadPendingUploads()
            guard let upload = pending.removeValue(forKey: String(task.taskIdentifier)) else {
                return
            }
            defer {
                savePendingUploads(pending)
                try? FileManager.default.removeItem(atPath: upload.stagedFilePath)
            }

            let httpStatus = (task.response as? HTTPURLResponse)?.statusCode ?? 0
            if let error {
                BackgroundDebugStore().record(
                    area: "upload",
                    message: "Upload failed for \(upload.relativePath): \(error.localizedDescription)"
                )
                backgroundUploadLogger.error("Background upload failed for \(upload.relativePath, privacy: .public): \(error.localizedDescription, privacy: .public)")
                return
            }

            guard (200...299).contains(httpStatus) else {
                BackgroundDebugStore().record(
                    area: "upload",
                    message: "Upload rejected for \(upload.relativePath): HTTP \(httpStatus)"
                )
                backgroundUploadLogger.error("Background upload rejected for \(upload.relativePath, privacy: .public): HTTP \(httpStatus)")
                return
            }

            var manifest = loadManifest()
            manifest[upload.relativePath] = FileSnapshot(
                relativePath: upload.relativePath,
                modifiedAt: upload.modifiedAt,
                size: upload.size
            )
            saveManifest(manifest)
            BackgroundDebugStore().record(
                area: "upload",
                message: "Upload succeeded for \(upload.relativePath) (HTTP \(httpStatus))."
            )
            backgroundUploadLogger.info("Background upload succeeded for \(upload.relativePath, privacy: .public)")
        }
    }

    nonisolated func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        Task { @MainActor in
            backgroundCompletionHandler?()
            backgroundCompletionHandler = nil
        }
    }
}

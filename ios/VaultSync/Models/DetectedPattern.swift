import Foundation

/// Mirror of the Go bridge's `DetectedPattern` JSON.
/// Surfaced in the Sync Filters UI's "Found in this vault" section.
struct DetectedPattern: Codable, Identifiable, Sendable, Hashable {
    let pattern: String
    let label: String
    let sizeBytes: Int64
    let fileCount: Int

    var id: String { pattern }
}

struct DetectedScan: Decodable, Sendable {
    let detected: [DetectedPattern]
    let complete: Bool
    let error: String?
}

enum KnownPatternScanResult: Equatable, Sendable {
    case complete([DetectedPattern])
    case unavailable
}

import Foundation
import Testing
import UIKit
@testable import VaultSync

/// The system's prominent button style fills its shape with the inherited
/// tint and draws the label white. The tint is the text token
/// `vaultAccentText` (decision 065), which in dark is the lifted teal: white
/// on it measures about 2.2:1. The kit's primary pair, `vaultOnAccent` on
/// `vaultAccentProminent`, clears 4.5:1 in every appearance (decision 044).
/// Each button is found by its declaration in its view's source and measured
/// with the pair its style draws, so a button moved back to the system style
/// fails here, and so does a new one anywhere in the app or the widget.
@Suite("No button draws white on the lifted teal (#353)")
struct Issue353ProminentButtonContrastTests {
    /// A button, found by the start of its declaration in a view's source.
    struct Probe: CustomStringConvertible, Sendable {
        let file: String
        let declaration: String
        var description: String { "\(declaration) in \(file)" }
    }

    /// The four buttons the system style drew: the QR scanner's two (camera
    /// denied, camera unavailable) and Controlled Diagnostics' two.
    static let probes = [
        Probe(file: "QRScannerView.swift", declaration: #"Button("Open Settings")"#),
        Probe(file: "QRScannerView.swift", declaration: "Button(manualButtonTitle)"),
        Probe(file: "ControlledDiagnosticsView.swift", declaration: #"Button(L10n.tr("Fingerprint Matches — Activate"))"#),
        Probe(file: "ControlledDiagnosticsView.swift", declaration: #"Button(L10n.tr("Start Foreground Upload and Download Check"))"#),
    ]

    /// `ios/`, from this file's place in `ios/VaultSyncTests/`.
    static var iosDirectory: URL {
        URL(fileURLWithPath: "\(#filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
    }

    /// The label and fill a button style draws; nil for a style this suite
    /// has not measured.
    static func pair(forStyle style: String) -> (label: WCAGContrast.Ink, fill: WCAGContrast.Ink)? {
        if style.hasPrefix(".borderedProminent") {
            return (.system(.white), .token(.vaultAccentText))
        }
        if style.hasPrefix(".vault(.primary") {
            return (.token(.vaultOnAccent), .token(.vaultAccentProminent))
        }
        return nil
    }

    @Test("Each button's label clears 4.5:1 on its fill", arguments: probes)
    func labelClearsAA(probe: Probe) throws {
        let url = Self.iosDirectory.appendingPathComponent("VaultSync/Views/\(probe.file)")
        let source = try String(contentsOf: url, encoding: .utf8)
        let declaration = try #require(source.range(of: probe.declaration), "\(probe) not found")
        // The button's own style: before the next button is declared.
        let end = source.range(of: "Button(", range: declaration.upperBound..<source.endIndex)?.lowerBound
            ?? source.endIndex
        let modifier = try #require(
            source.range(of: ".buttonStyle(", range: declaration.upperBound..<end),
            "\(probe) has no style of its own"
        )
        let style = String(source[modifier.upperBound...].prefix { $0 != "\n" })
        let pair = try #require(Self.pair(forStyle: style), "\(probe): unmeasured style \(style)")
        for appearance in WCAGContrast.Appearance.all {
            let ratio = WCAGContrast.ratio(pair.label, on: pair.fill, in: appearance)
            #expect(ratio >= 4.5, "\(probe) in \(appearance): \(ratio)")
        }
    }

    @Test("No view of the app or the widget uses the system's prominent style")
    func noSystemProminentStyle() throws {
        var hits: [String] = []
        for folder in ["VaultSync", "VaultSyncWidget"] {
            let root = Self.iosDirectory.appendingPathComponent(folder)
            let files = try #require(FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil))
            for case let file as URL in files where file.pathExtension == "swift" {
                let source = try String(contentsOf: file, encoding: .utf8)
                for (index, line) in source.components(separatedBy: "\n").enumerated()
                where line.contains("borderedProminent") || line.contains("BorderedProminentButtonStyle") {
                    hits.append("\(folder)/\(file.lastPathComponent):\(index + 1)")
                }
            }
        }
        #expect(hits.isEmpty, "the system's prominent style draws white on the lifted teal: \(hits)")
    }
}

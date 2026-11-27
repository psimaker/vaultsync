import Foundation
import Testing
import UIKit
@testable import VaultSync

/// A disabled kit button is drawn at 40 %, its label included. Add Hub's
/// Pair, Sync and Reconnect buttons are disabled while the Hub is asked and
/// show a `vaultOnAccent` spinner with "Pairing…" or "Asking your Hub…"
/// inside: dimmed with the button, the dark label fell to about 1.3:1 on the
/// teal in dark mode, and it is the only sign that something runs. A busy
/// button stays disabled for taps and is drawn at full strength.
@Suite("A busy button's spinner and text stay readable (#354)")
struct Issue354BusyButtonTests {
    /// Add Hub's page.
    static let page = WCAGContrast.Ink.token(.vaultBackground)

    /// Contrast of the kit's primary label on its fill, both drawn at
    /// `opacity` over the page — the way `.opacity` composites the button.
    static func drawnRatio(opacity: Double, in appearance: WCAGContrast.Appearance) -> Double {
        let ground = WCAGContrast.resolve(page, in: appearance)
        func drawn(_ ink: WCAGContrast.Ink) -> WCAGContrast.RGBA {
            var color = WCAGContrast.composite(WCAGContrast.resolve(ink, in: appearance), over: ground)
            color.alpha = opacity
            return WCAGContrast.composite(color, over: ground)
        }
        let fill = drawn(.token(.vaultAccentProminent))
        let label = drawn(.token(.vaultOnAccent))
        let (first, second) = (WCAGContrast.luminance(label), WCAGContrast.luminance(fill))
        return (max(first, second) + 0.05) / (min(first, second) + 0.05)
    }

    @Test("A busy button is drawn at full strength; a disabled one stays dimmed")
    func opacityRule() {
        #expect(VaultButtonStyle.opacity(isEnabled: false, busy: true, isPressed: false) == 1)
        #expect(VaultButtonStyle.opacity(isEnabled: false, busy: false, isPressed: false) == 0.4)
        #expect(VaultButtonStyle.opacity(isEnabled: true, busy: false, isPressed: true) == 0.7)
        #expect(VaultButtonStyle.opacity(isEnabled: true, busy: false, isPressed: false) == 1)
    }

    @Test("The busy label and spinner clear 4.5:1 on the primary fill", arguments: WCAGContrast.Appearance.all)
    func busyLabelClearsAA(appearance: WCAGContrast.Appearance) {
        // Busy buttons are disabled by their caller (`canPair`, `canSync`,
        // `canReconnect` are false while the Hub is asked).
        let opacity = VaultButtonStyle.opacity(isEnabled: false, busy: true, isPressed: false)
        let ratio = Self.drawnRatio(opacity: opacity, in: appearance)
        #expect(ratio >= 4.5, "busy primary button in \(appearance): \(ratio)")
    }

    @Test("Every spinner inside a kit button in Add Hub is in a busy button")
    func addHubSpinnersAreBusy() throws {
        let url = URL(fileURLWithPath: "\(#filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("VaultSync/Views/AddHubSheet.swift")
        let source = try String(contentsOf: url, encoding: .utf8)
        var inButtons = 0
        var searchFrom = source.startIndex
        while let spinner = source.range(of: "ProgressView()", range: searchFrom..<source.endIndex) {
            searchFrom = spinner.upperBound
            // The style of the button whose label holds the spinner comes
            // before any other button is declared; a spinner outside a
            // button meets a declaration first.
            guard let style = source.range(of: ".buttonStyle(", range: spinner.upperBound..<source.endIndex) else { continue }
            let between = source[spinner.upperBound..<style.lowerBound]
            if between.contains("Button(") || between.contains("Button {") { continue }
            let line = source[style.upperBound...].prefix { $0 != "\n" }
            guard line.hasPrefix(".vault(") else { continue }
            inButtons += 1
            #expect(line.contains("busy: model.isWorking"), "a spinner in a kit button that is not busy: \(line)")
        }
        // Pair, Sync and Reconnect.
        #expect(inButtons == 3)
    }
}

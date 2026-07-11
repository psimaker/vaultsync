import Testing
@testable import VaultSync

@Suite("SyncUserError Mapping")
struct SyncUserErrorTests {
    @Test("Maps syncthing-not-running into actionable UX contract")
    func mapsSyncthingNotRunning() {
        let error = SyncUserError.from(rawMessage: "syncthing not running")

        #expect(error.category == .syncthingNotRunning)
        #expect(error.title == L10n.tr("Sync Engine Not Running"))
        #expect(error.message == L10n.tr("VaultSync cannot talk to Syncthing right now."))
        #expect(error.remediation == L10n.tr("Keep the app open for a moment and retry. If this persists, restart VaultSync."))
        #expect(error.technicalDetails == "syncthing not running")
    }

    @Test("Maps relay connectivity failures to relay-unreachable category")
    func mapsRelayConnectivityFailure() {
        let error = SyncUserError.from(rawMessage: "relay request timed out")

        #expect(error.category == .relayUnreachable)
        #expect(error.title == L10n.tr("Relay Unreachable"))
    }

    @Test("Maps folder status reasons into per-folder remediation")
    func mapsFolderStatusReasons() {
        let permission = SyncUserError.fromFolderStatus(
            reason: "permission_denied",
            message: "permission denied",
            path: "/vaults/work"
        )
        #expect(permission.category == .permission)
        #expect(permission.message.contains("/vaults/work"))

        let missing = SyncUserError.fromFolderStatus(
            reason: "folder_not_found",
            message: "folder missing",
            path: nil
        )
        #expect(missing.category == .config)
        #expect(missing.title == L10n.tr("Folder Not Configured"))
    }

    @Test("Maps relay provisioning failures for rate limiting and unknown causes")
    func mapsRelayProvisionFailures() {
        let rateLimited = SyncUserError.relayProvisionFailed(reason: "HTTP 429 rate limit")
        #expect(rateLimited.category == .relayProvision)
        #expect(rateLimited.title == L10n.tr("Relay Rate Limited"))

        let generic = SyncUserError.relayProvisionFailed(reason: "unexpected")
        #expect(generic.category == .relayProvision)
        #expect(generic.title == L10n.tr("Relay Provisioning Failed"))
    }

    @Test("Uses fallback title for unknown raw errors")
    func unknownErrorUsesFallbackTitle() {
        let error = SyncUserError.from(rawMessage: "weird low-level error", fallbackTitle: "Bridge Failure")

        #expect(error.category == .unknown)
        #expect(error.title == "Bridge Failure")
        #expect(error.remediation == L10n.tr("Retry the action. If it keeps failing, restart the app and check Settings diagnostics."))
    }
}

@Suite("Marker-missing folder error mapping (#65)")
struct FolderMarkerMissingMappingTests {
    /// Syncthing's literal engine text (lib/config/folderconfiguration.go) —
    /// the exact string users saw raw and untranslated.
    static let rawEngineText =
        "folder marker missing (this indicates potential data loss, search docs/forum to get information about how to proceed)"

    @Test("Folder status maps the raw engine text to localized guidance")
    func mapsFolderStatusMarkerMissing() {
        let error = SyncUserError.fromFolderStatus(
            reason: "unknown_error",
            message: Self.rawEngineText,
            path: "/vaults/Life"
        )

        #expect(error.category == .folderMarkerMissing)
        #expect(error.title == L10n.tr("Vault Folder Was Moved or Deleted"))
        #expect(!error.message.contains("search docs/forum"))
        #expect(error.message.contains("/vaults/Life"))
        #expect(error.technicalDetails == Self.rawEngineText)
    }

    @Test("Remediation follows the manual-recovery doctrine — no rescan, no retry")
    func remediationAvoidsRescanMisdirection() {
        let error = SyncUserError.fromFolderStatus(
            reason: "unknown_error",
            message: Self.rawEngineText,
            path: nil
        )

        let guidance = error.remediation.lowercased()
        #expect(!guidance.isEmpty)
        #expect(!guidance.contains("rescan"))
        #expect(!guidance.contains("retry"))
    }

    @Test("Raw-message path (e.g. a failed rescan) maps the same way")
    func mapsRawMessageMarkerMissing() {
        let error = SyncUserError.from(rawMessage: Self.rawEngineText)

        #expect(error.category == .folderMarkerMissing)
        #expect(error.title == L10n.tr("Vault Folder Was Moved or Deleted"))
        #expect(error.technicalDetails == Self.rawEngineText)
        #expect(!error.remediation.lowercased().contains("retry"))
    }

    @Test("Marker-missing errors route to their own troubleshooting anchor")
    func routesToDedicatedTroubleshootingAnchor() {
        let error = SyncUserError.fromFolderStatus(
            reason: "unknown_error",
            message: Self.rawEngineText,
            path: nil
        )

        let url = SyncUserError.troubleshootingURL(for: error)
        #expect(url?.absoluteString.hasSuffix("#vault-folder-was-moved-or-deleted") == true)
    }

    @Test("Other unknown folder errors keep the generic mapping")
    func keepsGenericMappingForOtherErrors() {
        let error = SyncUserError.fromFolderStatus(
            reason: "unknown_error",
            message: "database is locked",
            path: nil
        )

        #expect(error.title == L10n.tr("Folder Sync Error"))
        #expect(error.message == "database is locked")
    }
}

@Suite("Rescan CTA availability under marker loss (#65)")
struct RescanCTAAvailabilityTests {
    private func errorStatus(message: String) -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: "error",
            stateChanged: "2026-07-07T10:00:00Z",
            completionPct: 0,
            globalBytes: 0,
            globalFiles: 0,
            localBytes: 0,
            localFiles: 0,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: "unknown_error",
            errorMessage: message,
            errorPath: nil,
            errorChanged: nil
        ))
    }

    @Test("Marker loss as the only error hides the rescan path")
    @MainActor
    func markerOnlyErrorsAreNotRescanable() {
        let manager = SyncthingManager()
        manager._testSetFolderStatuses([
            "vault-a": errorStatus(message: FolderMarkerMissingMappingTests.rawEngineText),
        ])

        #expect(manager.hasRescanableFolderErrors == false)
    }

    @Test("A non-marker folder error keeps the rescan path available")
    @MainActor
    func otherErrorsStayRescanable() {
        let manager = SyncthingManager()
        manager._testSetFolderStatuses([
            "vault-a": errorStatus(message: FolderMarkerMissingMappingTests.rawEngineText),
            "vault-b": errorStatus(message: "database is locked"),
        ])

        #expect(manager.hasRescanableFolderErrors)
    }
}

@Suite("Device pairing error mapping (#93)")
struct DevicePairingErrorMappingTests {
    @Test("Invalid device ID from the bridge maps to actionable guidance, not the unexpected-error fallback")
    func mapsInvalidDeviceID() {
        // Exact shape the bridge produces: "invalid device ID: " + the Go
        // parser error (go/bridge/devices.go:33, lib/protocol/deviceid.go).
        let raw = "invalid device ID: \"NOTANID\": device ID invalid: incorrect length"
        let error = SyncUserError.from(rawMessage: raw, fallbackTitle: L10n.tr("Could Not Add Device"))

        #expect(error.category == .validation)
        #expect(error.title == L10n.tr("Invalid Device ID"))
        #expect(error.remediation.contains("Show ID"))
        #expect(error.technicalDetails == raw)
    }

    @Test("Check-digit and base32 parser variants map the same way")
    func mapsParserVariants() {
        let variants = [
            "invalid device ID: \"P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ3\": check digit incorrect",
            "invalid device ID: illegal base32 data at input byte 3",
        ]
        for raw in variants {
            let error = SyncUserError.from(rawMessage: raw)
            #expect(error.category == .validation)
            #expect(error.title == L10n.tr("Invalid Device ID"))
        }
    }

    @Test("Duplicate device maps to already-added guidance, not the generic configuration error")
    func mapsDeviceAlreadyExists() {
        let error = SyncUserError.from(rawMessage: "device already exists")

        #expect(error.category == .config)
        #expect(error.title == L10n.tr("Device Already Added"))
        #expect(error.title != L10n.tr("Configuration Error"))
    }

    @Test("Own device ID maps to own-device guidance")
    func mapsOwnDeviceID() {
        let error = SyncUserError.from(rawMessage: "cannot add own device ID")

        #expect(error.category == .config)
        #expect(error.title == L10n.tr("This Is Your Own Device ID"))
        #expect(error.remediation.contains("Show ID"))
    }

    @Test("No pairing error falls through to the restart-the-app fallback")
    func pairingErrorsNeverUnknown() {
        let bridgeErrors = [
            "invalid device ID: \"foo\": device ID invalid: incorrect length",
            "device already exists",
            "cannot add own device ID",
        ]
        for raw in bridgeErrors {
            #expect(SyncUserError.from(rawMessage: raw).category != .unknown)
        }
    }
}

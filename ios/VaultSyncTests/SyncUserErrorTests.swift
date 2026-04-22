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

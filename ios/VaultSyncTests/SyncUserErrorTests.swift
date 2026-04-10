import Testing
@testable import VaultSync

@Suite("SyncUserError Mapping")
struct SyncUserErrorTests {
    @Test("Maps syncthing-not-running into actionable UX contract")
    func mapsSyncthingNotRunning() {
        let error = SyncUserError.from(rawMessage: "syncthing not running")

        #expect(error.category == .syncthingNotRunning)
        #expect(error.title == "Sync Engine Not Running")
        #expect(error.message.contains("cannot talk to Syncthing"))
        #expect(error.remediation.contains("restart VaultSync"))
        #expect(error.technicalDetails == "syncthing not running")
    }

    @Test("Maps relay connectivity failures to relay-unreachable category")
    func mapsRelayConnectivityFailure() {
        let error = SyncUserError.from(rawMessage: "relay request timed out")

        #expect(error.category == .relayUnreachable)
        #expect(error.title == "Relay Unreachable")
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
        #expect(missing.title == "Folder Not Configured")
    }

    @Test("Maps relay provisioning failures for rate limiting and unknown causes")
    func mapsRelayProvisionFailures() {
        let rateLimited = SyncUserError.relayProvisionFailed(reason: "HTTP 429 rate limit")
        #expect(rateLimited.category == .relayProvision)
        #expect(rateLimited.title == "Relay Rate Limited")

        let generic = SyncUserError.relayProvisionFailed(reason: "unexpected")
        #expect(generic.category == .relayProvision)
        #expect(generic.title == "Relay Provisioning Failed")
    }

    @Test("Uses fallback title for unknown raw errors")
    func unknownErrorUsesFallbackTitle() {
        let error = SyncUserError.from(rawMessage: "weird low-level error", fallbackTitle: "Bridge Failure")

        #expect(error.category == .unknown)
        #expect(error.title == "Bridge Failure")
        #expect(error.remediation.contains("restart the app"))
    }
}

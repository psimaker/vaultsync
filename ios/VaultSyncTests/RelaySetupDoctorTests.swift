import Foundation
import Testing
@testable import VaultSync

@Suite("Relay setup --doctor escalation (#91)")
struct RelaySetupDoctorTests {
    @Test("Doctor command targets the container the installer creates and invokes --doctor")
    func doctorCommandShape() {
        // The in-app command must keep matching the container name the one-line
        // installer (and the docker run alternative on the same screen) uses.
        #expect(RelayServerSetupView.doctorCommand == "docker exec vaultsync-notify vaultsync-notify --doctor")
    }

    @Test("Quick-triage troubleshooting anchor resolves to the docs page")
    func quickTriageAnchorURL() {
        // docs/troubleshooting.md carries a "## Relay quick triage" heading that
        // generates exactly this GitHub anchor — renaming the heading breaks the
        // deep link from RelayServerSetupView.
        let url = SyncUserError.troubleshootingURL(anchor: "relay-quick-triage")
        #expect(url?.absoluteString == "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md#relay-quick-triage")
    }
}

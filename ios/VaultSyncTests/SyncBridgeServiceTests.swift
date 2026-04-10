import Testing
@testable import VaultSync

@Test func bridgePingReturnsPong() {
    #expect(SyncBridgeService.ping() == "pong")
}

@Test func goVersionHasPrefix() {
    #expect(SyncBridgeService.goVersion().hasPrefix("go"))
}

@Test func archIsNotEmpty() {
    #expect(!SyncBridgeService.arch().isEmpty)
}

@Test func syncthingVersionIsNotEmpty() {
    #expect(!SyncBridgeService.syncthingVersion().isEmpty)
}

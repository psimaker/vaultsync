import Testing
@testable import VaultSync

@Suite("SyncStatus registry & wire decoding")
struct SyncStatusTests {
    @Test("Known wire strings decode to their canonical status")
    func decodesKnownStrings() {
        #expect(SyncStatus.fromWire("idle") == .synced)
        #expect(SyncStatus.fromWire("synced") == .synced)
        #expect(SyncStatus.fromWire("syncing") == .syncing)
        #expect(SyncStatus.fromWire("scanning") == .syncing)
        #expect(SyncStatus.fromWire("starting") == .starting)
        #expect(SyncStatus.fromWire("attention") == .attention)
        #expect(SyncStatus.fromWire("warning") == .attention)
        #expect(SyncStatus.fromWire("error") == .error)
        #expect(SyncStatus.fromWire("paused") == .paused)
    }

    @Test("Decoding is case-insensitive")
    func decodingIsCaseInsensitive() {
        #expect(SyncStatus.fromWire("IDLE") == .synced)
        #expect(SyncStatus.fromWire("Syncing") == .syncing)
        #expect(SyncStatus.fromWire("ERROR") == .error)
    }

    // The load-bearing contract: a status the app never taught the widget about
    // must surface as "needs attention", NEVER silently as the green "all good"
    // branch. This is the regression guard for the documented widget-lies bug.
    @Test("Unknown wire strings resolve to .attention, never .synced")
    func unknownResolvesToAttention() {
        #expect(SyncStatus.fromWire("garbage") == .attention)
        #expect(SyncStatus.fromWire("") == .attention)
        #expect(SyncStatus.fromWire("some-future-state") == .attention)
        for raw in ["garbage", "", "???", "newstate"] {
            #expect(SyncStatus.fromWire(raw) != .synced)
        }
    }

    @Test("Wire value round-trips through fromWire for every case")
    func wireValueRoundTrips() {
        for status in SyncStatus.allCases {
            #expect(SyncStatus.fromWire(status.wireValue) == status)
        }
    }

    @Test("Every status carries a symbol and a non-empty label")
    func everyStatusHasSymbolAndLabel() {
        for status in SyncStatus.allCases {
            #expect(!status.symbolName.isEmpty)
            #expect(!status.label.isEmpty)
        }
    }

    @Test("Only attention and error are urgent")
    func urgencyFlags() {
        #expect(SyncStatus.attention.isUrgent)
        #expect(SyncStatus.error.isUrgent)
        #expect(!SyncStatus.synced.isUrgent)
        #expect(!SyncStatus.syncing.isUrgent)
        #expect(!SyncStatus.starting.isUrgent)
        #expect(!SyncStatus.paused.isUrgent)
    }
}

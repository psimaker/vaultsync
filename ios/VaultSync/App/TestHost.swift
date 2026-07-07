import Foundation

/// True when this process hosts the unit-test runner. The embedded bridge is
/// process-global and the tests own it exclusively — they start real engines
/// and stop them under attached managers (#60/#61). If the host app managed
/// the engine lifecycle too, its manager would fight the tests: hold the
/// lifecycle lock, poll the bridge, and — since the #61 death detector —
/// auto-restart engines a test deliberately stopped, right into the test's
/// assertions. Every app-level code path that starts, adopts, or stops the
/// engine must bail behind this flag.
enum TestHost {
    static let isActive = NSClassFromString("XCTestCase") != nil
}

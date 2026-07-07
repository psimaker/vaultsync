import Testing

/// Umbrella for every suite that mutates the process-global Syncthing bridge
/// (starting or stopping the real embedded engine). Swift Testing runs
/// top-level suites concurrently in one process, so two bridge-state suites
/// would race: one suite's `TestSupport.resetSyncthingState()` stops the
/// engine another suite just started. The `.serialized` trait is recursive —
/// declaring a suite inside an extension of this enum is what guarantees it
/// never overlaps with the others.
@Suite(.serialized)
enum EngineBridgeSuites {}

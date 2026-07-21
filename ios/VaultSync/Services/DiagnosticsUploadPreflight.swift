import Foundation

struct DiagnosticsUploadPreflight: Equatable, Sendable {
    let folderID: String
    let folderPath: String
    let peerID: String
    let engineGeneration: Int64
    let engineRunning: Bool
    let pathsSettled: Bool
    let folderMode: String
    let folderPaused: Bool
    let folderHealthy: Bool
    let designatedPeerIDs: [String]
    let peerConnected: Bool
    let peerPaused: Bool
    let pathOverlap: Bool
    let namespacePathAllowed: Bool
    let operationSlotEmpty: Bool

    func validate(
        record: DiagnosticsPairingRecord,
        requireEmptySlot: Bool
    ) throws {
        guard engineRunning,
              engineGeneration > 0,
              peerConnected else {
            throw DiagnosticsProtocolError.unavailable
        }
        guard pathsSettled,
              folderID == record.folderID,
              peerID == record.homeserverDeviceID,
              !folderPath.isEmpty,
              folderPath.hasPrefix("/"),
              folderMode == "sendreceive",
              !folderPaused,
              folderHealthy,
              designatedPeerIDs == [peerID],
              !peerPaused,
              !pathOverlap,
              namespacePathAllowed,
              !requireEmptySlot || operationSlotEmpty else {
            throw DiagnosticsProtocolError.unsupported
        }
    }

    func sameRuntimeBoundary(as initial: DiagnosticsUploadPreflight) -> Bool {
        var current = self
        var expected = initial
        current = current.withoutSlotVerdict()
        expected = expected.withoutSlotVerdict()
        return current == expected
    }

    private func withoutSlotVerdict() -> DiagnosticsUploadPreflight {
        DiagnosticsUploadPreflight(
            folderID: folderID,
            folderPath: folderPath,
            peerID: peerID,
            engineGeneration: engineGeneration,
            engineRunning: engineRunning,
            pathsSettled: pathsSettled,
            folderMode: folderMode,
            folderPaused: folderPaused,
            folderHealthy: folderHealthy,
            designatedPeerIDs: designatedPeerIDs,
            peerConnected: peerConnected,
            peerPaused: peerPaused,
            pathOverlap: pathOverlap,
            namespacePathAllowed: namespacePathAllowed,
            operationSlotEmpty: false
        )
    }
}

extension SyncthingManager {
    func diagnosticsUploadPreflight(
        folderID: String,
        peerID: String,
        installationComponent: String,
        operationComponent: String,
        requireEmptySlot: Bool
    ) -> DiagnosticsUploadPreflight {
        let folder = folders.first { $0.id == folderID }
        let peer = devices.first { $0.deviceID == peerID }
        let overlap = PathCollisionGuard.overlappingFolderIDs(
            folders.map { (id: $0.id, path: $0.path) },
            canonicalize: FolderPathReconciler.canonical
        ).contains(folderID)
        let pathAllowed = SyncBridgeService.diagnosticsUploadPathAllowed(
            folderID: folderID,
            installationComponent: installationComponent,
            operationComponent: operationComponent
        )
        let slotEmpty = requireEmptySlot && SyncBridgeService.diagnosticsUploadPathAvailable(
            folderID: folderID,
            installationComponent: installationComponent,
            operationComponent: operationComponent
        )
        return DiagnosticsUploadPreflight(
            folderID: folderID,
            folderPath: folder?.path ?? "",
            peerID: peerID,
            engineGeneration: SyncBridgeService.eventStreamGeneration(),
            engineRunning: isRunning,
            pathsSettled: pathSettlement.settled,
            folderMode: folder?.type ?? "",
            folderPaused: folder?.paused ?? true,
            folderHealthy: folderStatuses[folderID].map { $0.state != "error" } ?? false,
            designatedPeerIDs: folder?.deviceIDs.sorted() ?? [],
            peerConnected: peer?.connected ?? false,
            peerPaused: peer?.paused ?? true,
            pathOverlap: overlap,
            namespacePathAllowed: pathAllowed,
            operationSlotEmpty: slotEmpty
        )
    }
}

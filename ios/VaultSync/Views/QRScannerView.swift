@preconcurrency import AVFoundation
import os
import SwiftUI
import UIKit

struct QRScannerView: View {
    let onScan: (String) -> Void
    let title: String
    let deniedMessage: String
    let unavailableMessage: String
    let manualButtonTitle: String
    @Environment(\.dismiss) private var dismiss
    @State private var cameraPermission: AVAuthorizationStatus = .notDetermined
    /// Camera permission granted but the capture session could not be built
    /// (no device/input/output — Simulator, hardware fault, MDM policy).
    /// Without this the sheet stayed a silent black screen (#95).
    @State private var cameraSetupFailed = false

    init(
        title: String = L10n.tr("Scan QR Code"),
        deniedMessage: String = L10n.tr("VaultSync needs camera access to scan Syncthing Device ID QR codes. Please enable it in Settings."),
        unavailableMessage: String = L10n.tr("The camera could not be started on this device. Enter the Device ID manually instead — in Syncthing on your computer, choose Actions → Show ID."),
        manualButtonTitle: String = L10n.tr("Enter Device ID Manually"),
        onScan: @escaping (String) -> Void
    ) {
        self.title = title
        self.deniedMessage = deniedMessage
        self.unavailableMessage = unavailableMessage
        self.manualButtonTitle = manualButtonTitle
        self.onScan = onScan
    }

    var body: some View {
        NavigationStack {
            Group {
                switch cameraPermission {
                case .authorized:
                    if cameraSetupFailed {
                        cameraUnavailableView
                    } else {
                        CameraPreview(
                            onScan: { code in
                                onScan(code)
                                dismiss()
                            },
                            onSetupFailure: {
                                cameraSetupFailed = true
                            }
                        )
                        .ignoresSafeArea()
                    }
                case .denied, .restricted:
                    permissionDeniedView
                default:
                    ProgressView("Requesting camera access…")
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        dismiss()
                    }
                }
            }
        }
        .onAppear {
            checkPermission()
        }
    }

    private var permissionDeniedView: some View {
        VStack(spacing: VaultSpacing.l) {
            Image(systemName: "camera.fill")
                .font(.largeTitle)
                .imageScale(.large)
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)

            Text("Camera Access Required")
                .font(.title3.bold())

            Text(deniedMessage)
                .font(.body)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, VaultSpacing.xl)

            Button("Open Settings") {
                if let url = URL(string: UIApplication.openSettingsURLString) {
                    UIApplication.shared.open(url)
                }
            }
            .buttonStyle(.borderedProminent)
        }
        .accessibilityElement(children: .contain)
    }

    private var cameraUnavailableView: some View {
        VStack(spacing: VaultSpacing.l) {
            Image(systemName: "video.slash.fill")
                .font(.largeTitle)
                .imageScale(.large)
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)

            Text(L10n.tr("Camera Unavailable"))
                .font(.title3.bold())

            Text(unavailableMessage)
                .font(.body)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, VaultSpacing.xl)

            Button(manualButtonTitle) {
                dismiss()
            }
            .buttonStyle(.borderedProminent)
        }
        .accessibilityElement(children: .contain)
    }

    private func checkPermission() {
        let status = AVCaptureDevice.authorizationStatus(for: .video)
        if status == .notDetermined {
            AVCaptureDevice.requestAccess(for: .video) { granted in
                Task { @MainActor in
                    cameraPermission = granted ? .authorized : .denied
                }
            }
        } else {
            cameraPermission = status
        }
    }
}

// MARK: - Camera Preview (UIViewRepresentable)

private struct CameraPreview: UIViewRepresentable {
    let onScan: (String) -> Void
    let onSetupFailure: () -> Void

    func makeUIView(context: Context) -> CameraPreviewUIView {
        let view = CameraPreviewUIView(onScan: onScan, onSetupFailure: onSetupFailure)
        return view
    }

    func updateUIView(_ uiView: CameraPreviewUIView, context: Context) {}

}

private final class ScannerCoordinator: NSObject, AVCaptureMetadataOutputObjectsDelegate, @unchecked Sendable {
    private let onScan: (String) -> Void
    private let captureSession: AVCaptureSession
    private let didScan = OSAllocatedUnfairLock(initialState: false)

    init(onScan: @escaping (String) -> Void, captureSession: AVCaptureSession) {
        self.onScan = onScan
        self.captureSession = captureSession
        super.init()
    }

    func metadataOutput(
        _ output: AVCaptureMetadataOutput,
        didOutput metadataObjects: [AVMetadataObject],
        from connection: AVCaptureConnection
    ) {
        guard let object = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
              object.type == .qr,
              let value = object.stringValue,
              !value.isEmpty else { return }
        let shouldHandle = didScan.withLock { scanned -> Bool in
            if scanned { return false }
            scanned = true
            return true
        }
        guard shouldHandle else { return }

        captureSession.stopRunning()
        DispatchQueue.main.async { [onScan] in
            onScan(value)
        }
    }
}

private final class CameraPreviewUIView: UIView {
    private nonisolated(unsafe) let captureSession = AVCaptureSession()
    private let sessionQueue = DispatchQueue(label: "eu.vaultsync.qr-scanner", qos: .userInitiated)
    private var previewLayer: AVCaptureVideoPreviewLayer?
    private var coordinator: ScannerCoordinator?

    private let onSetupFailure: () -> Void

    init(onScan: @escaping (String) -> Void, onSetupFailure: @escaping () -> Void) {
        self.onSetupFailure = onSetupFailure
        super.init(frame: .zero)
        coordinator = ScannerCoordinator(onScan: onScan, captureSession: captureSession)
        setupCamera()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not supported")
    }

    private func setupCamera() {
        // Report asynchronously: setupCamera runs inside makeUIView, and the
        // callback mutates SwiftUI @State — mutating state mid-view-update is
        // undefined, so the failure surfaces on the next runloop turn.
        func reportFailure() {
            DispatchQueue.main.async { [onSetupFailure] in onSetupFailure() }
        }

        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device),
              captureSession.canAddInput(input) else {
            reportFailure()
            return
        }
        captureSession.addInput(input)

        let metadataOutput = AVCaptureMetadataOutput()
        guard captureSession.canAddOutput(metadataOutput) else {
            reportFailure()
            return
        }
        captureSession.addOutput(metadataOutput)
        metadataOutput.setMetadataObjectsDelegate(coordinator, queue: sessionQueue)
        metadataOutput.metadataObjectTypes = [.qr]

        let preview = AVCaptureVideoPreviewLayer(session: captureSession)
        preview.videoGravity = .resizeAspectFill
        layer.addSublayer(preview)
        previewLayer = preview

        sessionQueue.async { [captureSession] in
            captureSession.startRunning()
        }
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        previewLayer?.frame = bounds
    }

    deinit {
        let session = captureSession
        if session.isRunning {
            session.stopRunning()
        }
    }
}

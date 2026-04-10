@preconcurrency import AVFoundation
import os
import SwiftUI
import UIKit

struct QRScannerView: View {
    let onScan: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var cameraPermission: AVAuthorizationStatus = .notDetermined

    var body: some View {
        NavigationStack {
            Group {
                switch cameraPermission {
                case .authorized:
                    CameraPreview(onScan: { code in
                        onScan(code)
                        dismiss()
                    })
                    .ignoresSafeArea()
                case .denied, .restricted:
                    permissionDeniedView
                default:
                    ProgressView("Requesting camera access…")
                }
            }
            .navigationTitle("Scan QR Code")
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
        VStack(spacing: 16) {
            Image(systemName: "camera.fill")
                .font(.system(size: 48))
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)

            Text("Camera Access Required")
                .font(.title3.bold())

            Text("VaultSync needs camera access to scan Syncthing Device ID QR codes. Please enable it in Settings.")
                .font(.body)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)

            Button("Open Settings") {
                if let url = URL(string: UIApplication.openSettingsURLString) {
                    UIApplication.shared.open(url)
                }
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

    func makeUIView(context: Context) -> CameraPreviewUIView {
        let view = CameraPreviewUIView(onScan: onScan)
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
        let alreadyScanned = didScan.withLock { scanned -> Bool in
            if scanned { return true }
            scanned = true
            return false
        }
        guard !alreadyScanned,
              let object = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
              object.type == .qr,
              let value = object.stringValue,
              !value.isEmpty else { return }

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

    init(onScan: @escaping (String) -> Void) {
        super.init(frame: .zero)
        coordinator = ScannerCoordinator(onScan: onScan, captureSession: captureSession)
        setupCamera()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not supported")
    }

    private func setupCamera() {
        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device) else { return }

        if captureSession.canAddInput(input) {
            captureSession.addInput(input)
        }

        let metadataOutput = AVCaptureMetadataOutput()
        if captureSession.canAddOutput(metadataOutput) {
            captureSession.addOutput(metadataOutput)
            metadataOutput.setMetadataObjectsDelegate(coordinator, queue: sessionQueue)
            metadataOutput.metadataObjectTypes = [.qr]
        }

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

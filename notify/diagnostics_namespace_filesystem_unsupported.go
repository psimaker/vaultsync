//go:build !linux

package main

func openDiagnosticsNamespaceRoot(_ string, _ *diagnosticsNamespaceFileIdentity) (*diagnosticsNamespaceRootHandle, error) {
	return nil, errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) Close() error {
	return errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) CreateDirectory(_ diagnosticsNamespacePath) error {
	return errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) CreateImmutable(_ diagnosticsNamespacePath, _ []byte) (diagnosticsNamespaceOwnedArtifact, error) {
	return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) CreateImmutableAtomic(_ diagnosticsNamespacePath, _ []byte) (diagnosticsNamespaceOwnedArtifact, error) {
	return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) ReadImmutable(_ diagnosticsNamespacePath) ([]byte, diagnosticsNamespaceFileIdentity, error) {
	return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) CleanupOwned(_ []diagnosticsNamespaceOwnedArtifact) (diagnosticsNamespaceCleanupResult, error) {
	return diagnosticsNamespaceCleanupResult{}, errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) ScanFixedLayout() error {
	return errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) ScanFixedLayoutDuringHelperRotation() error {
	return errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) ValidateRootRecord(_ diagnosticsNamespaceRootRecord) error {
	return errDiagnosticsNamespaceUnsupported
}

func (handle *diagnosticsNamespaceRootHandle) InstallationCount() (int, error) {
	return 0, errDiagnosticsNamespaceUnsupported
}

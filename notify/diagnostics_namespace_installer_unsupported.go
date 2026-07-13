//go:build !linux

package main

func prepareDiagnosticsNamespaceExplicit(_ diagnosticsNamespacePreparationRequest) (diagnosticsNamespaceRootRecord, error) {
	return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
}

func appendDiagnosticsNamespaceHelperEpoch(_ *diagnosticsNamespaceRootHandle, _ diagnosticsNamespaceChain) error {
	return errDiagnosticsNamespaceUnsupported
}

func installDiagnosticsNamespaceAuthorization(_ *diagnosticsNamespaceRootHandle, _ diagnosticsNamespaceChain, _ int) error {
	return errDiagnosticsNamespaceUnsupported
}

func appendDiagnosticsNamespaceAuthorizationEpoch(_ *diagnosticsNamespaceRootHandle, _ diagnosticsNamespaceChain, _ int) error {
	return errDiagnosticsNamespaceUnsupported
}

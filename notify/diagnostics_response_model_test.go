package main

import "testing"

type diagnosticsResponseModelEvent uint8

const (
	diagnosticsResponseModelWeakSignal diagnosticsResponseModelEvent = iota
	diagnosticsResponseModelInstallUploadChain
	diagnosticsResponseModelAuthorize
	diagnosticsResponseModelPersistResponse
	diagnosticsResponseModelCleanup
	diagnosticsResponseModelHelperRestart
)

type diagnosticsResponseFoundationModel struct {
	uploadChainAuthenticated   bool
	authorizationAccepted      bool
	responsePersisted          bool
	responseCausallyAuthorized bool
	cleanupAttempted           bool
	downloadObserved           bool
	roundtripConfirmed         bool
}

func (model *diagnosticsResponseFoundationModel) apply(event diagnosticsResponseModelEvent) {
	switch event {
	case diagnosticsResponseModelInstallUploadChain:
		model.uploadChainAuthenticated = true
	case diagnosticsResponseModelAuthorize:
		model.authorizationAccepted = model.uploadChainAuthenticated
	case diagnosticsResponseModelPersistResponse:
		if model.uploadChainAuthenticated && model.authorizationAccepted {
			model.responsePersisted = true
			model.responseCausallyAuthorized = true
		}
	case diagnosticsResponseModelCleanup:
		model.cleanupAttempted = true
	case diagnosticsResponseModelHelperRestart:
		model.authorizationAccepted = false
	case diagnosticsResponseModelWeakSignal:
		// HTTP status, timestamp, Relay/APNs/StoreKit state, capability reachability,
		// cleanup, and file presence cannot create app download or roundtrip evidence.
	}
}

func TestDiagnosticsResponseFoundationModelRequiresCausalAuthorizationAndNeverCreatesAppEvidence(t *testing.T) {
	seed := uint64(0x02460009)
	for sequence := 0; sequence < 4096; sequence++ {
		model := diagnosticsResponseFoundationModel{}
		for step := 0; step < 32; step++ {
			seed = seed*6364136223846793005 + 1442695040888963407
			event := diagnosticsResponseModelEvent(seed % uint64(diagnosticsResponseModelHelperRestart+1))
			model.apply(event)
			if model.responsePersisted && (!model.uploadChainAuthenticated || !model.responseCausallyAuthorized) {
				t.Fatalf("sequence %d step %d persisted response without causal upload/authorization: %+v", sequence, step, model)
			}
			if model.downloadObserved || model.roundtripConfirmed {
				t.Fatalf("sequence %d step %d created out-of-scope app evidence: %+v", sequence, step, model)
			}
		}
	}
}

func TestDiagnosticsResponseCleanupIsEvidenceOrthogonalAcrossRestart(t *testing.T) {
	model := diagnosticsResponseFoundationModel{}
	for _, event := range []diagnosticsResponseModelEvent{
		diagnosticsResponseModelInstallUploadChain,
		diagnosticsResponseModelAuthorize,
		diagnosticsResponseModelPersistResponse,
		diagnosticsResponseModelCleanup,
		diagnosticsResponseModelHelperRestart,
		diagnosticsResponseModelCleanup,
	} {
		model.apply(event)
	}
	if !model.responsePersisted || !model.cleanupAttempted || model.downloadObserved || model.roundtripConfirmed {
		t.Fatalf("cleanup/restart changed evidence truth: %+v", model)
	}
}

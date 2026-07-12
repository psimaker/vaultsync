package main

const (
	diagnosticsPairingCapabilityID   = "eu.vaultsync.diagnostics.helper-pairing/1"
	diagnosticsNamespaceCapabilityID = "eu.vaultsync.diagnostics.namespace/1"
	diagnosticsRoundtripCapabilityID = "eu.vaultsync.diagnostics.correlated-roundtrip/1"

	diagnosticsProtocolMajor         = uint64(1)
	diagnosticsCryptographicSuite    = uint64(1)
	diagnosticsRoundtripRequiredBits = uint64(0x0f)
)

// diagnosticsCapabilityDisposition is deliberately limited to the two honest
// pre-activation outcomes. M2 has no ready/available state and no transition
// that can enable one.
type diagnosticsCapabilityDisposition uint8

const (
	diagnosticsCapabilityUnavailable diagnosticsCapabilityDisposition = iota
	diagnosticsCapabilityUnsupported
)

// diagnosticsCapabilityReason is a bounded local category. It never contains
// a requested identifier, key, binding, path, nonce, digest, or body.
type diagnosticsCapabilityReason uint8

const (
	diagnosticsCapabilityHelperMissing diagnosticsCapabilityReason = iota
	diagnosticsCapabilityDisabled
	diagnosticsCapabilityUnknown
)

type diagnosticsCapabilityDescriptor struct {
	identifier    string
	protocolMajor uint64
	suite         uint64
	requiredFlags uint64
}

type diagnosticsCapabilityStatus struct {
	disposition diagnosticsCapabilityDisposition
	reason      diagnosticsCapabilityReason
	descriptor  diagnosticsCapabilityDescriptor
}

// diagnosticsCapabilityFoundation is a side-effect-free catalog. Its zero
// value models a legacy helper with no capability foundation. The constructor
// models a new helper that knows the approved contracts but keeps every one of
// them disabled. There is intentionally no mutator or activation method.
type diagnosticsCapabilityFoundation struct {
	descriptors [3]diagnosticsCapabilityDescriptor
}

func newDormantDiagnosticsCapabilityFoundation() diagnosticsCapabilityFoundation {
	return diagnosticsCapabilityFoundation{descriptors: [3]diagnosticsCapabilityDescriptor{
		{
			identifier:    diagnosticsPairingCapabilityID,
			protocolMajor: diagnosticsProtocolMajor,
			suite:         diagnosticsCryptographicSuite,
		},
		{
			identifier:    diagnosticsNamespaceCapabilityID,
			protocolMajor: diagnosticsProtocolMajor,
			suite:         diagnosticsCryptographicSuite,
		},
		{
			identifier:    diagnosticsRoundtripCapabilityID,
			protocolMajor: diagnosticsProtocolMajor,
			suite:         diagnosticsCryptographicSuite,
			requiredFlags: diagnosticsRoundtripRequiredBits,
		},
	}}
}

// status is local model evaluation only. No runtime path currently calls it,
// advertises it, serializes it, logs it, or exposes it over a listener.
func (foundation diagnosticsCapabilityFoundation) status(identifier string) diagnosticsCapabilityStatus {
	for _, descriptor := range foundation.descriptors {
		if descriptor.identifier != "" && descriptor.identifier == identifier {
			return diagnosticsCapabilityStatus{
				disposition: diagnosticsCapabilityUnavailable,
				reason:      diagnosticsCapabilityDisabled,
				descriptor:  descriptor,
			}
		}
	}

	if isKnownDiagnosticsCapabilityIdentifier(identifier) {
		return diagnosticsCapabilityStatus{
			disposition: diagnosticsCapabilityUnavailable,
			reason:      diagnosticsCapabilityHelperMissing,
		}
	}

	return diagnosticsCapabilityStatus{
		disposition: diagnosticsCapabilityUnsupported,
		reason:      diagnosticsCapabilityUnknown,
	}
}

// catalog returns a value copy so callers cannot mutate the dormant runtime
// catalog through an alias.
func (foundation diagnosticsCapabilityFoundation) catalog() [3]diagnosticsCapabilityDescriptor {
	return foundation.descriptors
}

func isKnownDiagnosticsCapabilityIdentifier(identifier string) bool {
	switch identifier {
	case diagnosticsPairingCapabilityID,
		diagnosticsNamespaceCapabilityID,
		diagnosticsRoundtripCapabilityID:
		return true
	default:
		return false
	}
}

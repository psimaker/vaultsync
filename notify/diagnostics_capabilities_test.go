package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticsCapabilityCatalogIsExactAndDormant(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	foundation := newDormantDiagnosticsCapabilityFoundation()
	catalog := foundation.catalog()

	expectedFlags := map[string]uint64{
		fixture.Capabilities["pairing"]:   0,
		fixture.Capabilities["namespace"]: 0,
		fixture.Capabilities["roundtrip"]: diagnosticsRoundtripRequiredBits,
	}
	if len(catalog) != len(expectedFlags) {
		t.Fatalf("catalog size = %d, want %d", len(catalog), len(expectedFlags))
	}

	seen := make(map[string]bool, len(catalog))
	for _, descriptor := range catalog {
		wantFlags, ok := expectedFlags[descriptor.identifier]
		if !ok {
			t.Fatalf("unexpected diagnostics capability %q", descriptor.identifier)
		}
		if seen[descriptor.identifier] {
			t.Fatalf("duplicate diagnostics capability %q", descriptor.identifier)
		}
		seen[descriptor.identifier] = true
		if descriptor.protocolMajor != diagnosticsProtocolMajor || descriptor.suite != diagnosticsCryptographicSuite {
			t.Fatalf("capability %q version/suite = %d/%d, want %d/%d", descriptor.identifier, descriptor.protocolMajor, descriptor.suite, diagnosticsProtocolMajor, diagnosticsCryptographicSuite)
		}
		if descriptor.requiredFlags != wantFlags {
			t.Fatalf("capability %q flags = %#x, want %#x", descriptor.identifier, descriptor.requiredFlags, wantFlags)
		}

		status := foundation.status(descriptor.identifier)
		if status.disposition != diagnosticsCapabilityUnavailable || status.reason != diagnosticsCapabilityDisabled {
			t.Fatalf("capability %q is not dormant: %+v", descriptor.identifier, status)
		}
		if status.descriptor != descriptor {
			t.Fatalf("capability %q status descriptor changed", descriptor.identifier)
		}
	}
}

func TestDiagnosticsCapabilityZeroValueUpgradeAndDowngradeStayUnavailable(t *testing.T) {
	var legacy diagnosticsCapabilityFoundation
	upgraded := newDormantDiagnosticsCapabilityFoundation()
	var downgraded diagnosticsCapabilityFoundation

	for _, identifier := range []string{
		diagnosticsPairingCapabilityID,
		diagnosticsNamespaceCapabilityID,
		diagnosticsRoundtripCapabilityID,
	} {
		before := legacy.status(identifier)
		afterUpgrade := upgraded.status(identifier)
		afterDowngrade := downgraded.status(identifier)

		if before.disposition != diagnosticsCapabilityUnavailable || before.reason != diagnosticsCapabilityHelperMissing {
			t.Fatalf("legacy helper status for %q = %+v", identifier, before)
		}
		if afterUpgrade.disposition != diagnosticsCapabilityUnavailable || afterUpgrade.reason != diagnosticsCapabilityDisabled {
			t.Fatalf("upgraded helper status for %q = %+v", identifier, afterUpgrade)
		}
		if afterDowngrade != before {
			t.Fatalf("downgrade status for %q = %+v, want legacy %+v", identifier, afterDowngrade, before)
		}
	}
}

func TestDiagnosticsCapabilityUnknownInputIsUnsupportedAndNotReflected(t *testing.T) {
	const sentinel = "SENTINEL-PRIVATE-CAPABILITY-INPUT/2"
	status := newDormantDiagnosticsCapabilityFoundation().status(sentinel)
	if status.disposition != diagnosticsCapabilityUnsupported || status.reason != diagnosticsCapabilityUnknown {
		t.Fatalf("unknown capability status = %+v", status)
	}
	if status.descriptor != (diagnosticsCapabilityDescriptor{}) {
		t.Fatalf("unknown capability must not acquire a descriptor: %+v", status.descriptor)
	}
	if strings.Contains(fmt.Sprintf("%+v", status), sentinel) {
		t.Fatal("unknown capability input was reflected into local status")
	}
}

func TestLoadConfigInstallsOnlyDormantDiagnosticsCapabilities(t *testing.T) {
	t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
	t.Setenv("SYNCTHING_API_KEY", "test-key")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")
	t.Setenv("STARTUP_ANNOUNCE", "false")
	t.Setenv("STALE_RETRIGGER_SECONDS", "0")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned unexpected error: %v", err)
	}
	for _, descriptor := range cfg.diagnosticsCapabilities.catalog() {
		status := cfg.diagnosticsCapabilities.status(descriptor.identifier)
		if status.disposition != diagnosticsCapabilityUnavailable || status.reason != diagnosticsCapabilityDisabled {
			t.Fatalf("loaded capability %q is not dormant: %+v", descriptor.identifier, status)
		}
	}
}

func TestDiagnosticsCapabilityRuntimeFileHasNoActivationSurface(t *testing.T) {
	notifyRoot := filepath.Join(diagnosticsTestRepoRoot(t), "notify")
	path := filepath.Join(notifyRoot, "diagnostics_capabilities.go")
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse runtime capability foundation: %v", err)
	}
	if len(file.Imports) != 0 {
		t.Fatalf("dormant capability foundation must not import filesystem, network, logging, encoding, or persistence packages: %v", file.Imports)
	}

	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := strings.ToLower(function.Name.Name)
		for _, forbidden := range []string{
			"activate",
			"enable",
			"listen",
			"serve",
			"pair",
			"persist",
			"probe",
			"write",
		} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("dormant capability foundation exposes activating function %q", function.Name.Name)
			}
		}
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime capability foundation: %v", err)
	}
	for _, forbidden := range []string{
		"http://",
		"https://",
		"/api/",
		"VAULTSYNC_",
		"SYNCTHING_",
		"RELAY_",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("dormant capability foundation contains operational carrier %q", forbidden)
		}
	}

	mainBody, err := os.ReadFile(filepath.Join(notifyRoot, "main.go"))
	if err != nil {
		t.Fatalf("read helper entrypoint: %v", err)
	}
	// One declaration plus one dormant constructor assignment. Any third use
	// means a runtime path started consulting the catalog and needs a later
	// milestone's explicit review.
	if got := strings.Count(string(mainBody), "diagnosticsCapabilities"); got != 2 {
		t.Fatalf("helper entrypoint references diagnostics capabilities %d times, want exactly dormant declaration and construction", got)
	}
}

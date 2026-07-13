package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDiagnosticsNamespacePathsHaveOnlyFixedShapes(t *testing.T) {
	installation := bytes.Repeat([]byte{0x61}, 32)
	operation := bytes.Repeat([]byte{0x62}, 32)
	authorizationPaths, err := diagnosticsNamespaceAuthorizationPaths(installation)
	if err != nil {
		t.Fatal(err)
	}
	helperEpoch, _ := diagnosticsNamespaceHelperEpochPath(2)
	rotatedHelperEpoch, _ := diagnosticsNamespaceHelperEpochPath(42)
	authorizationEpoch, _ := diagnosticsNamespaceAuthorizationEpochPath(installation, 2)
	operationPath, _ := diagnosticsNamespaceOperationPath(installation, operation, 1)
	valid := []diagnosticsNamespacePath{
		diagnosticsNamespaceReadmePath(), diagnosticsNamespaceRootManifestPath(), helperEpoch, rotatedHelperEpoch,
		authorizationPaths[0], authorizationPaths[1], authorizationPaths[2], authorizationEpoch, operationPath,
		{components: []string{diagnosticsNamespaceManifestEpochsName}, persistent: true},
		{components: []string{diagnosticsNamespaceInstallationsName}, persistent: true},
		{components: authorizationPaths[0].components[:2], persistent: true},
	}
	for _, path := range valid {
		if !path.valid() {
			t.Fatalf("fixed path rejected: %#v", path)
		}
	}

	for _, components := range [][]string{
		{"..", "user-note.md"}, {"/absolute"}, {"README.txt", "child"},
		{diagnosticsNamespaceInstallationsName, "con", diagnosticsNamespaceOperationsName, "note.md"},
		{diagnosticsNamespaceInstallationsName, strings.Repeat("a", 52), diagnosticsNamespaceOperationsName, "../request.cbor"},
		{diagnosticsNamespaceManifestEpochsName, "01.helper-manifest.cbor"},
		{diagnosticsNamespaceManifestEpochsName, "2.authorization.cbor"},
	} {
		if (diagnosticsNamespacePath{components: components}).valid() {
			t.Fatalf("path-like or non-protocol shape accepted: %#v", components)
		}
	}
}

func FuzzDiagnosticsNamespacePathShapes(f *testing.F) {
	f.Add("../note.md")
	f.Add("installations\x00con\x00operations\x00note.md")
	f.Add("manifest-epochs\x0001.helper-manifest.cbor")
	f.Fuzz(func(t *testing.T, input string) {
		components := strings.Split(input, "\x00")
		path := diagnosticsNamespacePath{components: components}
		if !path.valid() {
			return
		}
		for _, component := range components {
			if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "/\\:%") {
				t.Fatalf("unsafe component accepted: %#v", components)
			}
		}
	})
}

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiagnosticsDecision023RecordsMatchCrossLanguageGolden(t *testing.T) {
	actual := diagnosticsNamespaceGoldenMessages(t)
	if os.Getenv("VAULTSYNC_PRINT_M4_NAMESPACE_FIXTURE") == "1" {
		encoded, err := json.MarshalIndent(actual, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(string(encoded))
		return
	}
	path := filepath.Join(diagnosticsTestRepoRoot(t), "ios", "VaultSyncTests", "Fixtures", "diagnostics-namespace-m4.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Decision 023 namespace bytes differ from the shared fixture\nactual=%#v\nexpected=%#v", actual, expected)
	}
	if len(actual) != 5 {
		t.Fatalf("golden message count = %d, want 5", len(actual))
	}
}

func TestDiagnosticsDecision023DomainsAndSchemasAreExact(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	expectedDomains := expectedDiagnosticsDomains()
	domains := map[string]string{
		"namespace.enablement_request":           diagnosticsNamespaceDomains[diagnosticsNamespaceEnablement].app,
		"namespace.root_manifest":                diagnosticsNamespaceDomains[diagnosticsNamespaceRootManifest].helper,
		"namespace.helper_epoch_prior":           diagnosticsNamespaceDomains[diagnosticsNamespaceHelperEpoch].priorHelper,
		"namespace.helper_epoch_current":         diagnosticsNamespaceDomains[diagnosticsNamespaceHelperEpoch].currentHelper,
		"namespace.authorization_initial_app":    diagnosticsNamespaceDomains[diagnosticsNamespaceInitialAuthorization].app,
		"namespace.authorization_initial_helper": diagnosticsNamespaceDomains[diagnosticsNamespaceInitialAuthorization].helper,
		"namespace.authorization_epoch_app":      diagnosticsNamespaceDomains[diagnosticsNamespaceAuthorizationEpoch].app,
		"namespace.authorization_epoch_helper":   diagnosticsNamespaceDomains[diagnosticsNamespaceAuthorizationEpoch].helper,
	}
	for name, actual := range domains {
		if actual != expectedDomains[name] {
			t.Fatalf("domain %s = %q, want %q", name, actual, expectedDomains[name])
		}
	}
	registry := fixture.Registries["namespace"]
	for label, expected := range map[string]string{
		"1":   "capability:text=eu.vaultsync.diagnostics.namespace/1",
		"4":   "message_type:uint-enum=1..5",
		"8":   "installation_binding:bstr=32",
		"253": "app_signature:bstr=64",
		"254": "prior_helper_signature:bstr=64",
		"255": "helper_signature:bstr=64",
	} {
		if registry[label] != expected {
			t.Fatalf("namespace registry %s = %q, want %q", label, registry[label], expected)
		}
	}
}

func TestDiagnosticsDecision023RecordValidationFailsClosed(t *testing.T) {
	messages := diagnosticsNamespaceGoldenMessages(t)
	for name, encodedHex := range messages {
		encoded, err := hex.DecodeString(encodedHex)
		if err != nil {
			t.Fatal(err)
		}
		message, err := decodeDiagnosticsNamespaceMessage(encoded)
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		reencoded, err := encodeDiagnosticsCBOR(message.value)
		if err != nil || !bytes.Equal(reencoded, encoded) {
			t.Fatalf("%s did not roundtrip byte-exactly", name)
		}
		for _, mutation := range []func([]byte) []byte{
			func(value []byte) []byte { return append(append([]byte(nil), value...), 0) },
			func(value []byte) []byte {
				result := append([]byte(nil), value...)
				result[len(result)-1] ^= 1
				return result
			},
			func(value []byte) []byte {
				result := append([]byte(nil), value...)
				result[0] = 0xbf
				return result
			},
		} {
			if _, err := decodeDiagnosticsNamespaceMessage(mutation(encoded)); err == nil {
				t.Fatalf("%s accepted a malformed or tampered record", name)
			}
		}
	}

	fixture := diagnosticsNamespaceGoldenFixture(t)
	invalidLifetime := cloneDiagnosticsCBOR(fixture.enablementBody)
	diagnosticsNamespaceReplaceField(&invalidLifetime, 27, diagnosticsCBORUint(fixture.issuedAt+301))
	if _, err := signDiagnosticsNamespaceEnablement(invalidLifetime, fixture.initialApp); err == nil {
		t.Fatal("enablement accepted a lifetime above 300 seconds")
	}

	unknown := cloneDiagnosticsCBOR(fixture.enablementBody)
	unknown.fields = append(unknown.fields, diagnosticsCBORMapField(200, diagnosticsCBORUint(1)))
	if _, err := signDiagnosticsNamespaceEnablement(unknown, fixture.initialApp); err == nil {
		t.Fatal("enablement accepted an unknown field")
	}

	wrongKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x77}, ed25519.SeedSize))
	if _, err := signDiagnosticsNamespaceEnablement(fixture.enablementBody, wrongKey); err == nil {
		t.Fatal("enablement accepted a signature whose key did not match the declared app key")
	}
}

func TestDiagnosticsDecision023StableNamesRejectPathInput(t *testing.T) {
	identifier := bytes.Repeat([]byte{0xab}, 32)
	component, err := diagnosticsNamespaceComponent(identifier)
	if err != nil {
		t.Fatal(err)
	}
	if component != "vov2xk5lvov2xk5lvov2xk5lvov2xk5lvov2xk5lvov2xk5lvovq" ||
		component != strings.ToLower(component) || strings.ContainsAny(component, ".%/\\:") {
		t.Fatalf("unexpected canonical component %q", component)
	}
	decoded, err := parseDiagnosticsNamespaceComponent(component)
	if err != nil || !bytes.Equal(decoded, identifier) {
		t.Fatal("canonical component did not decode")
	}
	for _, invalid := range []string{
		"../" + component, strings.ToUpper(component), component + ".", component[:51],
		strings.Replace(component, "v", "%", 1), "con", "nul", "a:b", "a\\b", "a/b", "é",
	} {
		if _, err := parseDiagnosticsNamespaceComponent(invalid); err == nil {
			t.Fatalf("accepted path-like component %q", invalid)
		}
	}
	files, err := diagnosticsNamespaceOperationFilenames(identifier)
	if err != nil {
		t.Fatal(err)
	}
	if files != [3]string{component + ".request.cbor", component + ".attestation.cbor", component + ".response.cbor"} {
		t.Fatalf("unexpected operation filenames %#v", files)
	}
	if name, _ := diagnosticsNamespaceEpochFilename(2, false); name != "2.helper-manifest.cbor" {
		t.Fatalf("unexpected helper epoch filename %q", name)
	}
	if name, _ := diagnosticsNamespaceEpochFilename(2, true); name != "2.authorization.cbor" {
		t.Fatalf("unexpected authorization filename %q", name)
	}
	if name, _ := diagnosticsNamespaceEpochFilename(42, false); name != "42.helper-manifest.cbor" {
		t.Fatalf("rotated helper epoch filename was capped by history count: %q", name)
	}
	if _, err := diagnosticsNamespaceEpochFilename(1, false); err == nil {
		t.Fatal("initial helper epoch was accepted as an epoch-manifest filename")
	}
	if _, err := diagnosticsNamespaceEpochFilename(1, true); err == nil {
		t.Fatal("initial authorization epoch was accepted as an epoch-record filename")
	}
	if _, err := diagnosticsNamespaceEpochFilename(diagnosticsNamespaceMaximumAuthorizationEpochs+2, true); err == nil {
		t.Fatal("authorization history accepted an epoch beyond its fixed maximum")
	}
}

func FuzzDiagnosticsDecision023Components(f *testing.F) {
	f.Add(bytes.Repeat([]byte{0xab}, 32))
	f.Add([]byte("../VaultSync Diagnostics"))
	f.Fuzz(func(t *testing.T, value []byte) {
		component, err := diagnosticsNamespaceComponent(value)
		if len(value) != 32 {
			if err == nil {
				t.Fatal("non-32-byte identifier was accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := parseDiagnosticsNamespaceComponent(component)
		if err != nil || !bytes.Equal(decoded, value) {
			t.Fatal("component roundtrip failed")
		}
	})
}

func TestDiagnosticsDecision023ChainRejectsCopiesForksAndOverflow(t *testing.T) {
	fixture := diagnosticsNamespaceGoldenFixture(t)
	if err := validateDiagnosticsNamespaceChain(fixture.chain); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}
	if err := validateDiagnosticsNamespacePersistentChain(
		fixture.chain.RootManifest, fixture.chain.HelperEpochs, fixture.chain.Authorizations,
	); err != nil {
		t.Fatalf("valid persistent chain rejected: %v", err)
	}

	copied := fixture.chain
	copied.RootManifest = append([]byte(nil), fixture.chain.RootManifest...)
	copied.RootManifest[len(copied.RootManifest)-1] ^= 1
	if err := validateDiagnosticsNamespaceChain(copied); err == nil {
		t.Fatal("tampered copied root was accepted")
	}

	forked := fixture.chain
	forked.HelperEpochs = [][]byte{append([]byte(nil), fixture.chain.HelperEpochs[0]...), append([]byte(nil), fixture.chain.HelperEpochs[0]...)}
	if err := validateDiagnosticsNamespaceChain(forked); err == nil {
		t.Fatal("forked helper epoch chain was accepted")
	}
	if err := validateDiagnosticsNamespacePersistentChain(
		fixture.chain.RootManifest,
		[][]byte{fixture.chain.HelperEpochs[0], fixture.chain.HelperEpochs[0]},
		fixture.chain.Authorizations,
	); err == nil {
		t.Fatal("persisted duplicate helper epoch was accepted")
	}
	if err := validateDiagnosticsNamespacePersistentChain(
		fixture.chain.RootManifest,
		fixture.chain.HelperEpochs,
		[][][]byte{{fixture.chain.Authorizations[0][0], fixture.chain.Authorizations[0][1], fixture.chain.Authorizations[0][1]}},
	); err == nil {
		t.Fatal("persisted authorization fork was accepted")
	}

	overflow := fixture.chain
	overflow.Authorizations = make([][][]byte, diagnosticsNamespaceMaximumInstallations+1)
	for index := range overflow.Authorizations {
		overflow.Authorizations[index] = fixture.chain.Authorizations[0]
	}
	if err := validateDiagnosticsNamespaceChain(overflow); err == nil {
		t.Fatal("more than eight installations were accepted")
	}

	duplicate := fixture.chain
	duplicate.Authorizations = append(append([][][]byte(nil), fixture.chain.Authorizations...), fixture.chain.Authorizations[0])
	if err := validateDiagnosticsNamespaceChain(duplicate); err == nil {
		t.Fatal("duplicate installation binding was accepted")
	}
}

type diagnosticsNamespaceGoldenData struct {
	initialApp     ed25519.PrivateKey
	currentApp     ed25519.PrivateKey
	initialHelper  ed25519.PrivateKey
	currentHelper  ed25519.PrivateKey
	issuedAt       uint64
	enablementBody diagnosticsCBORValue
	chain          diagnosticsNamespaceChain
}

func diagnosticsNamespaceGoldenMessages(t testing.TB) map[string]string {
	t.Helper()
	fixture := diagnosticsNamespaceGoldenFixture(t)
	return map[string]string{
		"01_enablement":            hex.EncodeToString(fixture.chain.Enablement),
		"02_root_manifest":         hex.EncodeToString(fixture.chain.RootManifest),
		"03_helper_epoch":          hex.EncodeToString(fixture.chain.HelperEpochs[0]),
		"04_initial_authorization": hex.EncodeToString(fixture.chain.Authorizations[0][0]),
		"05_authorization_epoch":   hex.EncodeToString(fixture.chain.Authorizations[0][1]),
	}
}

func diagnosticsNamespaceGoldenFixture(t testing.TB) diagnosticsNamespaceGoldenData {
	t.Helper()
	initialApp := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	currentApp := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x32}, ed25519.SeedSize))
	initialHelper := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x41}, ed25519.SeedSize))
	currentHelper := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	initialAppPublic := initialApp.Public().(ed25519.PublicKey)
	initialAppKeyID := diagnosticsKeyID(initialAppPublic)
	currentAppPublic := currentApp.Public().(ed25519.PublicKey)
	currentAppKeyID := diagnosticsKeyID(currentAppPublic)
	initialHelperPublic := initialHelper.Public().(ed25519.PublicKey)
	initialHelperKeyID := diagnosticsKeyID(initialHelperPublic)
	currentHelperPublic := currentHelper.Public().(ed25519.PublicKey)
	currentHelperKeyID := diagnosticsKeyID(currentHelperPublic)
	homeserver := bytes.Repeat([]byte{0x05}, 32)
	folder := bytes.Repeat([]byte{0x06}, 32)
	namespaceID := bytes.Repeat([]byte{0x07}, 32)
	enablementNonce := bytes.Repeat([]byte{0x19}, 32)
	issuedAt := uint64(1_700_000_000)
	expiresAt := issuedAt + diagnosticsNamespaceMaximumCandidateLifetimeSecs
	createdAt := issuedAt + 20
	readmeDigest := sha256Bytes([]byte(diagnosticsNamespaceReadme))

	enablementBody := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsNamespaceEnablement)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
		diagnosticsCBORMapField(9, diagnosticsCBORBstr(initialAppKeyID[:])),
		diagnosticsCBORMapField(10, diagnosticsCBORBstr(initialAppPublic)),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(initialAppKeyID[:])),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(initialHelperPublic)),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(initialHelperKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(19, diagnosticsCBORBstr(enablementNonce)),
		diagnosticsCBORMapField(26, diagnosticsCBORUint(issuedAt)),
		diagnosticsCBORMapField(27, diagnosticsCBORUint(expiresAt)),
	)
	enablement, err := signDiagnosticsNamespaceEnablement(enablementBody, initialApp)
	if err != nil {
		t.Fatalf("enablement: %v", err)
	}
	enablementDigest, _ := diagnosticsNamespaceRecordDigest(enablement)
	rootBody := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsNamespaceRootManifest)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)), diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(namespaceID)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(initialHelperPublic)), diagnosticsCBORMapField(14, diagnosticsCBORBstr(initialHelperKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(1)), diagnosticsCBORMapField(19, diagnosticsCBORBstr(enablementNonce)),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(enablementDigest[:])), diagnosticsCBORMapField(28, diagnosticsCBORUint(createdAt)),
		diagnosticsCBORMapField(29, diagnosticsCBORBstr(readmeDigest)),
	)
	root, err := signDiagnosticsNamespaceRootManifest(rootBody, initialHelper)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	rootDigest, _ := diagnosticsNamespaceRecordDigest(root)
	epochBody := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsNamespaceHelperEpoch)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)), diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(namespaceID)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(currentHelperPublic)), diagnosticsCBORMapField(14, diagnosticsCBORBstr(currentHelperKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(2)), diagnosticsCBORMapField(16, diagnosticsCBORBstr(initialHelperPublic)),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(initialHelperKeyID[:])), diagnosticsCBORMapField(18, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(21, diagnosticsCBORBstr(rootDigest[:])), diagnosticsCBORMapField(22, diagnosticsCBORBstr(rootDigest[:])),
		diagnosticsCBORMapField(28, diagnosticsCBORUint(createdAt+1)), diagnosticsCBORMapField(29, diagnosticsCBORBstr(readmeDigest)),
	)
	helperEpoch, err := signDiagnosticsNamespaceHelperEpoch(epochBody, initialHelper, currentHelper)
	if err != nil {
		t.Fatalf("helper epoch: %v", err)
	}
	helperEpochDigest, _ := diagnosticsNamespaceRecordDigest(helperEpoch)
	installation, _ := diagnosticsNamespaceInstallationBinding(initialAppKeyID[:], homeserver, folder)
	initialAuthorizationBody := diagnosticsNamespaceAuthorizationBody(
		diagnosticsNamespaceInitialAuthorization, homeserver, folder, namespaceID, installation[:], initialAppKeyID[:],
		initialAppPublic, initialAppKeyID[:], 1, currentHelperPublic, currentHelperKeyID[:], 2,
		rootDigest[:], helperEpochDigest[:], nil, bytes.Repeat([]byte{0x25}, 32), issuedAt+2, expiresAt+2, bytes.Repeat([]byte{0x30}, 32), 1,
	)
	initialCandidate, err := signDiagnosticsNamespaceAuthorizationCandidate(initialAuthorizationBody, initialApp)
	if err != nil {
		t.Fatalf("initial authorization candidate: %v", err)
	}
	initialAuthorization, err := countersignDiagnosticsNamespaceAuthorization(initialCandidate, currentHelper)
	if err != nil {
		t.Fatalf("initial authorization helper signature: %v", err)
	}
	initialAuthorizationDigest, _ := diagnosticsNamespaceRecordDigest(initialAuthorization)
	epochAuthorizationBody := diagnosticsNamespaceAuthorizationBody(
		diagnosticsNamespaceAuthorizationEpoch, homeserver, folder, namespaceID, installation[:], initialAppKeyID[:],
		currentAppPublic, currentAppKeyID[:], 2, currentHelperPublic, currentHelperKeyID[:], 2,
		rootDigest[:], helperEpochDigest[:], initialAuthorizationDigest[:], bytes.Repeat([]byte{0x26}, 32), issuedAt+3, expiresAt+3, bytes.Repeat([]byte{0x31}, 32), 2,
	)
	epochCandidate, err := signDiagnosticsNamespaceAuthorizationCandidate(epochAuthorizationBody, currentApp)
	if err != nil {
		t.Fatalf("authorization epoch candidate: %v", err)
	}
	epochAuthorization, err := countersignDiagnosticsNamespaceAuthorization(epochCandidate, currentHelper)
	if err != nil {
		t.Fatalf("authorization epoch helper signature: %v", err)
	}

	return diagnosticsNamespaceGoldenData{
		initialApp: initialApp, currentApp: currentApp, initialHelper: initialHelper, currentHelper: currentHelper,
		issuedAt: issuedAt, enablementBody: enablementBody,
		chain: diagnosticsNamespaceChain{
			Enablement: enablement, RootManifest: root, HelperEpochs: [][]byte{helperEpoch},
			Authorizations: [][][]byte{{initialAuthorization, epochAuthorization}},
		},
	}
}

func diagnosticsNamespaceAuthorizationBody(messageType uint64, homeserver, folder, namespaceID, installation, initialAppKeyID,
	currentAppPublic, currentAppKeyID []byte, appEpoch uint64, currentHelperPublic, currentHelperKeyID []byte, helperEpoch uint64,
	rootDigest, helperManifestDigest, priorAuthorizationDigest, credentialDigest []byte, issuedAt, expiresAt uint64, nonce []byte, authorizationEpoch uint64,
) diagnosticsCBORValue {
	fields := []diagnosticsCBORField{
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(messageType)), diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)), diagnosticsCBORMapField(7, diagnosticsCBORBstr(namespaceID)),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(installation)), diagnosticsCBORMapField(9, diagnosticsCBORBstr(initialAppKeyID)),
		diagnosticsCBORMapField(10, diagnosticsCBORBstr(currentAppPublic)), diagnosticsCBORMapField(11, diagnosticsCBORBstr(currentAppKeyID)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(appEpoch)), diagnosticsCBORMapField(13, diagnosticsCBORBstr(currentHelperPublic)),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(currentHelperKeyID)), diagnosticsCBORMapField(15, diagnosticsCBORUint(helperEpoch)),
		diagnosticsCBORMapField(21, diagnosticsCBORBstr(rootDigest)), diagnosticsCBORMapField(23, diagnosticsCBORBstr(helperManifestDigest)),
		diagnosticsCBORMapField(25, diagnosticsCBORBstr(credentialDigest)), diagnosticsCBORMapField(26, diagnosticsCBORUint(issuedAt)),
		diagnosticsCBORMapField(27, diagnosticsCBORUint(expiresAt)), diagnosticsCBORMapField(30, diagnosticsCBORBstr(nonce)),
		diagnosticsCBORMapField(31, diagnosticsCBORUint(authorizationEpoch)),
	}
	if messageType == diagnosticsNamespaceAuthorizationEpoch {
		fields = append(fields, diagnosticsCBORMapField(24, diagnosticsCBORBstr(priorAuthorizationDigest)))
	}
	return diagnosticsCBORMapValue(fields...)
}

func diagnosticsNamespaceReplaceField(value *diagnosticsCBORValue, label uint64, replacement diagnosticsCBORValue) {
	for index := range value.fields {
		if value.fields[index].label == label {
			value.fields[index].value = replacement
			return
		}
	}
}

func sha256Bytes(value []byte) []byte {
	digest := sha256.Sum256(value)
	return digest[:]
}

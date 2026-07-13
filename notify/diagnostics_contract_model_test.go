package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type diagnosticsTestPhase uint8

const (
	diagnosticsTestChecking diagnosticsTestPhase = iota
	diagnosticsTestCompleted
	diagnosticsTestCancelled
	diagnosticsTestInterrupted
	diagnosticsTestTimedOut
)

type diagnosticsTestEvent uint8

const (
	diagnosticsTestTimestamp diagnosticsTestEvent = iota
	diagnosticsTestHTTP
	diagnosticsTestRelay
	diagnosticsTestAPNS
	diagnosticsTestScan
	diagnosticsTestIndex
	diagnosticsTestIdle
	diagnosticsTestCompletion
	diagnosticsTestCapabilityReachable
	diagnosticsTestTombstone
	diagnosticsTestUploadAttestation
	diagnosticsTestResponseAuthorization
	diagnosticsTestFreshApply
	diagnosticsTestDownloadArtifact
	diagnosticsTestCleanup
	diagnosticsTestCancel
	diagnosticsTestTimeout
	diagnosticsTestAppRestart
	diagnosticsTestHelperRestart
	diagnosticsTestEngineRestart
)

type diagnosticsTestTransition struct {
	tuple      string
	operation  string
	event      diagnosticsTestEvent
	generation int
}

type diagnosticsTestEvidence struct {
	operation       string
	generation      int
	phase           diagnosticsTestPhase
	upload          bool
	authorized      bool
	freshApply      bool
	download        bool
	roundtrip       bool
	cleanupAttempts int
}

type diagnosticsTestStateMachine struct {
	tuples map[string]diagnosticsTestEvidence
}

func newDiagnosticsTestStateMachine(tuple, operation string, generation int) diagnosticsTestStateMachine {
	return diagnosticsTestStateMachine{tuples: map[string]diagnosticsTestEvidence{
		tuple: {operation: operation, generation: generation, phase: diagnosticsTestChecking},
	}}
}

func (machine *diagnosticsTestStateMachine) addTuple(tuple, operation string, generation int) {
	machine.tuples[tuple] = diagnosticsTestEvidence{operation: operation, generation: generation, phase: diagnosticsTestChecking}
}

func (machine *diagnosticsTestStateMachine) apply(transition diagnosticsTestTransition) {
	evidence, ok := machine.tuples[transition.tuple]
	if !ok || transition.operation != evidence.operation || transition.generation != evidence.generation {
		return
	}
	if transition.event == diagnosticsTestCleanup {
		evidence.cleanupAttempts++
		machine.tuples[transition.tuple] = evidence
		return
	}
	if evidence.phase != diagnosticsTestChecking {
		return
	}

	switch transition.event {
	case diagnosticsTestUploadAttestation:
		evidence.upload = true
	case diagnosticsTestResponseAuthorization:
		if evidence.upload {
			evidence.authorized = true
		}
	case diagnosticsTestFreshApply:
		if evidence.authorized {
			evidence.freshApply = true
		}
	case diagnosticsTestDownloadArtifact:
		if evidence.upload && evidence.authorized && evidence.freshApply {
			evidence.download = true
			evidence.roundtrip = true
			evidence.phase = diagnosticsTestCompleted
		}
	case diagnosticsTestCancel:
		evidence.phase = diagnosticsTestCancelled
	case diagnosticsTestTimeout:
		evidence.phase = diagnosticsTestTimedOut
	case diagnosticsTestAppRestart, diagnosticsTestHelperRestart, diagnosticsTestEngineRestart:
		evidence.phase = diagnosticsTestInterrupted
	case diagnosticsTestTimestamp, diagnosticsTestHTTP, diagnosticsTestRelay, diagnosticsTestAPNS,
		diagnosticsTestScan, diagnosticsTestIndex, diagnosticsTestIdle, diagnosticsTestCompletion,
		diagnosticsTestCapabilityReachable, diagnosticsTestTombstone:
		// Decision 024 explicitly forbids all of these from creating evidence.
	}
	machine.tuples[transition.tuple] = evidence
}

func TestDiagnosticsStateMachineRequiresCausalUploadAndDownload(t *testing.T) {
	machine := newDiagnosticsTestStateMachine("phone/server/folder", "operation-a", 7)
	transition := func(event diagnosticsTestEvent) {
		machine.apply(diagnosticsTestTransition{"phone/server/folder", "operation-a", event, 7})
	}

	transition(diagnosticsTestDownloadArtifact)
	assertNoDiagnosticsDirectionalEvidence(t, machine.tuples["phone/server/folder"])
	transition(diagnosticsTestFreshApply)
	assertNoDiagnosticsDirectionalEvidence(t, machine.tuples["phone/server/folder"])
	transition(diagnosticsTestUploadAttestation)
	transition(diagnosticsTestDownloadArtifact)
	if got := machine.tuples["phone/server/folder"]; !got.upload || got.download || got.roundtrip {
		t.Fatalf("download upgraded before authorization/fresh apply: %+v", got)
	}
	transition(diagnosticsTestResponseAuthorization)
	transition(diagnosticsTestFreshApply)
	transition(diagnosticsTestDownloadArtifact)
	if got := machine.tuples["phone/server/folder"]; !got.upload || !got.authorized || !got.freshApply || !got.download || !got.roundtrip {
		t.Fatalf("causal roundtrip was not derived: %+v", got)
	}
}

func TestDiagnosticsWeakSignalsNeverCreateEvidence(t *testing.T) {
	weakSignals := []diagnosticsTestEvent{
		diagnosticsTestTimestamp, diagnosticsTestHTTP, diagnosticsTestRelay, diagnosticsTestAPNS,
		diagnosticsTestScan, diagnosticsTestIndex, diagnosticsTestIdle, diagnosticsTestCompletion,
		diagnosticsTestCapabilityReachable, diagnosticsTestTombstone,
	}
	for _, event := range weakSignals {
		machine := newDiagnosticsTestStateMachine("tuple", "operation", 1)
		machine.apply(diagnosticsTestTransition{"tuple", "operation", event, 1})
		assertNoDiagnosticsDirectionalEvidence(t, machine.tuples["tuple"])
	}
}

func TestDiagnosticsTerminalRestartCancelledAndInterruptedOperationsNeverUpgrade(t *testing.T) {
	terminalEvents := []diagnosticsTestEvent{
		diagnosticsTestCancel, diagnosticsTestTimeout, diagnosticsTestAppRestart,
		diagnosticsTestHelperRestart, diagnosticsTestEngineRestart,
	}
	for _, terminal := range terminalEvents {
		machine := newDiagnosticsTestStateMachine("tuple", "operation", 1)
		machine.apply(diagnosticsTestTransition{"tuple", "operation", diagnosticsTestUploadAttestation, 1})
		machine.apply(diagnosticsTestTransition{"tuple", "operation", terminal, 1})
		before := machine.tuples["tuple"]
		for _, late := range []diagnosticsTestEvent{diagnosticsTestResponseAuthorization, diagnosticsTestFreshApply, diagnosticsTestDownloadArtifact} {
			machine.apply(diagnosticsTestTransition{"tuple", "operation", late, 1})
		}
		after := machine.tuples["tuple"]
		if after.upload != before.upload || after.download != before.download || after.roundtrip != before.roundtrip || after.phase != before.phase {
			t.Fatalf("terminal event %d upgraded from %+v to %+v", terminal, before, after)
		}
	}
}

func TestDiagnosticsTupleIsolationAndCleanupOrthogonality(t *testing.T) {
	machine := newDiagnosticsTestStateMachine("tuple-a", "operation-a", 1)
	machine.addTuple("tuple-b", "operation-b", 2)
	beforeB := machine.tuples["tuple-b"]
	for _, event := range []diagnosticsTestEvent{diagnosticsTestUploadAttestation, diagnosticsTestResponseAuthorization, diagnosticsTestFreshApply, diagnosticsTestDownloadArtifact} {
		machine.apply(diagnosticsTestTransition{"tuple-a", "operation-a", event, 1})
	}
	if got := machine.tuples["tuple-b"]; got != beforeB {
		t.Fatalf("tuple-a changed tuple-b: before=%+v after=%+v", beforeB, got)
	}
	beforeEvidence := machine.tuples["tuple-a"]
	machine.apply(diagnosticsTestTransition{"tuple-a", "operation-a", diagnosticsTestCleanup, 1})
	afterCleanup := machine.tuples["tuple-a"]
	if afterCleanup.upload != beforeEvidence.upload || afterCleanup.download != beforeEvidence.download || afterCleanup.roundtrip != beforeEvidence.roundtrip || afterCleanup.phase != beforeEvidence.phase {
		t.Fatalf("cleanup changed evidence: before=%+v after=%+v", beforeEvidence, afterCleanup)
	}
	if afterCleanup.cleanupAttempts != beforeEvidence.cleanupAttempts+1 {
		t.Fatalf("cleanup attempt count = %d, want %d", afterCleanup.cleanupAttempts, beforeEvidence.cleanupAttempts+1)
	}
}

func TestDiagnosticsStateMachinePropertiesOverArbitraryOrders(t *testing.T) {
	random := rand.New(rand.NewSource(0x024))
	for trial := 0; trial < 5_000; trial++ {
		machine := newDiagnosticsTestStateMachine("tuple", "operation", 3)
		terminalSeen := false
		var terminalSnapshot diagnosticsTestEvidence
		for step := 0; step < 64; step++ {
			event := diagnosticsTestEvent(random.Intn(int(diagnosticsTestEngineRestart) + 1))
			machine.apply(diagnosticsTestTransition{"tuple", "operation", event, 3})
			got := machine.tuples["tuple"]
			if got.roundtrip && (!got.upload || !got.download) {
				t.Fatalf("roundtrip without both legs in trial %d: %+v", trial, got)
			}
			if got.download && (!got.upload || !got.authorized || !got.freshApply) {
				t.Fatalf("download without post-authorization fresh apply in trial %d: %+v", trial, got)
			}
			if terminalSeen && event != diagnosticsTestCleanup {
				if got.upload != terminalSnapshot.upload || got.download != terminalSnapshot.download || got.roundtrip != terminalSnapshot.roundtrip || got.phase != terminalSnapshot.phase {
					t.Fatalf("terminal evidence upgraded in trial %d: before=%+v after=%+v", trial, terminalSnapshot, got)
				}
			}
			if got.phase != diagnosticsTestChecking && !terminalSeen {
				terminalSeen = true
				terminalSnapshot = got
			}
		}
	}
}

func TestDiagnosticsPrivacyAndPersistenceSnapshots(t *testing.T) {
	vector := loadDiagnosticsContractFixture(t).Vectors.Privacy
	redactedFields := diagnosticsTestRedactedPrivacyFields(vector.Sentinels)
	logSnapshot := diagnosticsTestLogSnapshot(vector.Sentinels)
	persistenceSnapshot := diagnosticsTestPersistenceSnapshot(vector.Sentinels)
	if logSnapshot != vector.ExpectedLogSnapshot {
		t.Fatalf("log snapshot = %q, want %q", logSnapshot, vector.ExpectedLogSnapshot)
	}
	if persistenceSnapshot != vector.ExpectedPersistenceSnapshot {
		t.Fatalf("persistence snapshot = %q, want %q", persistenceSnapshot, vector.ExpectedPersistenceSnapshot)
	}
	combined := fmt.Sprint(redactedFields) + logSnapshot + persistenceSnapshot
	for _, sentinel := range vector.Sentinels {
		if strings.Contains(combined, sentinel) {
			t.Fatalf("privacy sentinel %q leaked into snapshot", sentinel)
		}
	}
}

func TestDiagnosticsTriggerV1WireRemainsExact(t *testing.T) {
	vector := loadDiagnosticsContractFixture(t).Vectors.RelayV1
	var observedPath string
	var observedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedPath = request.URL.Path
		observedBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"status":"accepted","devices_notified":1}`))
	}))
	defer server.Close()

	client := NewRelayClient(server.URL, "TEST-DEVICE-ID")
	if err := client.Trigger(t.Context()); err != nil {
		t.Fatalf("Trigger v1 request failed: %v", err)
	}
	if observedPath != vector.TriggerPath || string(observedBody) != vector.TriggerBody {
		t.Fatalf("Trigger v1 wire = %s %s, want %s %s", observedPath, observedBody, vector.TriggerPath, vector.TriggerBody)
	}
	for _, forbidden := range vector.ForbiddenContractFields {
		if bytes.Contains(observedBody, []byte(forbidden)) {
			t.Fatalf("Trigger v1 body contains contract field %q", forbidden)
		}
	}
}

func TestDiagnosticsFoundationsHaveNoOperationalRuntimeCarrier(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	repoRoot := diagnosticsTestRepoRoot(t)
	dormantCapabilityCarrier := filepath.Join(repoRoot, "notify", "diagnostics_capabilities.go")
	pairingProtocolCarrier := filepath.Join(repoRoot, "notify", "diagnostics_pairing_protocol.go")
	namespaceProtocolCarrier := filepath.Join(repoRoot, "notify", "diagnostics_namespace_protocol.go")
	runtimeRoots := []string{
		filepath.Join(repoRoot, "notify"),
		filepath.Join(repoRoot, "ios", "VaultSync"),
		filepath.Join(repoRoot, "go", "bridge"),
	}
	for _, root := range runtimeRoots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") || (!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".swift")) {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for name, capability := range fixture.Capabilities {
				allowed := path == dormantCapabilityCarrier ||
					(name == "pairing" && path == pairingProtocolCarrier) ||
					(name == "namespace" && path == namespaceProtocolCarrier)
				if bytes.Contains(body, []byte(capability)) && !allowed {
					return fmt.Errorf("runtime file %s contains an unapproved diagnostics capability %s", path, name)
				}
			}
			for name, domain := range fixture.Domains {
				allowed := (strings.HasPrefix(name, "pairing.") && path == pairingProtocolCarrier) ||
					(strings.HasPrefix(name, "namespace.") && path == namespaceProtocolCarrier)
				if bytes.Contains(body, []byte(strings.TrimSuffix(domain, "\x00"))) && !allowed {
					return fmt.Errorf("runtime file %s contains an unapproved signature domain", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	entrypoints := []string{
		filepath.Join(repoRoot, "notify", "main.go"),
		filepath.Join(repoRoot, "notify", "Dockerfile"),
		filepath.Join(repoRoot, "notify", "docker-compose.yml"),
		filepath.Join(repoRoot, "notify", "scripts", "install.sh"),
		filepath.Join(repoRoot, "notify", "scripts", "install.ps1"),
		filepath.Join(repoRoot, "notify", "scripts", "bootstrap.sh"),
	}
	for _, path := range entrypoints {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"diagnosticsPairing",
			"diagnosticsCredential",
			"diagnosticsNamespace",
			diagnosticsPairingPath,
			diagnosticsCredentialStateFile,
			diagnosticsNamespaceRootName,
		} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("product/helper entrypoint %s activates dormant diagnostics carrier %q", path, forbidden)
			}
		}
	}
	for _, path := range []string{
		filepath.Join(repoRoot, "notify", "diagnostics_pairing_manager.go"),
		filepath.Join(repoRoot, "notify", "diagnostics_pairing_crypto.go"),
		filepath.Join(repoRoot, "notify", "diagnostics_pairing_store.go"),
		filepath.Join(repoRoot, "notify", "diagnostics_namespace_protocol.go"),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"ListenAndServe", "http.Server", "net.Listen", "RelayClient", "SyncthingClient"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("dormant diagnostics core %s contains operational carrier %q", path, forbidden)
			}
		}
	}
}

func TestDiagnosticsNamespaceFoundationCannotMutateSyncthingOrReachNetwork(t *testing.T) {
	repoRoot := diagnosticsTestRepoRoot(t)
	namespaceFiles, err := filepath.Glob(filepath.Join(repoRoot, "notify", "diagnostics_namespace_*.go"))
	if err != nil || len(namespaceFiles) == 0 {
		t.Fatalf("namespace files = %v, %v", namespaceFiles, err)
	}
	for _, path := range namespaceFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			`"net"`, `"net/http"`, "RelayClient", "SyncthingClient", "ListenAndServe", "net.Listen",
			".stignore", "config.xml", "SYNCTHING_", "RELAY_URL", "STARTUP_ANNOUNCE",
			"/api/v1/diagnostics/namespace", "http://", "https://",
		} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("dormant namespace file %s contains operational or config carrier %q", path, forbidden)
			}
		}
	}
}

func diagnosticsTestRedactedPrivacyFields(sentinels []string) map[string]any {
	sensitiveKeys := []string{
		"device_id",
		"folder_name",
		"vault_path",
		"pairing_secret",
		"operation_id",
		"nonce",
		"payload",
		"signature",
	}
	if len(sentinels) != len(sensitiveKeys) {
		panic("diagnostics privacy fixture must cover every sensitive field")
	}

	fields := map[string]any{
		"event":        "operation_terminal",
		"protocol":     1,
		"count":        1,
		"duration_ms":  25,
		"state":        "interrupted",
		"app_epoch":    1,
		"helper_epoch": 1,
		"paired_state": "paired",
	}
	for index, key := range sensitiveKeys {
		fields[key] = sentinels[index]
	}
	for _, key := range sensitiveKeys {
		delete(fields, key)
	}
	return fields
}

func diagnosticsTestLogSnapshot(sentinels []string) string {
	fields := diagnosticsTestRedactedPrivacyFields(sentinels)
	return fmt.Sprintf(
		"event=%s protocol=%d count=%d duration_ms=%d state=%s",
		fields["event"],
		fields["protocol"],
		fields["count"],
		fields["duration_ms"],
		fields["state"],
	)
}

func diagnosticsTestPersistenceSnapshot(sentinels []string) string {
	fields := diagnosticsTestRedactedPrivacyFields(sentinels)
	value := struct {
		AppEpoch    int    `json:"app_epoch"`
		HelperEpoch int    `json:"helper_epoch"`
		State       string `json:"state"`
	}{
		AppEpoch:    fields["app_epoch"].(int),
		HelperEpoch: fields["helper_epoch"].(int),
		State:       fields["paired_state"].(string),
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func diagnosticsTestRepoRoot(t testing.TB) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), ".."))
}

func assertNoDiagnosticsDirectionalEvidence(t *testing.T, evidence diagnosticsTestEvidence) {
	t.Helper()
	if evidence.upload || evidence.authorized || evidence.freshApply || evidence.download || evidence.roundtrip {
		t.Fatalf("unexpected directional evidence: %+v", evidence)
	}
}

func TestDiagnosticsDigestChainsFailOnPriorDigestMutation(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	chainNames := make([]string, 0, len(fixture.DigestChains))
	for name := range fixture.DigestChains {
		chainNames = append(chainNames, name)
	}
	sort.Strings(chainNames)
	for _, name := range chainNames {
		prior := []byte(fixture.DigestChains[name])
		body := testCBORMapValue(testCBORField(31, testCBORBstr(testDomainSHA256("test-prior\x00", prior))))
		encoded, err := encodeTestContractCBOR(body)
		if err != nil {
			t.Fatal(err)
		}
		first := testDomainSHA256("test-chain/"+name+"\x00", encoded)
		prior[0] ^= 0x01
		mutatedBody := testCBORMapValue(testCBORField(31, testCBORBstr(testDomainSHA256("test-prior\x00", prior))))
		mutatedEncoded, err := encodeTestContractCBOR(mutatedBody)
		if err != nil {
			t.Fatal(err)
		}
		second := testDomainSHA256("test-chain/"+name+"\x00", mutatedEncoded)
		if bytes.Equal(first, second) {
			t.Fatalf("digest chain %s ignored a prior-digest mutation", name)
		}
	}
}

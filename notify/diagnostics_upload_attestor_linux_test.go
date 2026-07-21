//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDiagnosticsUploadAttestorReadsThroughConfinementPersistsBeforeReplyAndRestartsIdempotently(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	requestArtifact := installDiagnosticsUploadRequest(t, prepared, fixture, golden.request.canonical)
	now := time.Unix(int64(fixture.AttestationIssuedAt), 0)
	attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(diagnosticsUploadMustHex(t, fixture.HelperNonceHex)), func() time.Time { return now }, nil, nil)

	result := attestor.attest(golden.query.canonical)
	if result.disposition != diagnosticsUploadAccepted || !bytes.Equal(result.attestation, golden.attestation.canonical) {
		t.Fatalf("first attestation = disposition %d bytes %x", result.disposition, result.attestation)
	}
	attestationPath := diagnosticsUploadOperationPath(t, fixture, 2)
	persisted, identity, err := prepared.handle.ReadImmutable(attestationPath)
	if err != nil || !bytes.Equal(persisted, result.attestation) || identity == (diagnosticsNamespaceFileIdentity{}) {
		t.Fatalf("persisted attestation = %x %#v %v", persisted, identity, err)
	}
	result.attestation[0] ^= 0xff
	retry := attestor.attest(golden.query.canonical)
	if retry.disposition != diagnosticsUploadAccepted || !bytes.Equal(retry.attestation, golden.attestation.canonical) {
		t.Fatalf("same-process retry changed bytes: %#v", retry)
	}

	restarted := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xee}, 32)), func() time.Time { return now.Add(time.Second) }, nil, nil)
	replayed := restarted.attest(golden.query.canonical)
	if replayed.disposition != diagnosticsUploadAccepted || !bytes.Equal(replayed.attestation, golden.attestation.canonical) {
		t.Fatalf("restart did not return exact persisted bytes: %#v", replayed)
	}

	attestationArtifact := diagnosticsNamespaceOwnedArtifact{path: attestationPath, identity: identity, digest: sha256.Sum256(persisted)}
	evidence := diagnosticsTestEvidence{upload: true, phase: diagnosticsTestCompleted}
	backup := filepath.Join(prepared.parentPath, "backup", "m5-attestation.cbor")
	version := filepath.Join(prepared.parentPath, ".stversions", "m5-attestation.cbor")
	for _, path := range []string{backup, version} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil || os.WriteFile(path, persisted, 0o600) != nil {
			t.Fatalf("create retention fixture %s", path)
		}
	}
	cleanup, err := prepared.handle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{requestArtifact, attestationArtifact})
	if err != nil || cleanup.Removed != 2 {
		t.Fatalf("cleanup = %#v %v", cleanup, err)
	}
	if !evidence.upload || evidence.download || evidence.roundtrip {
		t.Fatalf("cleanup changed evidence: %+v", evidence)
	}
	for _, path := range []string{backup, version, filepath.Join(prepared.parentPath, "user-note.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup touched retained/user data %s: %v", path, err)
		}
	}
}

func TestDiagnosticsUploadAtomicPublicationHasNoPartialNameAndCrashRecoveryUsesPersistedBytes(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	path := diagnosticsUploadOperationPath(t, fixture, 2)
	absolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, path)
	platform, err := prepared.handle.linux()
	if err != nil {
		t.Fatal(err)
	}
	crashBeforePublish := errors.New("simulated crash before atomic publication")
	platform.beforeAtomicPublish = func() error {
		if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("final attestation visible before atomic publish: %v", err)
		}
		return crashBeforePublish
	}
	if _, err := prepared.handle.CreateImmutableAtomic(path, golden.attestation.canonical); !errors.Is(err, crashBeforePublish) {
		t.Fatalf("pre-publication crash = %v", err)
	}
	platform.beforeAtomicPublish = nil
	if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("anonymous staging inode left a visible path: %v", err)
	}

	installDiagnosticsUploadRequest(t, prepared, fixture, golden.request.canonical)
	crashAfterPersist := errors.New("simulated crash after durable publication")
	now := time.Unix(int64(fixture.AttestationIssuedAt), 0)
	attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(diagnosticsUploadMustHex(t, fixture.HelperNonceHex)), func() time.Time { return now }, func() error { return crashAfterPersist }, nil)
	result := attestor.attest(golden.query.canonical)
	if result.disposition != diagnosticsUploadUnavailable || len(result.attestation) != 0 {
		t.Fatalf("crashed call exposed evidence bytes: %#v", result)
	}
	persisted, _, err := prepared.handle.ReadImmutable(path)
	if err != nil || !bytes.Equal(persisted, golden.attestation.canonical) {
		t.Fatalf("post-persist crash lost exact bytes: %x %v", persisted, err)
	}
	restarted := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 32)), func() time.Time { return now.Add(time.Second) }, nil, nil)
	recovered := restarted.attest(golden.query.canonical)
	if recovered.disposition != diagnosticsUploadAccepted || !bytes.Equal(recovered.attestation, persisted) {
		t.Fatalf("restart recovery = %#v", recovered)
	}
}

func TestDiagnosticsUploadPendingInvalidConflictAndDifferentQueryNeverAttest(t *testing.T) {
	t.Run("pending has no evidence or artifact", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
		attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0x74}, 32)), func() time.Time {
			return time.Unix(int64(fixture.AttestationIssuedAt), 0)
		}, nil, nil)
		result := attestor.attest(golden.query.canonical)
		if result.disposition != diagnosticsUploadPending || len(result.attestation) != 0 {
			t.Fatalf("pending = %#v", result)
		}
		if _, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 2)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending created attestation: %v", err)
		}
	})

	t.Run("invalid request and existing conflict stay untouched", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
		invalidRequest := append([]byte(nil), golden.request.canonical...)
		invalidRequest[len(invalidRequest)-1] ^= 0x01
		installDiagnosticsUploadRequest(t, prepared, fixture, invalidRequest)
		conflictPath := diagnosticsUploadOperationPath(t, fixture, 2)
		conflict := []byte("authenticated-attestation-conflict-sentinel")
		if _, err := prepared.handle.CreateImmutable(conflictPath, conflict); err != nil {
			t.Fatal(err)
		}
		attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0x74}, 32)), func() time.Time {
			return time.Unix(int64(fixture.AttestationIssuedAt), 0)
		}, nil, nil)
		result := attestor.attest(golden.query.canonical)
		if result.disposition != diagnosticsUploadRejected || len(result.attestation) != 0 {
			t.Fatalf("invalid request = %#v", result)
		}
		body, _, err := prepared.handle.ReadImmutable(conflictPath)
		if err != nil || !bytes.Equal(body, conflict) {
			t.Fatalf("conflict was overwritten: %q %v", body, err)
		}
	})

	t.Run("only one byte-exact query is active", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
		installDiagnosticsUploadRequest(t, prepared, fixture, golden.request.canonical)
		attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(diagnosticsUploadMustHex(t, fixture.HelperNonceHex)), func() time.Time {
			return time.Unix(int64(fixture.AttestationIssuedAt), 0)
		}, nil, nil)
		if result := attestor.attest(golden.query.canonical); result.disposition != diagnosticsUploadAccepted {
			t.Fatalf("first exact query = %#v", result)
		}
		otherQuery := diagnosticsUploadAlternateQuery(t, golden, 0x75)
		if result := attestor.attest(otherQuery.canonical); result.disposition != diagnosticsUploadRejected || len(result.attestation) != 0 {
			t.Fatalf("different active query = %#v", result)
		}
		restarted := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xbb}, 32)), func() time.Time {
			return time.Unix(int64(fixture.AttestationIssuedAt+1), 0)
		}, nil, nil)
		if result := restarted.attest(otherQuery.canonical); result.disposition != diagnosticsUploadConflict || len(result.attestation) != 0 {
			t.Fatalf("restart accepted a query not bound by persisted bytes: %#v", result)
		}
	})
}

func TestDiagnosticsUploadWrongBindingsAndEpochsCreateNoAttestation(t *testing.T) {
	tests := []struct {
		name        string
		label       uint64
		replacement diagnosticsCBORValue
	}{
		{name: "homeserver", label: 5, replacement: diagnosticsCBORBstr(bytes.Repeat([]byte{0xa5}, 32))},
		{name: "folder", label: 6, replacement: diagnosticsCBORBstr(bytes.Repeat([]byte{0xa6}, 32))},
		{name: "app epoch", label: 9, replacement: diagnosticsCBORUint(3)},
		{name: "helper epoch", label: 10, replacement: diagnosticsCBORUint(3)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
			defer prepared.handle.Close()
			fixture := loadDiagnosticsUploadGoldenFixture(t)
			golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
			requestValue := diagnosticsCBORWithoutLabels(golden.request.value, 255)
			diagnosticsUploadReplaceField(&requestValue, test.label, test.replacement)
			requestBytes := signDiagnosticsUploadTestMessage(
				t, requestValue, diagnosticsUploadOperationRequest, golden.appPrivate, golden.context,
			)
			request, err := decodeDiagnosticsUploadMessage(requestBytes, golden.context)
			if err != nil {
				t.Fatal(err)
			}
			queryValue := diagnosticsCBORWithoutLabels(golden.query.value, 255)
			diagnosticsUploadReplaceField(&queryValue, test.label, test.replacement)
			diagnosticsUploadReplaceField(&queryValue, 17, diagnosticsCBORBstr(request.digest[:]))
			queryBytes := signDiagnosticsUploadTestMessage(
				t, queryValue, diagnosticsUploadAttestationQuery, golden.appPrivate, golden.context,
			)
			installDiagnosticsUploadRequest(t, prepared, fixture, requestBytes)
			attestor := newDiagnosticsUploadGoldenAttestor(
				t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0x74}, 32)),
				func() time.Time { return time.Unix(int64(fixture.AttestationIssuedAt), 0) }, nil, nil,
			)
			result := attestor.attest(queryBytes)
			if result.disposition != diagnosticsUploadRejected || len(result.attestation) != 0 {
				t.Fatalf("wrong %s = %#v", test.name, result)
			}
			if _, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 2)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("wrong %s created an attestation: %v", test.name, err)
			}
		})
	}
}

func TestDiagnosticsUploadAcceptsValidCrossClockSkewWithoutTimeBasedEvidence(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	installDiagnosticsUploadRequest(t, prepared, fixture, golden.request.canonical)
	now := time.Unix(int64(fixture.RequestIssuedAt-60), 0)
	attestor := newDiagnosticsUploadGoldenAttestor(
		t, prepared, fixture, bytes.NewReader(diagnosticsUploadMustHex(t, fixture.HelperNonceHex)),
		func() time.Time { return now }, nil, nil,
	)
	result := attestor.attest(golden.query.canonical)
	if result.disposition != diagnosticsUploadAccepted || len(result.attestation) == 0 {
		t.Fatalf("valid cross-clock skew = %#v", result)
	}
	attestation, err := decodeDiagnosticsUploadMessage(result.attestation, golden.context)
	issuedAt, _ := diagnosticsUploadUintField(attestation.value, 12)
	observedAt, _ := diagnosticsUploadUintField(attestation.value, 19)
	if err != nil || issuedAt != uint64(now.Unix()) || observedAt != uint64(now.Unix()) ||
		validateDiagnosticsUploadChain(golden.request, golden.query, attestation) != nil {
		t.Fatalf("cross-clock attestation did not preserve helper observation time: %v", err)
	}
}

func TestDiagnosticsUploadConcurrencyPollRateAndSingleFlightBounds(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	installDiagnosticsUploadRequest(t, prepared, fixture, golden.request.canonical)
	now := time.Unix(int64(fixture.AttestationIssuedAt), 0)
	attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(diagnosticsUploadMustHex(t, fixture.HelperNonceHex)), func() time.Time { return now }, nil, nil)

	results := make([]diagnosticsUploadResult, 16)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index] = attestor.attest(golden.query.canonical)
		}(index)
	}
	wait.Wait()
	accepted := 0
	limited := 0
	for _, result := range results {
		switch result.disposition {
		case diagnosticsUploadAccepted:
			accepted++
			if !bytes.Equal(result.attestation, golden.attestation.canonical) {
				t.Fatal("concurrent response bytes diverged")
			}
		case diagnosticsUploadLimited:
			limited++
		default:
			t.Fatalf("unexpected concurrent result: %#v", result)
		}
	}
	if accepted != diagnosticsUploadMaximumPolls || limited != len(results)-diagnosticsUploadMaximumPolls {
		t.Fatalf("poll bound accepted=%d limited=%d", accepted, limited)
	}

	events := []time.Time{}
	for index := 0; index < diagnosticsUploadMaximumAppRequestsMinute; index++ {
		if !allowDiagnosticsUploadWindow(&events, now, time.Minute, diagnosticsUploadMaximumAppRequestsMinute) {
			t.Fatalf("request %d rejected before exact limit", index)
		}
	}
	if allowDiagnosticsUploadWindow(&events, now, time.Minute, diagnosticsUploadMaximumAppRequestsMinute) {
		t.Fatal("request rate limit accepted one too many")
	}
	if !allowDiagnosticsUploadWindow(&events, now.Add(time.Minute+time.Nanosecond), time.Minute, diagnosticsUploadMaximumAppRequestsMinute) {
		t.Fatal("request window did not expire")
	}

	coordinator := newDiagnosticsUploadCoordinator()
	for index := 0; index < diagnosticsUploadMaximumHelperActive; index++ {
		var tuple [32]byte
		tuple[0] = byte(index + 1)
		if !coordinator.begin(tuple, tuple, now, now.Add(time.Minute)) {
			t.Fatalf("active tuple %d rejected", index)
		}
	}
	var overflow [32]byte
	overflow[0] = 0xff
	if coordinator.begin(overflow, overflow, now, now.Add(time.Minute)) {
		t.Fatal("helper active-operation cap was bypassed")
	}
	if !coordinator.begin(overflow, overflow, now.Add(time.Minute+time.Nanosecond), now.Add(2*time.Minute)) {
		t.Fatal("expired helper leases did not release capacity")
	}
}

func TestDiagnosticsUploadInvalidRequestsAndEveryRateWindowAreBounded(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	now := time.Unix(int64(fixture.AttestationIssuedAt), 0)
	attestor := newDiagnosticsUploadGoldenAttestor(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0x74}, 32)), func() time.Time { return now }, nil, nil)
	for index := 0; index < diagnosticsUploadMaximumAppRequestsMinute; index++ {
		result := attestor.attest([]byte{0xff, byte(index)})
		if result.disposition != diagnosticsUploadRejected || result.reason != diagnosticsUploadReasonInvalidMessage {
			t.Fatalf("invalid request %d = %#v", index, result)
		}
	}
	if result := attestor.attest([]byte{0xff}); result.disposition != diagnosticsUploadLimited || result.reason != diagnosticsUploadReasonRateLimit {
		t.Fatalf("invalid request bypassed per-app limit: %#v", result)
	}

	t.Run("per-app direct requests", func(t *testing.T) {
		coordinator := newDiagnosticsUploadCoordinator()
		var app [32]byte
		app[0] = 1
		for index := 0; index < diagnosticsUploadMaximumAppRequestsMinute; index++ {
			if !coordinator.allowRequest(app, now) {
				t.Fatalf("per-app request %d rejected before exact limit", index)
			}
		}
		if coordinator.allowRequest(app, now) {
			t.Fatal("per-app request limit accepted one too many")
		}
		if !coordinator.allowRequest(app, now.Add(time.Minute+time.Nanosecond)) {
			t.Fatal("per-app request window did not expire")
		}
	})

	t.Run("helper-wide direct requests", func(t *testing.T) {
		coordinator := newDiagnosticsUploadCoordinator()
		for index := 0; index < diagnosticsUploadMaximumAllRequestsMinute; index++ {
			var app [32]byte
			app[0] = byte(index/diagnosticsUploadMaximumAppRequestsMinute + 1)
			if !coordinator.allowRequest(app, now) {
				t.Fatalf("helper request %d rejected before exact limit", index)
			}
		}
		var overflowApp [32]byte
		overflowApp[0] = 0xff
		if coordinator.allowRequest(overflowApp, now) {
			t.Fatal("helper-wide request limit accepted one too many")
		}
		if !coordinator.allowRequest(overflowApp, now.Add(time.Minute+time.Nanosecond)) {
			t.Fatal("helper-wide request window did not expire")
		}
	})

	t.Run("shared per-app-folder start lease", func(t *testing.T) {
		coordinator := newDiagnosticsUploadCoordinator()
		var startID [32]byte
		startID[0] = 0x24
		for index := 0; index < diagnosticsUploadMaximumAppStartsHour; index++ {
			var tupleID [32]byte
			tupleID[0] = byte(index + 1)
			if !coordinator.begin(tupleID, startID, now, now.Add(time.Minute)) {
				t.Fatalf("shared start %d rejected before exact limit", index)
			}
			coordinator.finish(tupleID)
		}
		var overflowTuple [32]byte
		overflowTuple[0] = 0xff
		if coordinator.begin(overflowTuple, startID, now, now.Add(time.Minute)) {
			t.Fatal("new attestor-shaped lease bypassed shared hourly limit")
		}
		later := now.Add(time.Hour + time.Nanosecond)
		if !coordinator.begin(overflowTuple, startID, later, later.Add(time.Minute)) {
			t.Fatal("shared hourly start window did not expire")
		}
	})

	for name, limit := range map[string]int{
		"per-app-folder hour": diagnosticsUploadMaximumAppStartsHour,
		"per-app-folder day":  diagnosticsUploadMaximumAppStartsDay,
		"helper day":          diagnosticsUploadMaximumHelperStartsDay,
	} {
		t.Run(name, func(t *testing.T) {
			events := []time.Time{}
			window := 24 * time.Hour
			if name == "per-app-folder hour" {
				window = time.Hour
			}
			for index := 0; index < limit; index++ {
				if !allowDiagnosticsUploadWindow(&events, now, window, limit) {
					t.Fatalf("start %d rejected before exact limit", index)
				}
			}
			if allowDiagnosticsUploadWindow(&events, now, window, limit) {
				t.Fatal("start limit accepted one too many")
			}
			if !allowDiagnosticsUploadWindow(&events, now.Add(window+time.Nanosecond), window, limit) {
				t.Fatal("start window did not expire")
			}
		})
	}
}

func TestDiagnosticsUploadAtomicCreateRaceHasOneImmutableWinner(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	path := diagnosticsUploadOperationPath(t, fixture, 2)
	bodies := [][]byte{golden.attestation.canonical, append([]byte(nil), golden.attestation.canonical...)}
	bodies[1][len(bodies[1])-1] ^= 0x01
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range bodies {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errs[index] = prepared.handle.CreateImmutableAtomic(path, bodies[index])
		}(index)
	}
	wait.Wait()
	successes := 0
	collisions := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, errDiagnosticsNamespaceCollision) {
			collisions++
		} else {
			t.Fatalf("atomic race error = %v", err)
		}
	}
	if successes != 1 || collisions != 1 {
		t.Fatalf("atomic race successes=%d collisions=%d", successes, collisions)
	}
	winner, _, err := prepared.handle.ReadImmutable(path)
	if err != nil || (!bytes.Equal(winner, bodies[0]) && !bytes.Equal(winner, bodies[1])) {
		t.Fatalf("atomic winner was partial or foreign: %x %v", winner, err)
	}
}

func newDiagnosticsUploadGoldenAttestor(t testing.TB, prepared diagnosticsNamespaceLinuxFixture, fixture diagnosticsUploadGoldenFixture, random *bytes.Reader, now func() time.Time, afterPersist func() error, coordinator *diagnosticsUploadCoordinator) *diagnosticsUploadAttestor {
	t.Helper()
	if coordinator == nil {
		coordinator = newDiagnosticsUploadCoordinator()
	}
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	authorization, err := decodeDiagnosticsNamespaceMessage(prepared.fixture.chain.Authorizations[0][1])
	if err != nil {
		t.Fatal(err)
	}
	installation, _ := authorization.bytesField(8, 32)
	attestor, err := newDiagnosticsUploadAttestor(diagnosticsUploadBinding{
		namespaceHandle:     prepared.handle,
		installationBinding: installation,
		homeserverBinding:   diagnosticsUploadMustHex(t, fixture.HomeserverBindingHex),
		folderBinding:       diagnosticsUploadMustHex(t, fixture.FolderBindingHex),
		appPublicKey:        golden.appPrivate.Public().(ed25519.PublicKey),
		helperPrivateKey:    golden.helperPrivate,
		appEpoch:            fixture.AppEpoch,
		helperEpoch:         fixture.HelperEpoch,
		authorizationEpoch:  fixture.AuthorizationEpoch,
	}, coordinator, diagnosticsUploadAttestorHooks{now: now, random: random, afterPersist: afterPersist})
	if err != nil {
		t.Fatal(err)
	}
	return attestor
}

func installDiagnosticsUploadRequest(t testing.TB, prepared diagnosticsNamespaceLinuxFixture, fixture diagnosticsUploadGoldenFixture, body []byte) diagnosticsNamespaceOwnedArtifact {
	t.Helper()
	path := diagnosticsUploadOperationPath(t, fixture, 1)
	artifact, err := prepared.handle.CreateImmutable(path, body)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func diagnosticsUploadOperationPath(t testing.TB, fixture diagnosticsUploadGoldenFixture, kind uint64) diagnosticsNamespacePath {
	t.Helper()
	path, err := diagnosticsNamespaceOperationPath(
		diagnosticsUploadMustHex(t, fixture.InstallationBindingHex), diagnosticsUploadMustHex(t, fixture.OperationIDHex), kind,
	)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func diagnosticsUploadAlternateQuery(t testing.TB, golden diagnosticsUploadGoldenMessages, nonce byte) diagnosticsUploadMessage {
	t.Helper()
	value := diagnosticsCBORWithoutLabels(golden.query.value, 255)
	diagnosticsUploadReplaceField(&value, 30, diagnosticsCBORBstr(bytes.Repeat([]byte{nonce}, 32)))
	encoded := signDiagnosticsUploadTestMessage(t, value, diagnosticsUploadAttestationQuery, golden.appPrivate, golden.context)
	query, err := decodeDiagnosticsUploadMessage(encoded, golden.context)
	if err != nil {
		t.Fatal(err)
	}
	return query
}

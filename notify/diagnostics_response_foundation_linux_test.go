//go:build linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDiagnosticsResponsePersistsBeforeAcceptanceAndRestartsIdempotently(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
	coordinator := newDiagnosticsUploadCoordinator()
	installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)

	foundation := newDiagnosticsResponseGoldenFoundation(
		t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
		func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, coordinator,
	)
	result := foundation.authorizeResponse(golden.authorization.canonical)
	if result.disposition != diagnosticsResponseAccepted || result.reason != diagnosticsResponseReasonNone || len(result.acknowledgment) != 0 {
		t.Fatalf("authorization result = %#v", result)
	}
	responsePath := diagnosticsUploadOperationPath(t, fixture, 3)
	persisted, identity, err := prepared.handle.ReadImmutable(responsePath)
	if err != nil || !bytes.Equal(persisted, golden.response.canonical) || identity == (diagnosticsNamespaceFileIdentity{}) {
		t.Fatalf("persisted response = %x %#v %v", persisted, identity, err)
	}

	restarted := newDiagnosticsResponseGoldenFoundation(
		t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xee}, 288)),
		func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt+1), 0) }, nil, newDiagnosticsUploadCoordinator(),
	)
	if replayed := restarted.authorizeResponse(golden.authorization.canonical); replayed.disposition != diagnosticsResponseAccepted {
		t.Fatalf("restart did not accept exact persisted response: %#v", replayed)
	}
	replayedBytes, _, err := prepared.handle.ReadImmutable(responsePath)
	if err != nil || !bytes.Equal(replayedBytes, persisted) {
		t.Fatalf("restart changed response bytes: %x %v", replayedBytes, err)
	}
}

func TestDiagnosticsResponseAtomicCrashBoundariesAndImmutableConflict(t *testing.T) {
	t.Run("before publication leaves no final name", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		coordinator := newDiagnosticsUploadCoordinator()
		installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
		responsePath := diagnosticsUploadOperationPath(t, fixture, 3)
		absolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, responsePath)
		platform, err := prepared.handle.linux()
		if err != nil {
			t.Fatal(err)
		}
		crash := errors.New("simulated response crash before publication")
		platform.beforeAtomicPublish = func() error {
			if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("response visible before atomic publish: %v", err)
			}
			return crash
		}
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, coordinator,
		)
		result := foundation.authorizeResponse(golden.authorization.canonical)
		if result.disposition != diagnosticsResponseConflict || result.reason != diagnosticsResponseReasonPersistence {
			t.Fatalf("pre-publication crash = %#v", result)
		}
		platform.beforeAtomicPublish = nil
		if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("response staging name survived crash: %v", err)
		}
	})

	t.Run("after durable publication restart uses exact bytes", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		coordinator := newDiagnosticsUploadCoordinator()
		installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
		crash := errors.New("simulated response crash after publication")
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) },
			func() error { return crash }, coordinator,
		)
		result := foundation.authorizeResponse(golden.authorization.canonical)
		if result.disposition != diagnosticsResponseUnavailable || result.reason != diagnosticsResponseReasonPersistence {
			t.Fatalf("post-persist crash = %#v", result)
		}
		responsePath := diagnosticsUploadOperationPath(t, fixture, 3)
		persisted, _, err := prepared.handle.ReadImmutable(responsePath)
		if err != nil || !bytes.Equal(persisted, golden.response.canonical) {
			t.Fatalf("post-persist response = %x %v", persisted, err)
		}
		restarted := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 288)),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt+1), 0) }, nil, newDiagnosticsUploadCoordinator(),
		)
		if recovered := restarted.authorizeResponse(golden.authorization.canonical); recovered.disposition != diagnosticsResponseAccepted {
			t.Fatalf("post-crash recovery = %#v", recovered)
		}
	})

	t.Run("different authorization cannot replace response", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		coordinator := newDiagnosticsUploadCoordinator()
		installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, coordinator,
		)
		if first := foundation.authorizeResponse(golden.authorization.canonical); first.disposition != diagnosticsResponseAccepted {
			t.Fatalf("first authorization = %#v", first)
		}
		otherValue := diagnosticsCBORWithoutLabels(golden.authorization.value, 255)
		diagnosticsUploadReplaceField(&otherValue, 21, diagnosticsCBORBstr(bytes.Repeat([]byte{0x99}, 32)))
		other := signDiagnosticsResponseTestMessage(
			t, otherValue, diagnosticsResponseAuthorization, golden.upload.appPrivate, golden.upload.context,
		)
		if second := foundation.authorizeResponse(other); second.disposition != diagnosticsResponseConflict {
			t.Fatalf("different authorization replaced response: %#v", second)
		}
		persisted, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 3))
		if err != nil || !bytes.Equal(persisted, golden.response.canonical) {
			t.Fatalf("immutable winner changed: %x %v", persisted, err)
		}
	})
}

func TestDiagnosticsResponseRequiresExactAuthenticatedUploadChain(t *testing.T) {
	tests := []struct {
		name    string
		install func(testing.TB, diagnosticsNamespaceLinuxFixture, diagnosticsUploadGoldenFixture, diagnosticsResponseGoldenMessages)
	}{
		{name: "missing request and attestation"},
		{name: "request only", install: func(t testing.TB, prepared diagnosticsNamespaceLinuxFixture, fixture diagnosticsUploadGoldenFixture, golden diagnosticsResponseGoldenMessages) {
			installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
		}},
		{name: "invalid attestation", install: func(t testing.TB, prepared diagnosticsNamespaceLinuxFixture, fixture diagnosticsUploadGoldenFixture, golden diagnosticsResponseGoldenMessages) {
			installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
			if _, err := prepared.handle.CreateImmutable(
				diagnosticsUploadOperationPath(t, fixture, 2), []byte("invalid-attestation-sentinel"),
			); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
			defer prepared.handle.Close()
			fixture := loadDiagnosticsUploadGoldenFixture(t)
			golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
			if test.install != nil {
				test.install(t, prepared, fixture, golden)
			}
			foundation := newDiagnosticsResponseGoldenFoundation(
				t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
				func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, newDiagnosticsUploadCoordinator(),
			)
			result := foundation.authorizeResponse(golden.authorization.canonical)
			if result.disposition == diagnosticsResponseAccepted {
				t.Fatalf("%s created response: %#v", test.name, result)
			}
			if _, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 3)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s left response: %v", test.name, err)
			}
		})
	}

	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
	coordinator := newDiagnosticsUploadCoordinator()
	installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
	wrongValue := diagnosticsCBORWithoutLabels(golden.authorization.value, 255)
	diagnosticsUploadReplaceField(&wrongValue, 20, diagnosticsCBORBstr(bytes.Repeat([]byte{0xb0}, 32)))
	wrongAuthorization := signDiagnosticsResponseTestMessage(
		t, wrongValue, diagnosticsResponseAuthorization, golden.upload.appPrivate, golden.upload.context,
	)
	foundation := newDiagnosticsResponseGoldenFoundation(
		t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
		func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, coordinator,
	)
	if result := foundation.authorizeResponse(wrongAuthorization); result.disposition != diagnosticsResponseRejected {
		t.Fatalf("wrong attestation digest = %#v", result)
	}
}

func TestDiagnosticsResponseAtomicRaceHasOneExactWinner(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
	coordinator := newDiagnosticsUploadCoordinator()
	installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
	foundations := []*diagnosticsResponseFoundation{
		newDiagnosticsResponseGoldenFoundation(t, prepared, fixture, diagnosticsResponseGoldenRandom(golden), func() time.Time {
			return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0)
		}, nil, coordinator),
		newDiagnosticsResponseGoldenFoundation(t, prepared, fixture, diagnosticsResponseGoldenRandom(golden), func() time.Time {
			return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0)
		}, nil, coordinator),
	}
	results := make([]diagnosticsResponseResult, len(foundations))
	var wait sync.WaitGroup
	for index := range foundations {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index] = foundations[index].authorizeResponse(golden.authorization.canonical)
		}(index)
	}
	wait.Wait()
	for index, result := range results {
		if result.disposition != diagnosticsResponseAccepted {
			t.Fatalf("racer %d = %#v", index, result)
		}
	}
	persisted, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 3))
	if err != nil || !bytes.Equal(persisted, golden.response.canonical) {
		t.Fatalf("race winner = %x %v", persisted, err)
	}
}

func TestDiagnosticsCleanupAuthenticatesTargetsPreservesAppArtifactAndIsIdempotent(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
	coordinator := newDiagnosticsUploadCoordinator()
	installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
	foundation := newDiagnosticsResponseGoldenFoundation(
		t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
		func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, coordinator,
	)
	if result := foundation.authorizeResponse(golden.authorization.canonical); result.disposition != diagnosticsResponseAccepted {
		t.Fatalf("create response = %#v", result)
	}

	backup := filepath.Join(prepared.parentPath, "backup", "m6-response.cbor")
	version := filepath.Join(prepared.parentPath, ".stversions", "m6-response.cbor")
	for _, path := range []string{backup, version} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil || os.WriteFile(path, golden.response.canonical, 0o600) != nil {
			t.Fatalf("create retained fixture %s", path)
		}
	}
	foundation.hooks.now = func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenCleanupAckIssuedAt), 0) }
	first := foundation.cleanup(golden.cleanupRequest.canonical)
	if first.disposition != diagnosticsResponseAccepted || !bytes.Equal(first.acknowledgment, golden.cleanupAck.canonical) {
		t.Fatalf("first cleanup = %#v", first)
	}
	if _, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 1)); err != nil {
		t.Fatalf("live app request was removed: %v", err)
	}
	for _, kind := range []uint64{2, 3} {
		if _, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, kind)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("helper artifact %d remains: %v", kind, err)
		}
	}
	for _, path := range []string{backup, version, filepath.Join(prepared.parentPath, "user-note.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup touched retained/user data %s: %v", path, err)
		}
	}

	second := foundation.cleanup(golden.cleanupRequest.canonical)
	if second.disposition != diagnosticsResponseAccepted || bytes.Equal(second.acknowledgment, first.acknowledgment) {
		t.Fatalf("idempotent cleanup result = %#v", second)
	}
	decoded, err := decodeDiagnosticsResponseMessage(second.acknowledgment, golden.upload.context)
	if err != nil || validateDiagnosticsCleanupAcknowledgmentChain(golden.cleanupRequest, decoded) != nil {
		t.Fatalf("idempotent cleanup acknowledgment: %v", err)
	}
	targets, _ := diagnosticsCleanupTargets(decoded.value)
	results, _ := diagnosticsCleanupResults(decoded.value)
	for index, target := range targets {
		expected := diagnosticsCleanupAlreadyAbsent
		if bytes.Equal(target, golden.upload.request.digest[:]) {
			expected = diagnosticsCleanupRetainedConflict
		}
		if results[index] != expected {
			t.Fatalf("idempotent result %d = %d, want %d", index, results[index], expected)
		}
	}
}

func TestDiagnosticsCleanupCrashRestartConflictExpiryAndRaceBoundaries(t *testing.T) {
	t.Run("expired cleanup cannot mutate without a live acknowledgment", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
		if _, err := prepared.handle.CreateImmutable(
			diagnosticsUploadOperationPath(t, fixture, 2), golden.upload.attestation.canonical,
		); err != nil {
			t.Fatal(err)
		}
		request := diagnosticsResponseCleanupRequestForTargets(
			t, golden, diagnosticsResponseGoldenCleanupIssuedAt, diagnosticsResponseGoldenCleanupAckIssuedAt,
			[][]byte{append([]byte(nil), golden.upload.attestation.digest[:]...)},
		)
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 288)),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenCleanupAckIssuedAt), 0) }, nil,
			newDiagnosticsUploadCoordinator(),
		)
		result := foundation.cleanup(request.canonical)
		if result.disposition != diagnosticsResponseRejected || result.reason != diagnosticsResponseReasonStale ||
			len(result.acknowledgment) != 0 {
			t.Fatalf("expired cleanup = %#v", result)
		}
		body, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 2))
		if err != nil || !bytes.Equal(body, golden.upload.attestation.canonical) {
			t.Fatalf("expired cleanup changed artifact: %x %v", body, err)
		}
	})

	t.Run("crash after delete retries as already absent", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		coordinator := newDiagnosticsUploadCoordinator()
		installDiagnosticsResponseUploadChain(t, prepared, fixture, golden, coordinator)
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, diagnosticsResponseGoldenRandom(golden),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) }, nil, coordinator,
		)
		if result := foundation.authorizeResponse(golden.authorization.canonical); result.disposition != diagnosticsResponseAccepted {
			t.Fatalf("create response = %#v", result)
		}
		foundation.hooks.now = func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenCleanupAckIssuedAt), 0) }
		foundation.hooks.afterCleanup = func() error { return errors.New("simulated cleanup crash") }
		if result := foundation.cleanup(golden.cleanupRequest.canonical); result.disposition != diagnosticsResponseUnavailable || len(result.acknowledgment) != 0 {
			t.Fatalf("cleanup crash = %#v", result)
		}
		restarted := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 288)),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenCleanupAckIssuedAt+1), 0) }, nil, newDiagnosticsUploadCoordinator(),
		)
		result := restarted.cleanup(golden.cleanupRequest.canonical)
		if result.disposition != diagnosticsResponseAccepted {
			t.Fatalf("cleanup restart = %#v", result)
		}
	})

	t.Run("invalid changed artifact is retained", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
		conflict := []byte("changed-attestation-conflict-sentinel")
		if _, err := prepared.handle.CreateImmutable(diagnosticsUploadOperationPath(t, fixture, 2), conflict); err != nil {
			t.Fatal(err)
		}
		request := diagnosticsResponseCleanupRequestForTargets(
			t, golden, diagnosticsResponseGoldenCleanupIssuedAt, fixture.ExpiresAt,
			[][]byte{append([]byte(nil), golden.upload.attestation.digest[:]...)},
		)
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 288)),
			func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenCleanupIssuedAt), 0) }, nil, newDiagnosticsUploadCoordinator(),
		)
		result := foundation.cleanup(request.canonical)
		if result.disposition != diagnosticsResponseAccepted {
			t.Fatalf("conflict cleanup = %#v", result)
		}
		ack, err := decodeDiagnosticsResponseMessage(result.acknowledgment, golden.upload.context)
		results, _ := diagnosticsCleanupResults(ack.value)
		if err != nil || len(results) != 1 || results[0] != diagnosticsCleanupRetainedConflict {
			t.Fatalf("conflict acknowledgment = %v %v", results, err)
		}
		body, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 2))
		if err != nil || !bytes.Equal(body, conflict) {
			t.Fatalf("conflict changed or removed: %q %v", body, err)
		}
	})

	t.Run("expired app artifact may be removed", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
		issuedAt := fixture.ExpiresAt + diagnosticsUploadMaximumClockSkewSeconds + 1
		request := diagnosticsResponseCleanupRequestForTargets(
			t, golden, issuedAt, issuedAt+diagnosticsUploadMaximumLifetimeSeconds,
			[][]byte{append([]byte(nil), golden.upload.request.digest[:]...)},
		)
		foundation := newDiagnosticsResponseGoldenFoundation(
			t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 288)),
			func() time.Time { return time.Unix(int64(issuedAt), 0) }, nil, newDiagnosticsUploadCoordinator(),
		)
		result := foundation.cleanup(request.canonical)
		if result.disposition != diagnosticsResponseAccepted {
			t.Fatalf("expired cleanup = %#v", result)
		}
		ack, err := decodeDiagnosticsResponseMessage(result.acknowledgment, golden.upload.context)
		results, _ := diagnosticsCleanupResults(ack.value)
		if err != nil || len(results) != 1 || results[0] != diagnosticsCleanupDeleted {
			t.Fatalf("expired cleanup acknowledgment = %v %v", results, err)
		}
		if _, _, err := prepared.handle.ReadImmutable(diagnosticsUploadOperationPath(t, fixture, 1)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired request remains: %v", err)
		}
	})

	t.Run("two helper instances remain idempotent", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		fixture := loadDiagnosticsUploadGoldenFixture(t)
		golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
		installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
		if _, err := prepared.handle.CreateImmutable(
			diagnosticsUploadOperationPath(t, fixture, 2), golden.upload.attestation.canonical,
		); err != nil {
			t.Fatal(err)
		}
		request := diagnosticsResponseCleanupRequestForTargets(
			t, golden, diagnosticsResponseGoldenCleanupIssuedAt, fixture.ExpiresAt,
			[][]byte{append([]byte(nil), golden.upload.attestation.digest[:]...)},
		)
		foundations := []*diagnosticsResponseFoundation{
			newDiagnosticsResponseGoldenFoundation(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xaa}, 288)), func() time.Time {
				return time.Unix(int64(diagnosticsResponseGoldenCleanupAckIssuedAt), 0)
			}, nil, newDiagnosticsUploadCoordinator()),
			newDiagnosticsResponseGoldenFoundation(t, prepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xbb}, 288)), func() time.Time {
				return time.Unix(int64(diagnosticsResponseGoldenCleanupAckIssuedAt), 0)
			}, nil, newDiagnosticsUploadCoordinator()),
		}
		results := make([]diagnosticsResponseResult, 2)
		var wait sync.WaitGroup
		for index := range foundations {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				results[index] = foundations[index].cleanup(request.canonical)
			}(index)
		}
		wait.Wait()
		seenDeleted := false
		seenAbsent := false
		for _, result := range results {
			if result.disposition != diagnosticsResponseAccepted {
				t.Fatalf("cleanup racer = %#v", result)
			}
			ack, err := decodeDiagnosticsResponseMessage(result.acknowledgment, golden.upload.context)
			ackResults, _ := diagnosticsCleanupResults(ack.value)
			if err != nil || len(ackResults) != 1 {
				t.Fatalf("cleanup racer acknowledgment = %v %v", ackResults, err)
			}
			seenDeleted = seenDeleted || ackResults[0] == diagnosticsCleanupDeleted
			seenAbsent = seenAbsent || ackResults[0] == diagnosticsCleanupAlreadyAbsent
		}
		if !seenDeleted || !seenAbsent {
			t.Fatalf("cleanup race outcomes deleted=%t absent=%t", seenDeleted, seenAbsent)
		}
	})
}

func TestDiagnosticsResponseAndCleanupDirectRequestRateLimitsIncludeInvalidBodies(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
	now := time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0)
	foundation := newDiagnosticsResponseGoldenFoundation(
		t, prepared, fixture, diagnosticsResponseGoldenRandom(golden), func() time.Time { return now }, nil, newDiagnosticsUploadCoordinator(),
	)
	for index := 0; index < diagnosticsUploadMaximumAppRequestsMinute; index++ {
		result := foundation.authorizeResponse([]byte{0xff, byte(index)})
		if result.disposition != diagnosticsResponseRejected || result.reason != diagnosticsResponseReasonInvalidMessage {
			t.Fatalf("invalid direct request %d = %#v", index, result)
		}
	}
	if result := foundation.cleanup([]byte{0xff}); result.disposition != diagnosticsResponseLimited || result.reason != diagnosticsResponseReasonRateLimit {
		t.Fatalf("invalid traffic bypassed shared rate limit: %#v", result)
	}
}

func installDiagnosticsResponseUploadChain(
	t testing.TB,
	prepared diagnosticsNamespaceLinuxFixture,
	fixture diagnosticsUploadGoldenFixture,
	golden diagnosticsResponseGoldenMessages,
	coordinator *diagnosticsUploadCoordinator,
) {
	t.Helper()
	installDiagnosticsUploadRequest(t, prepared, fixture, golden.upload.request.canonical)
	attestor := newDiagnosticsUploadGoldenAttestor(
		t, prepared, fixture, bytes.NewReader(diagnosticsUploadMustHex(t, fixture.HelperNonceHex)),
		func() time.Time { return time.Unix(int64(fixture.AttestationIssuedAt), 0) }, nil, coordinator,
	)
	result := attestor.attest(golden.upload.query.canonical)
	if result.disposition != diagnosticsUploadAccepted || !bytes.Equal(result.attestation, golden.upload.attestation.canonical) {
		t.Fatalf("install upload chain = %#v", result)
	}
}

func newDiagnosticsResponseGoldenFoundation(
	t testing.TB,
	prepared diagnosticsNamespaceLinuxFixture,
	fixture diagnosticsUploadGoldenFixture,
	random io.Reader,
	now func() time.Time,
	afterResponsePersist func() error,
	coordinator *diagnosticsUploadCoordinator,
) *diagnosticsResponseFoundation {
	t.Helper()
	if coordinator == nil {
		coordinator = newDiagnosticsUploadCoordinator()
	}
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)
	authorization, err := decodeDiagnosticsNamespaceMessage(prepared.fixture.chain.Authorizations[0][1])
	if err != nil {
		t.Fatal(err)
	}
	installation, _ := authorization.bytesField(8, 32)
	foundation, err := newDiagnosticsResponseFoundation(diagnosticsUploadBinding{
		namespaceHandle:     prepared.handle,
		installationBinding: installation,
		homeserverBinding:   diagnosticsUploadMustHex(t, fixture.HomeserverBindingHex),
		folderBinding:       diagnosticsUploadMustHex(t, fixture.FolderBindingHex),
		appPublicKey:        golden.upload.appPrivate.Public().(ed25519.PublicKey),
		helperPrivateKey:    golden.upload.helperPrivate,
		appEpoch:            fixture.AppEpoch,
		helperEpoch:         fixture.HelperEpoch,
		authorizationEpoch:  fixture.AuthorizationEpoch,
	}, coordinator, diagnosticsResponseFoundationHooks{
		now: now, random: random, afterResponsePersist: afterResponsePersist,
	})
	if err != nil {
		t.Fatal(err)
	}
	return foundation
}

func diagnosticsResponseGoldenRandom(golden diagnosticsResponseGoldenMessages) *bytes.Reader {
	body := append(append([]byte(nil), golden.responseNonce...), golden.responsePayload...)
	return bytes.NewReader(body)
}

func diagnosticsResponseCleanupRequestForTargets(
	t testing.TB,
	golden diagnosticsResponseGoldenMessages,
	issuedAt, expiresAt uint64,
	targets [][]byte,
) diagnosticsResponseMessage {
	t.Helper()
	sortedTargets := make([][]byte, len(targets))
	for index := range targets {
		sortedTargets[index] = append([]byte(nil), targets[index]...)
	}
	// A one-target request is already sorted. Multi-target callers pass the
	// canonical order produced by the D024 fixture.
	targetValues := make([]diagnosticsCBORValue, len(sortedTargets))
	for index := range sortedTargets {
		targetValues[index] = diagnosticsCBORBstr(sortedTargets[index])
	}
	value := diagnosticsCBORWithoutLabels(golden.cleanupRequest.value, 255)
	diagnosticsUploadReplaceField(&value, 12, diagnosticsCBORUint(issuedAt))
	diagnosticsUploadReplaceField(&value, 13, diagnosticsCBORUint(expiresAt))
	diagnosticsUploadReplaceField(&value, 28, diagnosticsCBORArrayValue(targetValues...))
	encoded := signDiagnosticsResponseTestMessage(
		t, value, diagnosticsCleanupRequest, golden.upload.appPrivate, golden.upload.context,
	)
	message, err := decodeDiagnosticsResponseMessage(encoded, golden.upload.context)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

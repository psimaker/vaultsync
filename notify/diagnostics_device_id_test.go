package main

import (
	"bytes"
	"encoding/base32"
	"strings"
	"testing"
)

func TestDiagnosticsDeviceIDRequiresCanonicalCheckDigits(t *testing.T) {
	raw := bytes.Repeat([]byte{0x5a}, 32)
	display := diagnosticsTestDeviceID(raw)
	decoded, err := parseDiagnosticsDeviceID(display)
	if err != nil || !bytes.Equal(decoded[:], raw) {
		t.Fatalf("canonical Device ID = %x, %v", decoded, err)
	}
	compact := strings.ReplaceAll(display, "-", "")
	if _, err := parseDiagnosticsDeviceID(compact[:52]); err == nil {
		t.Fatal("unchecked legacy Device ID was accepted")
	}
	if _, err := parseDiagnosticsDeviceID(compact); err == nil {
		t.Fatal("non-canonical compact Device ID was accepted")
	}
	if _, err := parseDiagnosticsDeviceID(strings.ToLower(display)); err == nil {
		t.Fatal("non-canonical lowercase Device ID was accepted")
	}
	tampered := []byte(display)
	for index := range tampered {
		if tampered[index] != '-' {
			tampered[index] = 'A'
			if string(tampered) == display {
				tampered[index] = 'B'
			}
			break
		}
	}
	if _, err := parseDiagnosticsDeviceID(string(tampered)); err == nil {
		t.Fatal("Device ID with invalid check digit was accepted")
	}
}

func diagnosticsTestDeviceID(raw []byte) string {
	encoded := strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "=")
	withChecks := make([]byte, 0, 56)
	for group := 0; group < 4; group++ {
		chunk := encoded[group*13 : (group+1)*13]
		check, _ := diagnosticsLuhn32(chunk)
		withChecks = append(withChecks, chunk...)
		withChecks = append(withChecks, check)
	}
	chunks := make([]string, 0, 8)
	for offset := 0; offset < len(withChecks); offset += 7 {
		chunks = append(chunks, string(withChecks[offset:offset+7]))
	}
	return strings.Join(chunks, "-")
}

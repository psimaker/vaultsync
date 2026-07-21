package main

import (
	"encoding/base32"
	"errors"
	"strings"
)

const diagnosticsDeviceIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

var errDiagnosticsDeviceIDInvalid = errors.New("diagnostics device identity unavailable")

// parseDiagnosticsDeviceID accepts only the canonical check-digit-bearing
// Syncthing Device ID form returned by /rest/system/status. Accepting the old
// unchecked 52-character form would violate Decision 022's binding rule.
func parseDiagnosticsDeviceID(display string) ([32]byte, error) {
	var raw [32]byte
	if len(display) != 63 || display != strings.ToUpper(display) {
		return raw, errDiagnosticsDeviceIDInvalid
	}
	for index := range display {
		separator := (index+1)%8 == 0
		if (separator && display[index] != '-') || (!separator && strings.IndexByte(diagnosticsDeviceIDAlphabet, display[index]) < 0) {
			return raw, errDiagnosticsDeviceIDInvalid
		}
	}
	compact := strings.ReplaceAll(display, "-", "")
	if len(compact) != 56 {
		return raw, errDiagnosticsDeviceIDInvalid
	}
	encoded := make([]byte, 0, 52)
	for group := 0; group < 4; group++ {
		chunk := compact[group*14 : (group+1)*14]
		check, err := diagnosticsLuhn32(chunk[:13])
		if err != nil || chunk[13] != check {
			return raw, errDiagnosticsDeviceIDInvalid
		}
		encoded = append(encoded, chunk[:13]...)
	}
	decoded, err := base32.StdEncoding.DecodeString(string(encoded) + "====")
	if err != nil || len(decoded) != len(raw) {
		return raw, errDiagnosticsDeviceIDInvalid
	}
	copy(raw[:], decoded)
	return raw, nil
}

func diagnosticsLuhn32(value string) (byte, error) {
	factor := 1
	sum := 0
	for index := range value {
		codepoint := strings.IndexByte(diagnosticsDeviceIDAlphabet, value[index])
		if codepoint < 0 {
			return 0, errDiagnosticsDeviceIDInvalid
		}
		addend := factor * codepoint
		if factor == 2 {
			factor = 1
		} else {
			factor = 2
		}
		addend = addend/32 + addend%32
		sum += addend
	}
	return diagnosticsDeviceIDAlphabet[(32-sum%32)%32], nil
}

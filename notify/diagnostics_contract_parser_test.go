package main

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"testing"
)

func TestDiagnosticsContractParserRejectsAmbiguousAndForbiddenCBOR(t *testing.T) {
	tests := map[string]string{
		"duplicate keys":           "a201000101",
		"reordered map":            "a202000100",
		"non-uint map key":         "a1616100",
		"non-shortest integer":     "1817",
		"non-shortest bstr length": "580100",
		"indefinite byte string":   "5f4100ff",
		"indefinite array":         "9f00ff",
		"indefinite map":           "bf0100ff",
		"negative integer":         "20",
		"tag":                      "c000",
		"float":                    "f90000",
		"boolean":                  "f4",
		"null":                     "f6",
		"reserved additional info": "1c",
		"huge declared length":     "5a00010000",
	}
	for name, encodedHex := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := hex.DecodeString(encodedHex)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeTestContractCBOR(encoded); err == nil {
				t.Fatalf("accepted forbidden CBOR %x", encoded)
			}
		})
	}

	tooDeep := append(bytes.Repeat([]byte{0x81}, testContractMaximumNestingDepth+2), 0x00)
	if _, err := decodeTestContractCBOR(tooDeep); err == nil {
		t.Fatal("accepted CBOR nesting deeper than the Decision 024 limit")
	}
	oversized := make([]byte, testContractMaximumMessageBytes+1)
	if _, err := decodeTestContractCBOR(oversized); err == nil {
		t.Fatal("accepted oversized CBOR input")
	}
}

func TestDiagnosticsContractParserRejectsTruncationAndTrailingInput(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	golden := mustDecodeHex(t, fixture.Vectors.ContractQuery.ExpectedCanonicalBodyHex)
	for length := 0; length < len(golden); length++ {
		if _, err := decodeTestContractCBOR(golden[:length]); err == nil {
			t.Fatalf("accepted truncation at byte %d of %d", length, len(golden))
		}
	}
	withTrailing := append(append([]byte(nil), golden...), 0x00)
	if _, err := decodeTestContractCBOR(withTrailing); err == nil {
		t.Fatal("accepted trailing input")
	}
}

func TestDiagnosticsCapabilityQueryRejectsUnknownWrongTypeLengthAndValue(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	golden := mustDecodeHex(t, fixture.Vectors.ContractQuery.ExpectedCanonicalBodyHex)
	decoded, err := decodeTestContractCBOR(golden)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*testCBORValue){
		"unknown key": func(value *testCBORValue) {
			value.entries = append(value.entries, testCBORField(26, testCBORUint(1)))
		},
		"wrong capability type": func(value *testCBORValue) {
			replaceTestCBORField(value, 1, testCBORBstr([]byte(fixture.Capabilities["roundtrip"])))
		},
		"wrong capability value": func(value *testCBORValue) {
			replaceTestCBORField(value, 1, testCBORTextValue("eu.vaultsync.diagnostics.correlated-roundtrip/2"))
		},
		"wrong protocol": func(value *testCBORValue) {
			replaceTestCBORField(value, 2, testCBORUint(2))
		},
		"short binding": func(value *testCBORValue) {
			replaceTestCBORField(value, 5, testCBORBstr(make([]byte, 31)))
		},
		"long key id": func(value *testCBORValue) {
			replaceTestCBORField(value, 7, testCBORBstr(make([]byte, 33)))
		},
		"wrong time type": func(value *testCBORValue) {
			replaceTestCBORField(value, 12, testCBORTextValue("1700000000"))
		},
		"non-increasing expiry": func(value *testCBORValue) {
			replaceTestCBORField(value, 13, testCBORUint(fixture.Vectors.ContractQuery.IssuedAt))
		},
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := cloneTestCBORValue(decoded)
			mutate(&candidate)
			encoded, err := encodeTestContractCBOR(candidate)
			if err != nil {
				t.Fatalf("encode mutation: %v", err)
			}
			parsed, err := decodeTestContractCBOR(encoded)
			if err != nil {
				t.Fatalf("mutation should remain structurally canonical: %v", err)
			}
			if err := validateDiagnosticsCapabilityQuery(parsed, fixture); err == nil {
				t.Fatal("schema accepted invalid capability query")
			}
		})
	}
}

func TestDiagnosticsContractCanonicalRoundTripProperty(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	seeds := [][]byte{
		{0x00}, {0x17}, {0x18, 0x18}, {0x40}, {0x60}, {0x80}, {0xa0},
		mustDecodeHex(t, fixture.Vectors.BootstrapHMAC.CanonicalBodyHex),
		mustDecodeHex(t, fixture.Vectors.ContractQuery.ExpectedCanonicalBodyHex),
	}
	for _, seed := range seeds {
		value, err := decodeTestContractCBOR(seed)
		if err != nil {
			t.Fatalf("valid seed %x was rejected: %v", seed, err)
		}
		reencoded, err := encodeTestContractCBOR(value)
		if err != nil || !bytes.Equal(reencoded, seed) {
			t.Fatalf("canonical roundtrip = %x, %v; want %x", reencoded, err, seed)
		}
	}
}

func TestDiagnosticsContractArbitraryBytesNeverBypassCanonicalization(t *testing.T) {
	random := rand.New(rand.NewSource(0x022024))
	for index := 0; index < 10_000; index++ {
		length := random.Intn(512)
		candidate := make([]byte, length)
		if _, err := random.Read(candidate); err != nil {
			t.Fatal(err)
		}
		value, err := decodeTestContractCBOR(candidate)
		if err != nil {
			continue
		}
		reencoded, err := encodeTestContractCBOR(value)
		if err != nil {
			t.Fatalf("accepted input could not be re-encoded: %v", err)
		}
		if !bytes.Equal(reencoded, candidate) {
			t.Fatalf("accepted non-canonical input %x as %x", candidate, reencoded)
		}
	}
}

func FuzzDiagnosticsContractCanonicalCBOR(f *testing.F) {
	fixture := loadDiagnosticsContractFixture(f)
	f.Add([]byte{0xa0})
	f.Add(mustDecodeHex(f, fixture.Vectors.BootstrapHMAC.CanonicalBodyHex))
	f.Add(mustDecodeHex(f, fixture.Vectors.ContractQuery.ExpectedCanonicalBodyHex))
	f.Add([]byte{0xa2, 0x01, 0x00, 0x01, 0x01})
	f.Fuzz(func(t *testing.T, candidate []byte) {
		if len(candidate) > testContractMaximumMessageBytes+1 {
			return
		}
		value, err := decodeTestContractCBOR(candidate)
		if err != nil {
			return
		}
		reencoded, err := encodeTestContractCBOR(value)
		if err != nil {
			t.Fatalf("accepted input could not be re-encoded: %v", err)
		}
		if !bytes.Equal(reencoded, candidate) {
			t.Fatalf("accepted non-canonical input %x as %x", candidate, reencoded)
		}
	})
}

func replaceTestCBORField(value *testCBORValue, label uint64, replacement testCBORValue) {
	for index := range value.entries {
		if value.entries[index].label == label {
			value.entries[index].value = replacement
			return
		}
	}
}

func cloneTestCBORValue(value testCBORValue) testCBORValue {
	clone := value
	clone.bytes = append([]byte(nil), value.bytes...)
	clone.array = make([]testCBORValue, len(value.array))
	for index := range value.array {
		clone.array[index] = cloneTestCBORValue(value.array[index])
	}
	clone.entries = make([]testCBORMapEntry, len(value.entries))
	for index, entry := range value.entries {
		clone.entries[index] = testCBORField(entry.label, cloneTestCBORValue(entry.value))
	}
	return clone
}

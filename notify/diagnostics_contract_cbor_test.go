package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"
)

const (
	testContractMaximumMessageBytes = 16 * 1024
	testContractMaximumMapEntries   = 32
	testContractMaximumArrayEntries = 8
	testContractMaximumNestingDepth = 4
)

type testCBORKind uint8

const (
	testCBORUnsigned testCBORKind = iota
	testCBORBytes
	testCBORText
	testCBORArray
	testCBORMap
)

type testCBORValue struct {
	kind     testCBORKind
	unsigned uint64
	bytes    []byte
	text     string
	array    []testCBORValue
	entries  []testCBORMapEntry
}

type testCBORMapEntry struct {
	label uint64
	value testCBORValue
}

func testCBORUint(value uint64) testCBORValue {
	return testCBORValue{kind: testCBORUnsigned, unsigned: value}
}

func testCBORBstr(value []byte) testCBORValue {
	return testCBORValue{kind: testCBORBytes, bytes: append([]byte(nil), value...)}
}

func testCBORTextValue(value string) testCBORValue {
	return testCBORValue{kind: testCBORText, text: value}
}

func testCBORArrayValue(values ...testCBORValue) testCBORValue {
	return testCBORValue{kind: testCBORArray, array: append([]testCBORValue(nil), values...)}
}

func testCBORMapValue(entries ...testCBORMapEntry) testCBORValue {
	return testCBORValue{kind: testCBORMap, entries: append([]testCBORMapEntry(nil), entries...)}
}

func testCBORField(label uint64, value testCBORValue) testCBORMapEntry {
	return testCBORMapEntry{label: label, value: value}
}

func encodeTestContractCBOR(value testCBORValue) ([]byte, error) {
	encoded, err := appendTestContractCBOR(nil, value, 0)
	if err != nil {
		return nil, err
	}
	if len(encoded) > testContractMaximumMessageBytes {
		return nil, fmt.Errorf("encoded message exceeds %d bytes", testContractMaximumMessageBytes)
	}
	return encoded, nil
}

func appendTestContractCBOR(dst []byte, value testCBORValue, depth int) ([]byte, error) {
	if depth > testContractMaximumNestingDepth {
		return nil, errors.New("CBOR nesting is too deep")
	}

	switch value.kind {
	case testCBORUnsigned:
		return appendTestCBORHead(dst, 0, value.unsigned), nil
	case testCBORBytes:
		dst = appendTestCBORHead(dst, 2, uint64(len(value.bytes)))
		return append(dst, value.bytes...), nil
	case testCBORText:
		if !utf8.ValidString(value.text) {
			return nil, errors.New("invalid UTF-8 text")
		}
		dst = appendTestCBORHead(dst, 3, uint64(len(value.text)))
		return append(dst, value.text...), nil
	case testCBORArray:
		if len(value.array) > testContractMaximumArrayEntries {
			return nil, errors.New("too many array entries")
		}
		dst = appendTestCBORHead(dst, 4, uint64(len(value.array)))
		for _, child := range value.array {
			var err error
			dst, err = appendTestContractCBOR(dst, child, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	case testCBORMap:
		if len(value.entries) > testContractMaximumMapEntries {
			return nil, errors.New("too many map entries")
		}
		entries := append([]testCBORMapEntry(nil), value.entries...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].label < entries[j].label })
		for i := 1; i < len(entries); i++ {
			if entries[i-1].label == entries[i].label {
				return nil, errors.New("duplicate map key")
			}
		}
		dst = appendTestCBORHead(dst, 5, uint64(len(entries)))
		for _, entry := range entries {
			dst = appendTestCBORHead(dst, 0, entry.label)
			var err error
			dst, err = appendTestContractCBOR(dst, entry.value, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	default:
		return nil, errors.New("unsupported CBOR value")
	}
}

func appendTestCBORHead(dst []byte, major byte, value uint64) []byte {
	switch {
	case value < 24:
		return append(dst, major<<5|byte(value))
	case value <= 0xff:
		return append(dst, major<<5|24, byte(value))
	case value <= 0xffff:
		return binary.BigEndian.AppendUint16(append(dst, major<<5|25), uint16(value))
	case value <= 0xffffffff:
		return binary.BigEndian.AppendUint32(append(dst, major<<5|26), uint32(value))
	default:
		return binary.BigEndian.AppendUint64(append(dst, major<<5|27), value)
	}
}

type testCBORDecoder struct {
	data  []byte
	index int
}

func decodeTestContractCBOR(data []byte) (testCBORValue, error) {
	if len(data) == 0 {
		return testCBORValue{}, errors.New("empty CBOR input")
	}
	if len(data) > testContractMaximumMessageBytes {
		return testCBORValue{}, errors.New("CBOR input is oversized")
	}
	decoder := testCBORDecoder{data: data}
	value, err := decoder.decode(0)
	if err != nil {
		return testCBORValue{}, err
	}
	if decoder.index != len(data) {
		return testCBORValue{}, errors.New("trailing CBOR input")
	}
	reencoded, err := encodeTestContractCBOR(value)
	if err != nil {
		return testCBORValue{}, err
	}
	if hex.EncodeToString(reencoded) != hex.EncodeToString(data) {
		return testCBORValue{}, errors.New("CBOR input is not deterministic")
	}
	return value, nil
}

func (decoder *testCBORDecoder) decode(depth int) (testCBORValue, error) {
	if depth > testContractMaximumNestingDepth {
		return testCBORValue{}, errors.New("CBOR nesting is too deep")
	}
	if decoder.index >= len(decoder.data) {
		return testCBORValue{}, errors.New("truncated CBOR input")
	}

	initial := decoder.data[decoder.index]
	decoder.index++
	major := initial >> 5
	argument, err := decoder.readArgument(initial & 0x1f)
	if err != nil {
		return testCBORValue{}, err
	}

	switch major {
	case 0:
		return testCBORUint(argument), nil
	case 2:
		body, err := decoder.readBody(argument)
		if err != nil {
			return testCBORValue{}, err
		}
		return testCBORBstr(body), nil
	case 3:
		body, err := decoder.readBody(argument)
		if err != nil {
			return testCBORValue{}, err
		}
		if !utf8.Valid(body) {
			return testCBORValue{}, errors.New("invalid UTF-8 text")
		}
		return testCBORTextValue(string(body)), nil
	case 4:
		if argument > testContractMaximumArrayEntries {
			return testCBORValue{}, errors.New("too many array entries")
		}
		values := make([]testCBORValue, 0, int(argument))
		for range argument {
			value, err := decoder.decode(depth + 1)
			if err != nil {
				return testCBORValue{}, err
			}
			values = append(values, value)
		}
		return testCBORValue{kind: testCBORArray, array: values}, nil
	case 5:
		if argument > testContractMaximumMapEntries {
			return testCBORValue{}, errors.New("too many map entries")
		}
		entries := make([]testCBORMapEntry, 0, int(argument))
		var previous uint64
		for index := range argument {
			key, err := decoder.decode(depth + 1)
			if err != nil {
				return testCBORValue{}, err
			}
			if key.kind != testCBORUnsigned {
				return testCBORValue{}, errors.New("map key is not an unsigned integer")
			}
			if index > 0 && key.unsigned <= previous {
				return testCBORValue{}, errors.New("duplicate or reordered map key")
			}
			previous = key.unsigned
			value, err := decoder.decode(depth + 1)
			if err != nil {
				return testCBORValue{}, err
			}
			entries = append(entries, testCBORField(key.unsigned, value))
		}
		return testCBORValue{kind: testCBORMap, entries: entries}, nil
	default:
		return testCBORValue{}, fmt.Errorf("forbidden CBOR major type %d", major)
	}
}

func (decoder *testCBORDecoder) readArgument(additional byte) (uint64, error) {
	switch {
	case additional < 24:
		return uint64(additional), nil
	case additional == 24:
		body, err := decoder.readFixed(1)
		if err != nil {
			return 0, err
		}
		if body[0] < 24 {
			return 0, errors.New("non-shortest CBOR integer or length")
		}
		return uint64(body[0]), nil
	case additional == 25:
		body, err := decoder.readFixed(2)
		if err != nil {
			return 0, err
		}
		value := uint64(binary.BigEndian.Uint16(body))
		if value <= 0xff {
			return 0, errors.New("non-shortest CBOR integer or length")
		}
		return value, nil
	case additional == 26:
		body, err := decoder.readFixed(4)
		if err != nil {
			return 0, err
		}
		value := uint64(binary.BigEndian.Uint32(body))
		if value <= 0xffff {
			return 0, errors.New("non-shortest CBOR integer or length")
		}
		return value, nil
	case additional == 27:
		body, err := decoder.readFixed(8)
		if err != nil {
			return 0, err
		}
		value := binary.BigEndian.Uint64(body)
		if value <= 0xffffffff {
			return 0, errors.New("non-shortest CBOR integer or length")
		}
		return value, nil
	default:
		return 0, errors.New("reserved or indefinite CBOR form")
	}
}

func (decoder *testCBORDecoder) readBody(length uint64) ([]byte, error) {
	if length > testContractMaximumMessageBytes {
		return nil, errors.New("declared CBOR body is oversized")
	}
	if length > uint64(len(decoder.data)-decoder.index) {
		return nil, errors.New("truncated CBOR body")
	}
	return decoder.readFixed(int(length))
}

func (decoder *testCBORDecoder) readFixed(length int) ([]byte, error) {
	if length < 0 || length > len(decoder.data)-decoder.index {
		return nil, errors.New("truncated CBOR input")
	}
	start := decoder.index
	decoder.index += length
	return decoder.data[start:decoder.index], nil
}

func testCBORMapLookup(value testCBORValue, label uint64) (testCBORValue, bool) {
	if value.kind != testCBORMap {
		return testCBORValue{}, false
	}
	for _, entry := range value.entries {
		if entry.label == label {
			return entry.value, true
		}
	}
	return testCBORValue{}, false
}

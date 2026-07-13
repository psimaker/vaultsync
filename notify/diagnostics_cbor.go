package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
	"unicode/utf8"
)

const (
	diagnosticsMaximumMessageBytes = 16 * 1024
	diagnosticsMaximumMapEntries   = 32
	diagnosticsMaximumArrayEntries = 8
	diagnosticsMaximumNestingDepth = 4
)

var errDiagnosticsInvalidCBOR = errors.New("invalid diagnostics message")

type diagnosticsCBORKind uint8

const (
	diagnosticsCBORUnsigned diagnosticsCBORKind = iota
	diagnosticsCBORBytes
	diagnosticsCBORText
	diagnosticsCBORArray
	diagnosticsCBORMap
)

type diagnosticsCBORValue struct {
	kind     diagnosticsCBORKind
	unsigned uint64
	bytes    []byte
	text     string
	array    []diagnosticsCBORValue
	fields   []diagnosticsCBORField
}

type diagnosticsCBORField struct {
	label uint64
	value diagnosticsCBORValue
}

func diagnosticsCBORUint(value uint64) diagnosticsCBORValue {
	return diagnosticsCBORValue{kind: diagnosticsCBORUnsigned, unsigned: value}
}

func diagnosticsCBORBstr(value []byte) diagnosticsCBORValue {
	return diagnosticsCBORValue{kind: diagnosticsCBORBytes, bytes: append([]byte(nil), value...)}
}

func diagnosticsCBORTextValue(value string) diagnosticsCBORValue {
	return diagnosticsCBORValue{kind: diagnosticsCBORText, text: value}
}

func diagnosticsCBORArrayValue(values ...diagnosticsCBORValue) diagnosticsCBORValue {
	return diagnosticsCBORValue{kind: diagnosticsCBORArray, array: append([]diagnosticsCBORValue(nil), values...)}
}

func diagnosticsCBORMapValue(fields ...diagnosticsCBORField) diagnosticsCBORValue {
	return diagnosticsCBORValue{kind: diagnosticsCBORMap, fields: append([]diagnosticsCBORField(nil), fields...)}
}

func diagnosticsCBORMapField(label uint64, value diagnosticsCBORValue) diagnosticsCBORField {
	return diagnosticsCBORField{label: label, value: value}
}

func encodeDiagnosticsCBOR(value diagnosticsCBORValue) ([]byte, error) {
	encoded, err := appendDiagnosticsCBOR(nil, value, 0)
	if err != nil || len(encoded) > diagnosticsMaximumMessageBytes {
		return nil, errDiagnosticsInvalidCBOR
	}
	return encoded, nil
}

func appendDiagnosticsCBOR(dst []byte, value diagnosticsCBORValue, depth int) ([]byte, error) {
	if depth > diagnosticsMaximumNestingDepth {
		return nil, errDiagnosticsInvalidCBOR
	}

	switch value.kind {
	case diagnosticsCBORUnsigned:
		return appendDiagnosticsCBORHead(dst, 0, value.unsigned), nil
	case diagnosticsCBORBytes:
		dst = appendDiagnosticsCBORHead(dst, 2, uint64(len(value.bytes)))
		return append(dst, value.bytes...), nil
	case diagnosticsCBORText:
		if !utf8.ValidString(value.text) {
			return nil, errDiagnosticsInvalidCBOR
		}
		dst = appendDiagnosticsCBORHead(dst, 3, uint64(len(value.text)))
		return append(dst, value.text...), nil
	case diagnosticsCBORArray:
		if len(value.array) > diagnosticsMaximumArrayEntries {
			return nil, errDiagnosticsInvalidCBOR
		}
		dst = appendDiagnosticsCBORHead(dst, 4, uint64(len(value.array)))
		var err error
		for _, child := range value.array {
			dst, err = appendDiagnosticsCBOR(dst, child, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	case diagnosticsCBORMap:
		if len(value.fields) > diagnosticsMaximumMapEntries {
			return nil, errDiagnosticsInvalidCBOR
		}
		fields := append([]diagnosticsCBORField(nil), value.fields...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].label < fields[j].label })
		for index := 1; index < len(fields); index++ {
			if fields[index-1].label == fields[index].label {
				return nil, errDiagnosticsInvalidCBOR
			}
		}
		dst = appendDiagnosticsCBORHead(dst, 5, uint64(len(fields)))
		var err error
		for _, field := range fields {
			dst = appendDiagnosticsCBORHead(dst, 0, field.label)
			dst, err = appendDiagnosticsCBOR(dst, field.value, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	default:
		return nil, errDiagnosticsInvalidCBOR
	}
}

func appendDiagnosticsCBORHead(dst []byte, major byte, value uint64) []byte {
	switch {
	case value < 24:
		return append(dst, major<<5|byte(value))
	case value <= uint64(^uint8(0)):
		return append(dst, major<<5|24, byte(value))
	case value <= uint64(^uint16(0)):
		dst = append(dst, major<<5|25)
		return binary.BigEndian.AppendUint16(dst, uint16(value))
	case value <= uint64(^uint32(0)):
		dst = append(dst, major<<5|26)
		return binary.BigEndian.AppendUint32(dst, uint32(value))
	default:
		dst = append(dst, major<<5|27)
		return binary.BigEndian.AppendUint64(dst, value)
	}
}

type diagnosticsCBORDecoder struct {
	data  []byte
	index int
}

func decodeDiagnosticsCBOR(data []byte) (diagnosticsCBORValue, error) {
	if len(data) == 0 || len(data) > diagnosticsMaximumMessageBytes {
		return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
	}
	decoder := diagnosticsCBORDecoder{data: data}
	value, err := decoder.decode(0)
	if err != nil || decoder.index != len(data) {
		return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
	}
	reencoded, err := encodeDiagnosticsCBOR(value)
	if err != nil || !bytes.Equal(reencoded, data) {
		return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
	}
	return value, nil
}

func (decoder *diagnosticsCBORDecoder) decode(depth int) (diagnosticsCBORValue, error) {
	if depth > diagnosticsMaximumNestingDepth || decoder.index >= len(decoder.data) {
		return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
	}
	initial := decoder.data[decoder.index]
	decoder.index++
	argument, err := decoder.readArgument(initial & 0x1f)
	if err != nil {
		return diagnosticsCBORValue{}, err
	}

	switch initial >> 5 {
	case 0:
		return diagnosticsCBORUint(argument), nil
	case 2:
		body, err := decoder.readBody(argument)
		if err != nil {
			return diagnosticsCBORValue{}, err
		}
		return diagnosticsCBORBstr(body), nil
	case 3:
		body, err := decoder.readBody(argument)
		if err != nil || !utf8.Valid(body) {
			return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
		}
		return diagnosticsCBORTextValue(string(body)), nil
	case 4:
		if argument > diagnosticsMaximumArrayEntries {
			return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
		}
		values := make([]diagnosticsCBORValue, 0, int(argument))
		for range argument {
			value, err := decoder.decode(depth + 1)
			if err != nil {
				return diagnosticsCBORValue{}, err
			}
			values = append(values, value)
		}
		return diagnosticsCBORArrayValue(values...), nil
	case 5:
		if argument > diagnosticsMaximumMapEntries {
			return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
		}
		fields := make([]diagnosticsCBORField, 0, int(argument))
		var prior uint64
		for index := uint64(0); index < argument; index++ {
			key, err := decoder.decode(depth + 1)
			if err != nil || key.kind != diagnosticsCBORUnsigned || (index > 0 && key.unsigned <= prior) {
				return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
			}
			prior = key.unsigned
			value, err := decoder.decode(depth + 1)
			if err != nil {
				return diagnosticsCBORValue{}, err
			}
			fields = append(fields, diagnosticsCBORMapField(key.unsigned, value))
		}
		return diagnosticsCBORMapValue(fields...), nil
	default:
		return diagnosticsCBORValue{}, errDiagnosticsInvalidCBOR
	}
}

func (decoder *diagnosticsCBORDecoder) readArgument(additional byte) (uint64, error) {
	switch additional {
	case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23:
		return uint64(additional), nil
	case 24:
		body, err := decoder.readFixed(1)
		if err != nil || body[0] < 24 {
			return 0, errDiagnosticsInvalidCBOR
		}
		return uint64(body[0]), nil
	case 25:
		body, err := decoder.readFixed(2)
		if err != nil {
			return 0, err
		}
		value := uint64(binary.BigEndian.Uint16(body))
		if value <= uint64(^uint8(0)) {
			return 0, errDiagnosticsInvalidCBOR
		}
		return value, nil
	case 26:
		body, err := decoder.readFixed(4)
		if err != nil {
			return 0, err
		}
		value := uint64(binary.BigEndian.Uint32(body))
		if value <= uint64(^uint16(0)) {
			return 0, errDiagnosticsInvalidCBOR
		}
		return value, nil
	case 27:
		body, err := decoder.readFixed(8)
		if err != nil {
			return 0, err
		}
		value := binary.BigEndian.Uint64(body)
		if value <= uint64(^uint32(0)) {
			return 0, errDiagnosticsInvalidCBOR
		}
		return value, nil
	default:
		return 0, errDiagnosticsInvalidCBOR
	}
}

func (decoder *diagnosticsCBORDecoder) readBody(length uint64) ([]byte, error) {
	if length > diagnosticsMaximumMessageBytes || length > uint64(len(decoder.data)-decoder.index) {
		return nil, errDiagnosticsInvalidCBOR
	}
	return decoder.readFixed(int(length))
}

func (decoder *diagnosticsCBORDecoder) readFixed(length int) ([]byte, error) {
	if length < 0 || length > len(decoder.data)-decoder.index {
		return nil, errDiagnosticsInvalidCBOR
	}
	start := decoder.index
	decoder.index += length
	return decoder.data[start:decoder.index], nil
}

func diagnosticsCBORLookup(value diagnosticsCBORValue, label uint64) (diagnosticsCBORValue, bool) {
	if value.kind != diagnosticsCBORMap {
		return diagnosticsCBORValue{}, false
	}
	for _, field := range value.fields {
		if field.label == label {
			return field.value, true
		}
	}
	return diagnosticsCBORValue{}, false
}

func diagnosticsCBORWithoutLabels(value diagnosticsCBORValue, labels ...uint64) diagnosticsCBORValue {
	if value.kind != diagnosticsCBORMap {
		return diagnosticsCBORValue{}
	}
	remove := make(map[uint64]struct{}, len(labels))
	for _, label := range labels {
		remove[label] = struct{}{}
	}
	fields := make([]diagnosticsCBORField, 0, len(value.fields))
	for _, field := range value.fields {
		if _, ok := remove[field.label]; !ok {
			fields = append(fields, diagnosticsCBORMapField(field.label, cloneDiagnosticsCBOR(field.value)))
		}
	}
	return diagnosticsCBORMapValue(fields...)
}

func cloneDiagnosticsCBOR(value diagnosticsCBORValue) diagnosticsCBORValue {
	switch value.kind {
	case diagnosticsCBORBytes:
		return diagnosticsCBORBstr(value.bytes)
	case diagnosticsCBORArray:
		values := make([]diagnosticsCBORValue, len(value.array))
		for index := range value.array {
			values[index] = cloneDiagnosticsCBOR(value.array[index])
		}
		return diagnosticsCBORArrayValue(values...)
	case diagnosticsCBORMap:
		fields := make([]diagnosticsCBORField, len(value.fields))
		for index := range value.fields {
			fields[index] = diagnosticsCBORMapField(value.fields[index].label, cloneDiagnosticsCBOR(value.fields[index].value))
		}
		return diagnosticsCBORMapValue(fields...)
	default:
		return value
	}
}

// Package jsonvalue implements Serpe's stable JSON value semantics. Its
// canonical form ignores whitespace and object-key order, preserves number
// lexemes, and deliberately does not claim RFC 8785 conformance.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

// Canonical returns a stable encoding of one JSON value.
func Canonical(raw []byte) ([]byte, error) {
	value, err := decode(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON: %w", err)
	}
	return canonical, nil
}

// CanonicalObject returns a stable encoding of one JSON object.
func CanonicalObject(raw []byte) ([]byte, error) {
	value, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("JSON object required")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON object: %w", err)
	}
	return canonical, nil
}

// IsObject reports whether raw contains exactly one JSON object.
//
// It validates with a single scan (json.Valid) plus an O(1) first-token
// check, instead of decoding and re-encoding the whole value. json.Valid
// rejects trailing values, so the "exactly one" requirement holds.
func IsObject(raw []byte) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return json.Valid(raw)
		default:
			return false
		}
	}
	return false
}

// Equal compares two JSON values after stable encoding.
//
// Identical bytes short-circuit without decoding; only differing encodings
// fall back to decoded-tree comparison, which is order-independent and
// preserves number lexemes (json.Number), matching the canonical form.
func Equal(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func decode(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("empty JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON value: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return value, nil
}

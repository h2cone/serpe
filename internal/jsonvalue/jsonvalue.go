// Package jsonvalue implements Ouro's stable JSON value semantics. Its
// canonical form ignores whitespace and object-key order, preserves number
// lexemes, and deliberately does not claim RFC 8785 conformance.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
func IsObject(raw []byte) bool {
	_, err := CanonicalObject(raw)
	return err == nil
}

// Equal compares two JSON values after stable encoding.
func Equal(left, right []byte) bool {
	leftCanonical, leftErr := Canonical(left)
	rightCanonical, rightErr := Canonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
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

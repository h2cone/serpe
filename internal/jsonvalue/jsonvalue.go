// Package jsonvalue implements Serpe's stable JSON value semantics. Its
// canonical form ignores whitespace and object-key order, preserves number
// lexemes, and deliberately does not claim RFC 8785 conformance.
package jsonvalue

import "bytes"

func canonicalLimits() Limits {
	return Limits{
		MaxDepth:       defaultMaxDepth,
		MaxNodes:       1_048_576,
		MaxNumberBytes: defaultMaxNumberBytes,
		MaxExponent:    defaultMaxExponent,
		MaxScale:       defaultMaxScale,
	}
}

// Canonical returns a stable encoding of one JSON value.
func Canonical(raw []byte) ([]byte, error) {
	value, err := Parse(raw, canonicalLimits())
	if err != nil {
		return nil, err
	}
	return CanonicalValue(value)
}

// CanonicalObject returns a stable encoding of one JSON object.
func CanonicalObject(raw []byte) ([]byte, error) {
	value, err := ParseObject(raw, ObjectLimits())
	if err != nil {
		return nil, err
	}
	return CanonicalValue(value)
}

// IsObject reports whether raw contains exactly one JSON object under the
// strict parser (duplicate keys are rejected).
func IsObject(raw []byte) bool {
	_, err := ParseObject(raw, ObjectLimits())
	return err == nil
}

// Equal compares two JSON values after stable encoding.
func Equal(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	leftCanon, leftErr := Canonical(left)
	rightCanon, rightErr := Canonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanon, rightCanon)
}

package shared

import (
	jsonv2 "encoding/json/v2"

	"github.com/h2cone/serpe/internal/jsonvalue"
)

// EncodeJSON marshals v with encoding/json/v2. Object members are emitted in
// deterministic order; HTML is not escaped; there is no trailing newline.
func EncodeJSON(v any) ([]byte, error) {
	return jsonv2.Marshal(v, jsonv2.Deterministic(true))
}

// DecodeJSON unmarshals one JSON value with encoding/json/v2 defaults
// (case-sensitive names, reject duplicates, reject invalid UTF-8).
func DecodeJSON(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v)
}

// JSONObject reports whether raw is a valid JSON object value.
func JSONObject(raw []byte) bool {
	return jsonvalue.IsObject(raw)
}

var (
	unaryJSONLimits = jsonvalue.Limits{
		MaxDepth:       128,
		MaxNodes:       1_048_576,
		MaxNumberBytes: 128,
		MaxExponent:    1_000,
		MaxScale:       1_024,
	}
	streamJSONLimits = jsonvalue.Limits{
		MaxDepth:       128,
		MaxNodes:       262_144,
		MaxNumberBytes: 128,
		MaxExponent:    1_000,
		MaxScale:       1_024,
	}
	errorJSONLimits = jsonvalue.Limits{
		MaxDepth:       64,
		MaxNodes:       8_192,
		MaxNumberBytes: 128,
		MaxExponent:    1_000,
		MaxScale:       1_024,
	}
)

// ValidateUnaryJSON validates one provider unary response before a protocol
// DTO can replace invalid Unicode or choose a duplicate object member.
func ValidateUnaryJSON(raw []byte) error {
	_, err := jsonvalue.Parse(raw, unaryJSONLimits)
	return err
}

// ValidateStreamJSON validates one SSE data payload before protocol decoding.
func ValidateStreamJSON(raw []byte) error {
	_, err := jsonvalue.Parse(raw, streamJSONLimits)
	return err
}

// ValidateErrorJSON validates one bounded provider error response before its
// public message and code are extracted.
func ValidateErrorJSON(raw []byte) error {
	_, err := jsonvalue.Parse(raw, errorJSONLimits)
	return err
}

package shared

import "github.com/h2cone/serpe/internal/jsonvalue"

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

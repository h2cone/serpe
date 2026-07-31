package shared

import (
	"bytes"
	"encoding/json"
)

// JSONObject reports whether raw is a valid JSON object value.
func JSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

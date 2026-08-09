package shared

import "github.com/h2cone/serpe/internal/jsonvalue"

// JSONObject reports whether raw is a valid JSON object value.
func JSONObject(raw []byte) bool {
	return jsonvalue.IsObject(raw)
}

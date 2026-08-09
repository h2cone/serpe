package shared

import (
	"encoding/json"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestEquivalentContentComparesToolArgumentsAsJSON(t *testing.T) {
	left := []models.Content{models.ToolCallContent("call-1", "lookup", json.RawMessage(`{"x":1,"nested":{"a":true,"b":2}}`))}
	right := []models.Content{models.ToolCallContent("call-1", "lookup", json.RawMessage(` { "nested": { "b": 2, "a": true }, "x": 1 } `))}
	if !EquivalentContent(left, right) {
		t.Fatal("semantically equivalent tool arguments were considered different")
	}
	right[0].ToolCall.Arguments = json.RawMessage(`{"x":2,"nested":{"a":true,"b":2}}`)
	if EquivalentContent(left, right) {
		t.Fatal("different tool arguments were considered equivalent")
	}
}

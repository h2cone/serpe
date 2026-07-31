package shared

import (
	"bytes"
	"encoding/json"

	"github.com/h2cone/ouro/core/models"
)

// EquivalentContent reports whether two normalized content sequences carry the
// same meaning. Tool arguments are compared as JSON values so insignificant
// whitespace and object-key order do not break provider-state round trips.
func EquivalentContent(left, right []models.Content) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !equivalentContent(left[index], right[index]) {
			return false
		}
	}
	return true
}

func equivalentContent(left, right models.Content) bool {
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case models.ContentText:
		return left.Text != nil && right.Text != nil && left.Text.Text == right.Text.Text
	case models.ContentImage:
		return left.Image != nil && right.Image != nil &&
			left.Image.URI == right.Image.URI &&
			left.Image.MIMEType == right.Image.MIMEType &&
			left.Image.Detail == right.Image.Detail &&
			bytes.Equal(left.Image.Data, right.Image.Data)
	case models.ContentToolCall:
		return left.ToolCall != nil && right.ToolCall != nil &&
			left.ToolCall.ID == right.ToolCall.ID &&
			left.ToolCall.Name == right.ToolCall.Name &&
			equivalentJSON(left.ToolCall.Arguments, right.ToolCall.Arguments)
	case models.ContentToolResult:
		return left.ToolResult != nil && right.ToolResult != nil &&
			left.ToolResult.CallID == right.ToolResult.CallID &&
			left.ToolResult.Name == right.ToolResult.Name &&
			left.ToolResult.IsError == right.ToolResult.IsError &&
			EquivalentContent(left.ToolResult.Content, right.ToolResult.Content)
	case models.ContentReasoningSummary:
		return left.ReasoningSummary != nil && right.ReasoningSummary != nil &&
			left.ReasoningSummary.Text == right.ReasoningSummary.Text
	case models.ContentRefusal:
		return left.Refusal != nil && right.Refusal != nil &&
			left.Refusal.Text == right.Refusal.Text
	default:
		return false
	}
}

func equivalentJSON(left, right []byte) bool {
	leftCanonical, leftOK := canonicalJSON(left)
	rightCanonical, rightOK := canonicalJSON(right)
	return leftOK && rightOK && bytes.Equal(leftCanonical, rightCanonical)
}

func canonicalJSON(raw []byte) ([]byte, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	canonical, err := json.Marshal(value)
	return canonical, err == nil
}

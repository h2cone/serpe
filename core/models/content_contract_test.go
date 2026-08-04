package models_test

import (
	"encoding/json"
	"testing"

	"github.com/h2cone/ouro/core/models"
)

func TestContentValidateCloneEqualByKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content models.Content
		mutate  func(*models.Content)
	}{
		{name: "text", content: models.Text("x"), mutate: func(c *models.Content) { c.Text.Text = "y" }},
		{name: "image", content: models.ImageBytes("image/png", []byte{1, 2}), mutate: func(c *models.Content) { c.Image.Data[0] = 9 }},
		{name: "tool_call", content: models.ToolCallContent("c", "f", json.RawMessage(`{"b":1,"a":2}`)), mutate: func(c *models.Content) { c.ToolCall.Name = "g" }},
		{name: "tool_result", content: models.ToolResultContent("c", "f", false, models.Text("ok")), mutate: func(c *models.Content) { c.ToolResult.Content[0].Text.Text = "changed" }},
		{name: "reasoning", content: models.ReasoningSummary("summary"), mutate: func(c *models.Content) { c.ReasoningSummary.Text = "changed" }},
		{name: "refusal", content: models.Refusal("no"), mutate: func(c *models.Content) { c.Refusal.Text = "changed" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.content.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			clone := test.content.Clone()
			if !test.content.Equal(clone) {
				t.Fatal("clone is not equal")
			}
			test.mutate(&clone)
			if test.content.Equal(clone) {
				t.Fatal("mutation remained equal")
			}
		})
	}
}

func TestContentEqualUsesJSONValueSemantics(t *testing.T) {
	t.Parallel()
	left := models.ToolCallContent("c", "f", json.RawMessage(`{"b":1,"a":2}`))
	right := models.ToolCallContent("c", "f", json.RawMessage(` { "a": 2, "b": 1 } `))
	if !left.Equal(right) {
		t.Fatal("object key order and whitespace changed equality")
	}
	right.ToolCall.Arguments = json.RawMessage(`{"a":2,"b":1.0}`)
	if left.Equal(right) {
		t.Fatal("different number lexemes were considered equal")
	}
}

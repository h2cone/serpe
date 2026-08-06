package models_test

import (
	"bytes"
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

func TestContentCanonicalBytesMatchesEqualSemantics(t *testing.T) {
	t.Parallel()
	equal := [][2]models.Content{
		{models.Text("a"), models.Text("a")},
		{models.ImageURI("https://example.com/a.png"), models.ImageURI("https://example.com/a.png")},
		{models.ImageBytes("image/png", []byte{1, 2}), models.ImageBytes("image/png", []byte{1, 2})},
		{
			models.ToolCallContent("c", "f", json.RawMessage(`{"b":1,"a":2}`)),
			models.ToolCallContent("c", "f", json.RawMessage(` { "a": 2, "b": 1 } `)),
		},
		{
			models.ToolResultContent("c", "f", true, models.Text("ok")),
			models.ToolResultContent("c", "f", true, models.Text("ok")),
		},
		{models.ReasoningSummary("a"), models.ReasoningSummary("a")},
		{models.Refusal("a"), models.Refusal("a")},
	}
	for i, pair := range equal {
		left, err := pair[0].CanonicalBytes()
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		right, err := pair[1].CanonicalBytes()
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !bytes.Equal(left, right) {
			t.Fatalf("case %d: equal contents encoded differently", i)
		}
	}

	different := [][2]models.Content{
		{models.Text("a"), models.Text("b")},
		{models.ImageBytes("image/png", []byte{1}), models.ImageBytes("image/png", []byte{2})},
		{models.ReasoningSummary("a"), models.ReasoningSummary("b")},
		{models.Refusal("a"), models.Refusal("b")},
	}
	for i, pair := range different {
		left, err := pair[0].CanonicalBytes()
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		right, err := pair[1].CanonicalBytes()
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if bytes.Equal(left, right) {
			t.Fatalf("case %d: different contents encoded identically", i)
		}
	}

	if _, err := (models.Content{}).CanonicalBytes(); err == nil {
		t.Fatal("invalid content must fail to encode")
	}
}

func TestContentCanonicalBytesFramingIsUnambiguous(t *testing.T) {
	t.Parallel()
	// Nested tool-result children must not collide with a flat list of the
	// same child contents.
	nested, err := models.ToolResultContent("c", "f", false, models.Text("a")).CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	flat, err := models.Text("a").CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(nested, flat) {
		t.Fatal("nested content framing collided with flat content")
	}
}

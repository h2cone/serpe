package models_test

import (
	"encoding/json"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestContentCodecRoundTrip(t *testing.T) {
	img := models.ImageBytes("image/png", []byte{0x89, 0x50})
	img.Image.Detail = models.ImageDetailLow
	blocks := []models.Content{
		models.Text("hello"),
		models.ReasoningSummary("think"),
		models.Refusal("no"),
		models.ImageURI("https://example.com/a.png"),
		img,
		models.ToolCallContent("c1", "now", json.RawMessage(`{"a":1}`)),
		models.ToolResultContent("c1", "now", false, models.Text("ok"), models.ImageURI("https://example.com/t.png")),
		models.ToolResultContent("c2", "fail", true, models.Text("err")),
	}
	for i, c := range blocks {
		rec, err := models.EncodeContent(c)
		if err != nil {
			t.Fatalf("block %d EncodeContent: %v", i, err)
		}
		got, err := models.DecodeContent(rec)
		if err != nil {
			t.Fatalf("block %d DecodeContent: %v", i, err)
		}
		if !c.Equal(got) {
			t.Fatalf("block %d round-trip mismatch\nwant %+v\ngot  %+v", i, c, got)
		}
		raw, err := models.MarshalContent(c)
		if err != nil {
			t.Fatalf("block %d MarshalContent: %v", i, err)
		}
		got2, err := models.UnmarshalContent(raw)
		if err != nil {
			t.Fatalf("block %d UnmarshalContent: %v", i, err)
		}
		if !c.Equal(got2) {
			t.Fatalf("block %d JSON round-trip mismatch", i)
		}
	}
}

func TestContentAccessors(t *testing.T) {
	if models.Text("hi").PlainText() != "hi" {
		t.Fatal("PlainText")
	}
	if models.ImageURI("u").PlainText() != "" {
		t.Fatal("PlainText on non-text")
	}
	if text, ok := models.ReasoningSummary("r").TextValue(); !ok || text != "r" {
		t.Fatalf("TextValue reasoning = %q %v", text, ok)
	}
	if text, ok := models.Refusal("no").TextValue(); !ok || text != "no" {
		t.Fatalf("TextValue refusal = %q %v", text, ok)
	}
	if _, ok := models.ImageURI("u").TextValue(); ok {
		t.Fatal("TextValue on image")
	}

	msg := models.NewAssistantMessage(
		models.Text("x"),
		models.ToolCallContent("id", "f", json.RawMessage(`{}`)),
		models.ToolCallContent("id2", "g", json.RawMessage(`{"n":1}`)),
	)
	calls := models.ToolCalls(msg)
	if len(calls) != 2 || calls[0].Name != "f" || calls[1].Name != "g" {
		t.Fatalf("ToolCalls = %+v", calls)
	}
	// Defensive copy of arguments.
	calls[0].Arguments[0] = 'x'
	again := models.ToolCalls(msg)
	if again[0].Arguments[0] == 'x' {
		t.Fatal("ToolCalls must copy arguments")
	}
}

func TestDecodeContentRejectsUnknown(t *testing.T) {
	if _, err := models.DecodeContent(models.ContentRecord{Type: "audio"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := models.EncodeContent(models.Content{}); err == nil {
		t.Fatal("expected error for empty content")
	}
}

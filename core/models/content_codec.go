package models

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/h2cone/ouro/internal/jsonvalue"
)

// ContentRecord is the stable JSON shape for a Content block. Field names are
// independent of Go struct identifiers so persistence and interop layers can
// share one content-kind table without hand-written switches of their own.
//
// Only fields relevant to Type are set. Nested tool_result children use the
// same record shape.
type ContentRecord struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	MIME      string          `json:"mime,omitempty"`
	URI       string          `json:"uri,omitempty"`
	Data      string          `json:"data,omitempty"` // base64 for inline media
	Detail    string          `json:"detail,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Content   []ContentRecord `json:"content,omitempty"`
}

// EncodeContent maps a validated Content into the stable record shape.
// This is the single authority for content-kind serialization knowledge.
func EncodeContent(c Content) (ContentRecord, error) {
	if err := c.Validate(); err != nil {
		return ContentRecord{}, err
	}
	switch c.Kind {
	case ContentText:
		return ContentRecord{Type: string(ContentText), Text: c.Text.Text}, nil
	case ContentReasoningSummary:
		return ContentRecord{Type: string(ContentReasoningSummary), Text: c.ReasoningSummary.Text}, nil
	case ContentRefusal:
		return ContentRecord{Type: string(ContentRefusal), Text: c.Refusal.Text}, nil
	case ContentImage:
		rec := ContentRecord{Type: string(ContentImage), Detail: string(c.Image.Detail)}
		if c.Image.URI != "" {
			rec.URI = c.Image.URI
			return rec, nil
		}
		rec.MIME = c.Image.MIMEType
		rec.Data = base64.StdEncoding.EncodeToString(c.Image.Data)
		return rec, nil
	case ContentToolCall:
		if !jsonvalue.IsObject(c.ToolCall.Arguments) {
			return ContentRecord{}, fmt.Errorf("content: tool call arguments must be a JSON object")
		}
		return ContentRecord{
			Type:      string(ContentToolCall),
			ID:        c.ToolCall.ID,
			Name:      c.ToolCall.Name,
			Arguments: append(json.RawMessage(nil), c.ToolCall.Arguments...),
		}, nil
	case ContentToolResult:
		children := make([]ContentRecord, len(c.ToolResult.Content))
		for i := range c.ToolResult.Content {
			child, err := EncodeContent(c.ToolResult.Content[i])
			if err != nil {
				return ContentRecord{}, fmt.Errorf("content: tool_result child %d: %w", i, err)
			}
			children[i] = child
		}
		return ContentRecord{
			Type:    string(ContentToolResult),
			CallID:  c.ToolResult.CallID,
			Name:    c.ToolResult.Name,
			IsError: c.ToolResult.IsError,
			Content: children,
		}, nil
	default:
		return ContentRecord{}, fmt.Errorf("content: unknown kind %q", c.Kind)
	}
}

// DecodeContent rebuilds Content from a ContentRecord. The result is validated.
func DecodeContent(rec ContentRecord) (Content, error) {
	var c Content
	var err error
	switch rec.Type {
	case string(ContentText):
		c = Text(rec.Text)
	case string(ContentReasoningSummary):
		c = ReasoningSummary(rec.Text)
	case string(ContentRefusal):
		c = Refusal(rec.Text)
	case string(ContentImage):
		if rec.URI != "" {
			c = ImageURI(rec.URI)
			if rec.Detail != "" {
				c.Image.Detail = ImageDetail(rec.Detail)
			}
		} else {
			if rec.MIME == "" || rec.Data == "" {
				return Content{}, fmt.Errorf("content: image requires uri or mime+data")
			}
			raw, decErr := base64.StdEncoding.DecodeString(rec.Data)
			if decErr != nil {
				return Content{}, fmt.Errorf("content: image data: %w", decErr)
			}
			c = ImageBytes(rec.MIME, raw)
			if rec.Detail != "" {
				c.Image.Detail = ImageDetail(rec.Detail)
			}
		}
	case string(ContentToolCall):
		if !jsonvalue.IsObject(rec.Arguments) {
			return Content{}, fmt.Errorf("content: tool call arguments must be a JSON object")
		}
		c = ToolCallContent(rec.ID, rec.Name, rec.Arguments)
	case string(ContentToolResult):
		children := make([]Content, len(rec.Content))
		for i := range rec.Content {
			child, childErr := DecodeContent(rec.Content[i])
			if childErr != nil {
				return Content{}, fmt.Errorf("content: tool_result child %d: %w", i, childErr)
			}
			children[i] = child
		}
		c = ToolResultContent(rec.CallID, rec.Name, rec.IsError, children...)
	default:
		return Content{}, fmt.Errorf("content: unknown type %q", rec.Type)
	}
	if err = c.Validate(); err != nil {
		return Content{}, err
	}
	return c, nil
}

// MarshalContent encodes Content as stable JSON bytes via ContentRecord.
func MarshalContent(c Content) ([]byte, error) {
	rec, err := EncodeContent(c)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rec)
}

// UnmarshalContent decodes stable JSON bytes into Content.
func UnmarshalContent(data []byte) (Content, error) {
	var rec ContentRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return Content{}, fmt.Errorf("content: decode: %w", err)
	}
	return DecodeContent(rec)
}

// PlainText returns the text payload for ContentText, or empty if not text.
func (c Content) PlainText() string {
	if c.Kind != ContentText || c.Text == nil {
		return ""
	}
	return c.Text.Text
}

// TextValue returns the string payload for text-like kinds (text,
// reasoning_summary, refusal). ok is false for other kinds or nil variants.
func (c Content) TextValue() (text string, ok bool) {
	switch c.Kind {
	case ContentText:
		if c.Text == nil {
			return "", false
		}
		return c.Text.Text, true
	case ContentReasoningSummary:
		if c.ReasoningSummary == nil {
			return "", false
		}
		return c.ReasoningSummary.Text, true
	case ContentRefusal:
		if c.Refusal == nil {
			return "", false
		}
		return c.Refusal.Text, true
	default:
		return "", false
	}
}

// ToolCalls extracts finalized tool calls from a message's content blocks.
func ToolCalls(msg Message) []ToolCall {
	var out []ToolCall
	for i := range msg.Content {
		if msg.Content[i].Kind != ContentToolCall || msg.Content[i].ToolCall == nil {
			continue
		}
		call := *msg.Content[i].ToolCall
		call.Arguments = append(json.RawMessage(nil), msg.Content[i].ToolCall.Arguments...)
		out = append(out, call)
	}
	return out
}

// Package canon is ouro's lingua franca. The agent loop and provider read and
// write only these types; any wire shape is confined to the codec layer.
// This package holds types only -- no I/O, no protocol-specific logic.
package canon

import "encoding/json"

// ContentBlock is one part of a message's content. The five concrete
// implementations below are distinguished by a "type" field when marshaled.
// See json.go for discriminant (un)marshaling.
type ContentBlock interface{ contentBlock() }

// TextBlock is plain text.
type TextBlock struct {
	Text  string         `json:"text"`
	Extra map[string]any `json:"-"` // block-level protocol metadata, e.g. Anthropic cache_control
}

// ImageBlock is an image referenced by URL or inlined as base64.
type ImageBlock struct {
	MediaType string         `json:"media_type,omitempty"` // "image/png", "image/jpeg", ...
	URL       string         `json:"url,omitempty"`        // set when the source is a URL
	Data      string         `json:"data,omitempty"`       // base64 payload when inlined
	Extra     map[string]any `json:"-"`
}

// ToolUseBlock is a tool call issued by the model. Input is raw JSON so any
// parameter shape is preserved and parsed lazily, avoiding float64 precision
// loss from map[string]any.
type ToolUseBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	Extra map[string]any  `json:"-"`
}

// ToolResultBlock is the result of a tool call, fed back to the model. Content
// is a slice so a result may carry text and image together (usually one TextBlock).
type ToolResultBlock struct {
	ToolUseID string         `json:"tool_use_id"`
	Content   []ContentBlock `json:"content"`
	IsError   bool           `json:"is_error,omitempty"`
	Extra     map[string]any `json:"-"`
}

// ThinkingBlock is non-explicit reasoning. Signature carries Anthropic's
// encrypted thinking signature (empty for protocols with no equivalent).
type ThinkingBlock struct {
	Text      string         `json:"thinking"`
	Signature string         `json:"signature,omitempty"`
	Extra     map[string]any `json:"-"`
}

func (*TextBlock) contentBlock()       {}
func (*ImageBlock) contentBlock()      {}
func (*ToolUseBlock) contentBlock()    {}
func (*ToolResultBlock) contentBlock() {}
func (*ThinkingBlock) contentBlock()   {}

// BlockType reports the canonical discriminator for a block. Unknown / nil
// blocks report "".
func BlockType(b ContentBlock) string {
	switch b.(type) {
	case *TextBlock:
		return "text"
	case *ImageBlock:
		return "image"
	case *ToolUseBlock:
		return "tool_use"
	case *ToolResultBlock:
		return "tool_result"
	case *ThinkingBlock:
		return "thinking"
	}
	return ""
}

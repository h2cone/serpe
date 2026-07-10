package canon

import (
	"encoding/json"
	"fmt"
)

// MarshalBlocks serializes a slice of ContentBlock to a JSON array, with each
// block carrying its "type" discriminator. It is the canonical block encoding;
// codecs translate between this and their wire shapes.
func MarshalBlocks(blocks []ContentBlock) (json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		if b == nil {
			continue
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return json.Marshal(out)
}

// UnmarshalBlocks parses a JSON array of discriminated blocks into []ContentBlock.
func UnmarshalBlocks(data json.RawMessage) ([]ContentBlock, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, err
	}
	out := make([]ContentBlock, 0, len(raws))
	for _, raw := range raws {
		b, err := unmarshalBlock(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func unmarshalBlock(data json.RawMessage) (ContentBlock, error) {
	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &probe)
	switch probe.Type {
	case "text":
		var b TextBlock
		return &b, json.Unmarshal(data, &b)
	case "image":
		var b ImageBlock
		return &b, json.Unmarshal(data, &b)
	case "tool_use":
		var b ToolUseBlock
		return &b, json.Unmarshal(data, &b)
	case "tool_result":
		var b ToolResultBlock
		return &b, json.Unmarshal(data, &b)
	case "thinking":
		var b ThinkingBlock
		return &b, json.Unmarshal(data, &b)
	case "":
		return nil, fmt.Errorf("canonical content block is missing type")
	default:
		return nil, fmt.Errorf("unknown canonical content block type %q", probe.Type)
	}
}

// extras extracts unknown keys (everything except "type" and the known fields)
// into a map for the block's Extra. Returns nil when there is nothing extra.
func extras(data json.RawMessage, known ...string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	delete(m, "type")
	for _, k := range known {
		delete(m, k)
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// marshalBlock merges a type discriminator, the given fields, and Extra into a
// single JSON object. Fields whose values are zero strings/bools may still be
// included; callers pass only the fields they want emitted.
func marshalBlock(typeName string, fields map[string]any, extra map[string]any) ([]byte, error) {
	m := make(map[string]any, len(fields)+len(extra)+1)
	m["type"] = typeName
	for k, v := range fields {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// ---- TextBlock ----

func (b *TextBlock) MarshalJSON() ([]byte, error) {
	return marshalBlock("text", map[string]any{"text": b.Text}, b.Extra)
}

func (b *TextBlock) UnmarshalJSON(data []byte) error {
	var s struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.Text = s.Text
	b.Extra = extras(data, "text")
	return nil
}

// ---- ImageBlock ----

func (b *ImageBlock) MarshalJSON() ([]byte, error) {
	fields := map[string]any{}
	if b.MediaType != "" {
		fields["media_type"] = b.MediaType
	}
	if b.URL != "" {
		fields["url"] = b.URL
	}
	if b.Data != "" {
		fields["data"] = b.Data
	}
	return marshalBlock("image", fields, b.Extra)
}

func (b *ImageBlock) UnmarshalJSON(data []byte) error {
	var s struct {
		MediaType string `json:"media_type"`
		URL       string `json:"url"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.MediaType = s.MediaType
	b.URL = s.URL
	b.Data = s.Data
	b.Extra = extras(data, "media_type", "url", "data")
	return nil
}

// ---- ToolUseBlock ----

func (b *ToolUseBlock) MarshalJSON() ([]byte, error) {
	input := b.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return marshalBlock("tool_use", map[string]any{
		"id":    b.ID,
		"name":  b.Name,
		"input": input,
	}, b.Extra)
}

func (b *ToolUseBlock) UnmarshalJSON(data []byte) error {
	var s struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.ID = s.ID
	b.Name = s.Name
	b.Input = s.Input
	b.Extra = extras(data, "id", "name", "input")
	return nil
}

// ---- ToolResultBlock ----

func (b *ToolResultBlock) MarshalJSON() ([]byte, error) {
	fields := map[string]any{
		"tool_use_id": b.ToolUseID,
	}
	content, err := MarshalBlocks(b.Content)
	if err != nil {
		return nil, err
	}
	fields["content"] = content
	if b.IsError {
		fields["is_error"] = true
	}
	return marshalBlock("tool_result", fields, b.Extra)
}

func (b *ToolResultBlock) UnmarshalJSON(data []byte) error {
	var s struct {
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.ToolUseID = s.ToolUseID
	b.IsError = s.IsError
	blocks, err := UnmarshalBlocks(s.Content)
	if err != nil {
		return err
	}
	b.Content = blocks
	b.Extra = extras(data, "tool_use_id", "content", "is_error")
	return nil
}

// ---- ThinkingBlock ----

func (b *ThinkingBlock) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"thinking": b.Text}
	if b.Signature != "" {
		fields["signature"] = b.Signature
	}
	return marshalBlock("thinking", fields, b.Extra)
}

func (b *ThinkingBlock) UnmarshalJSON(data []byte) error {
	var s struct {
		Text      string `json:"thinking"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	b.Text = s.Text
	b.Signature = s.Signature
	b.Extra = extras(data, "thinking", "signature")
	return nil
}

// ---- Message ----

func (m *Message) MarshalJSON() ([]byte, error) {
	content, err := MarshalBlocks(m.Content)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"role":    string(m.Role),
		"content": content,
	}
	for k, v := range m.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var s struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	m.Role = Role(s.Role)
	blocks, err := UnmarshalBlocks(s.Content)
	if err != nil {
		return err
	}
	m.Content = blocks
	m.Extra = extras(data, "role", "content")
	return nil
}

// ---- Conversation ----

func (c *Conversation) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	if c.System != "" {
		out["system"] = c.System
	}
	msgs := make([]json.RawMessage, 0, len(c.Messages))
	for i := range c.Messages {
		raw, err := json.Marshal(&c.Messages[i])
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, raw)
	}
	out["messages"] = msgs
	for k, v := range c.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (c *Conversation) UnmarshalJSON(data []byte) error {
	var s struct {
		System   string            `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	c.System = s.System
	c.Messages = make([]Message, 0, len(s.Messages))
	for _, raw := range s.Messages {
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		c.Messages = append(c.Messages, m)
	}
	c.Extra = extras(data, "system", "messages")
	return nil
}

// ---- Response ----

func (r *Response) MarshalJSON() ([]byte, error) {
	content, err := MarshalBlocks(r.Content)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id":            r.ID,
		"model":         r.Model,
		"content":       content,
		"finish_reason": string(r.FinishReason),
		"usage":         r.Usage,
	}
	if r.Provider != "" {
		out["provider"] = r.Provider
	}
	for k, v := range r.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (r *Response) UnmarshalJSON(data []byte) error {
	var s struct {
		ID           string          `json:"id"`
		Model        string          `json:"model"`
		Content      json.RawMessage `json:"content"`
		FinishReason string          `json:"finish_reason"`
		Usage        Usage           `json:"usage"`
		Provider     string          `json:"provider"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	r.ID = s.ID
	r.Model = s.Model
	r.FinishReason = FinishReason(s.FinishReason)
	r.Usage = s.Usage
	r.Provider = s.Provider
	blocks, err := UnmarshalBlocks(s.Content)
	if err != nil {
		return err
	}
	r.Content = blocks
	r.Extra = extras(data, "id", "model", "content", "finish_reason", "usage", "provider")
	return nil
}

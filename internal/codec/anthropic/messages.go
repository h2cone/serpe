// Package anthropic contains the codec for the Anthropic Messages API wire shape.
package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/sse"
)

const (
	defaultMaxTokens          = 4096
	redactedThinkingMarker    = "anthropic.redacted"
	redactedThinkingItemExtra = "anthropic.redacted_thinking"
)

// MessagesCodec translates between canonical and the Anthropic Messages wire
// shape (POST /v1/messages). The canonical content-block taxonomy is taken from
// Anthropic, so this mapping is the most direct and near-lossless.
type MessagesCodec struct{}

// Name returns the protocol identifier.
func (MessagesCodec) Name() string { return "anthropic-messages" }

// EncodeRequest encodes a canonical Request into an Anthropic Messages wire body.
func (MessagesCodec) EncodeRequest(req *canon.Request) ([]byte, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": encodeAnthropicMessages(req.Conversation.Messages),
	}
	if req.Conversation.System != "" {
		body["system"] = req.Conversation.System
	}
	if v, ok := req.Conversation.Extra["anthropic.system_blocks"]; ok {
		body["system"] = v
	}
	if tools := encodeAnthropicTools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if c, ok := encodeAnthropicToolChoice(req.ToolChoice); ok {
		body["tool_choice"] = c
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
		log.Printf("anthropic-messages: MaxTokens unset, defaulting to %d", defaultMaxTokens)
	}
	body["max_tokens"] = maxTokens
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}
	if req.Stream {
		body["stream"] = true
	}
	if v, ok := req.Extra["anthropic.cache_control"]; ok {
		body["cache_control"] = v
	}
	if v, ok := req.Extra["top_k"]; ok {
		body["top_k"] = v
	}
	return json.Marshal(body)
}

// DecodeRequest parses an Anthropic Messages wire body into a canonical Request.
func (MessagesCodec) DecodeRequest(data []byte) (*canon.Request, error) {
	var wire struct {
		Model       string            `json:"model"`
		System      json.RawMessage   `json:"system"`
		Messages    []json.RawMessage `json:"messages"`
		Tools       []json.RawMessage `json:"tools"`
		ToolChoice  json.RawMessage   `json:"tool_choice"`
		Temperature *float64          `json:"temperature"`
		TopP        *float64          `json:"top_p"`
		MaxTokens   int               `json:"max_tokens"`
		Stop        []string          `json:"stop_sequences"`
		Stream      bool              `json:"stream"`
		TopK        json.RawMessage   `json:"top_k"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("anthropic-messages decode request: %w", err)
	}

	req := &canon.Request{
		Model: wire.Model,
		Conversation: canon.Conversation{
			Messages: decodeAnthropicMessages(wire.Messages),
		},
		Temperature: wire.Temperature,
		TopP:        wire.TopP,
		MaxTokens:   wire.MaxTokens,
		Stop:        wire.Stop,
		Stream:      wire.Stream,
	}
	req.Conversation.System, req.Conversation.Extra = decodeAnthropicSystem(wire.System)

	var err error
	req.Tools, err = decodeAnthropicTools(wire.Tools)
	if err != nil {
		return nil, err
	}
	if len(wire.ToolChoice) > 0 && string(wire.ToolChoice) != "null" {
		req.ToolChoice, err = decodeAnthropicToolChoice(wire.ToolChoice)
		if err != nil {
			return nil, err
		}
	}
	if len(wire.TopK) > 0 && string(wire.TopK) != "null" {
		if req.Extra == nil {
			req.Extra = map[string]any{}
		}
		req.Extra["top_k"] = json.RawMessage(wire.TopK)
	}
	return req, nil
}

// DecodeResponse parses an Anthropic Messages wire body into a canonical Response.
func (MessagesCodec) DecodeResponse(data []byte) (*canon.Response, error) {
	var wire struct {
		ID           string            `json:"id"`
		Model        string            `json:"model"`
		Content      []json.RawMessage `json:"content"`
		StopReason   string            `json:"stop_reason"`
		StopSequence string            `json:"stop_sequence"`
		Usage        struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Type  string `json:"type"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("anthropic-messages decode response: %w", err)
	}
	if wire.Error != nil {
		return nil, fmt.Errorf("anthropic-messages: %s", wire.Error.Message)
	}
	resp := &canon.Response{
		ID:           wire.ID,
		Model:        wire.Model,
		Provider:     "anthropic-messages",
		FinishReason: anthropicStopToCanon(wire.StopReason),
		Usage: canon.Usage{
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
			CacheRead:    wire.Usage.CacheReadInputTokens,
			CacheWrite:   wire.Usage.CacheCreationInputTokens,
		},
	}
	if wire.StopReason == "stop_sequence" && wire.StopSequence != "" {
		if resp.Extra == nil {
			resp.Extra = map[string]any{}
		}
		resp.Extra["anthropic.stop_sequence"] = wire.StopSequence
	}
	blocks, err := decodeAnthropicContent(wire.Content)
	if err != nil {
		return nil, err
	}
	resp.Content = blocks
	return resp, nil
}

// EncodeResponse renders a canonical Response into an Anthropic Messages wire body.
func (MessagesCodec) EncodeResponse(r *canon.Response) ([]byte, error) {
	content := make([]any, 0, len(r.Content))
	for _, b := range r.Content {
		content = append(content, encodeAnthropicBlock(b))
	}
	stopReason, stopSequence := canonToAnthropicStop(r.FinishReason)
	if v, ok := r.Extra["anthropic.stop_sequence"].(string); ok && v != "" {
		stopSequence = v
	}
	body := map[string]any{
		"id":            r.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         r.Model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": stopSequence,
		"usage": map[string]any{
			"input_tokens":                r.Usage.InputTokens,
			"output_tokens":               r.Usage.OutputTokens,
			"cache_read_input_tokens":     r.Usage.CacheRead,
			"cache_creation_input_tokens": r.Usage.CacheWrite,
		},
	}
	return json.Marshal(body)
}

// EncodeError renders an Anthropic-shaped error body.
func (MessagesCodec) EncodeError(status int, err error) ([]byte, int) {
	if status == 0 {
		status = 500
	}
	errType := "api_error"
	if status >= 400 && status < 500 {
		errType = "invalid_request_error"
	}
	msg := "internal error"
	if err != nil {
		msg = err.Error()
	}
	body := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": msg,
		},
	}
	data, _ := json.Marshal(body)
	return data, status
}

// ---- request helpers ----

func encodeAnthropicMessages(msgs []canon.Message) []any {
	out := make([]any, 0, len(msgs))
	for _, msg := range msgs {
		parts := make([]any, 0, len(msg.Content))
		for _, b := range msg.Content {
			parts = append(parts, encodeAnthropicBlock(b))
		}
		m := map[string]any{"role": string(msg.Role), "content": parts}
		for k, v := range msg.Extra {
			m[k] = v
		}
		out = append(out, m)
	}
	return out
}

func encodeAnthropicBlock(b canon.ContentBlock) any {
	switch blk := b.(type) {
	case *canon.TextBlock:
		m := map[string]any{"type": "text", "text": blk.Text}
		mergeExtra(m, blk.Extra)
		return m
	case *canon.ImageBlock:
		source := map[string]any{}
		if blk.URL != "" {
			source["type"] = "url"
			source["url"] = blk.URL
		} else {
			source["type"] = "base64"
			source["media_type"] = blk.MediaType
			source["data"] = blk.Data
		}
		m := map[string]any{"type": "image", "source": source}
		mergeExtra(m, blk.Extra)
		return m
	case *canon.ToolUseBlock:
		input := blk.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		m := map[string]any{"type": "tool_use", "id": blk.ID, "name": blk.Name, "input": input}
		mergeExtra(m, blk.Extra)
		return m
	case *canon.ToolResultBlock:
		content := make([]any, 0, len(blk.Content))
		for _, c := range blk.Content {
			content = append(content, encodeAnthropicBlock(c))
		}
		m := map[string]any{"type": "tool_result", "tool_use_id": blk.ToolUseID, "content": content}
		if blk.IsError {
			m["is_error"] = true
		}
		mergeExtra(m, blk.Extra)
		return m
	case *canon.ThinkingBlock:
		if item, ok := redactedThinkingItem(blk.Extra); ok {
			return item
		}
		m := map[string]any{"type": "thinking", "thinking": blk.Text}
		if blk.Signature != "" {
			m["signature"] = blk.Signature
		}
		mergeExtra(m, blk.Extra)
		return m
	}
	return map[string]any{"type": "text", "text": ""}
}

func mergeExtra(m map[string]any, extra map[string]any) {
	for k, v := range extra {
		m[k] = v
	}
}

func decodeAnthropicMessages(raws []json.RawMessage) []canon.Message {
	var msgs []canon.Message
	var cur *canon.Message
	flush := func() {
		if cur != nil {
			msgs = append(msgs, *cur)
			cur = nil
		}
	}
	for _, raw := range raws {
		var item struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		blocks := decodeAnthropicContentShorthand(item.Content)
		// Merge adjacent same-role turns (Anthropic accepts them).
		if cur != nil && cur.Role == canon.Role(item.Role) {
			cur.Content = append(cur.Content, blocks...)
			continue
		}
		flush()
		cur = &canon.Message{Role: canon.Role(item.Role), Content: blocks}
		var extra map[string]any
		_ = json.Unmarshal(raw, &extra)
		delete(extra, "role")
		delete(extra, "content")
		if len(extra) > 0 {
			cur.Extra = extra
		}
	}
	flush()
	return msgs
}

// decodeAnthropicContentShorthand accepts a string content or a block array.
func decodeAnthropicContentShorthand(raw json.RawMessage) []canon.ContentBlock {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return []canon.ContentBlock{&canon.TextBlock{Text: asStr}}
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	out, _ := decodeAnthropicContent(blocks)
	return out
}

func decodeAnthropicContent(raws []json.RawMessage) ([]canon.ContentBlock, error) {
	out := make([]canon.ContentBlock, 0, len(raws))
	for _, raw := range raws {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &probe)
		switch probe.Type {
		case "text":
			var b struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(raw, &b)
			out = append(out, &canon.TextBlock{Text: b.Text})
		case "image":
			out = append(out, decodeAnthropicImage(raw))
		case "tool_use":
			var b struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(raw, &b)
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			out = append(out, &canon.ToolUseBlock{ID: b.ID, Name: b.Name, Input: input})
		case "tool_result":
			out = append(out, decodeAnthropicToolResult(raw))
		case "thinking":
			var b struct {
				Text      string `json:"thinking"`
				Signature string `json:"signature"`
			}
			_ = json.Unmarshal(raw, &b)
			out = append(out, &canon.ThinkingBlock{Text: b.Text, Signature: b.Signature})
		case "redacted_thinking":
			out = append(out, decodeRedactedThinking(raw))
		}
	}
	return out, nil
}

func decodeRedactedThinking(raw json.RawMessage) *canon.ThinkingBlock {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		item = map[string]any{"type": "redacted_thinking"}
	}
	return redactedThinkingBlock(item)
}

func redactedThinkingBlock(item map[string]any) *canon.ThinkingBlock {
	return &canon.ThinkingBlock{Extra: map[string]any{
		redactedThinkingMarker:    true,
		redactedThinkingItemExtra: cloneMap(item),
	}}
}

func redactedThinkingItem(extra map[string]any) (map[string]any, bool) {
	if extra == nil {
		return nil, false
	}
	item, ok := extra[redactedThinkingItemExtra].(map[string]any)
	if !ok || item == nil {
		return nil, false
	}
	out := cloneMap(item)
	out["type"] = "redacted_thinking"
	return out, true
}

func decodeAnthropicImage(raw json.RawMessage) *canon.ImageBlock {
	var b struct {
		Source struct {
			Type      string `json:"type"`
			URL       string `json:"url"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	_ = json.Unmarshal(raw, &b)
	if b.Source.Type == "url" {
		return &canon.ImageBlock{URL: b.Source.URL}
	}
	return &canon.ImageBlock{MediaType: b.Source.MediaType, Data: b.Source.Data}
}

func decodeAnthropicToolResult(raw json.RawMessage) *canon.ToolResultBlock {
	var b struct {
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	_ = json.Unmarshal(raw, &b)
	content := decodeAnthropicContentShorthand(b.Content)
	return &canon.ToolResultBlock{
		ToolUseID: b.ToolUseID,
		Content:   content,
		IsError:   b.IsError,
	}
}

func decodeAnthropicSystem(raw json.RawMessage) (string, map[string]any) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return asStr, nil
	}
	// Array of system text blocks.
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil
	}
	var texts []string
	for _, b := range blocks {
		var part struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(b, &part)
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n"), map[string]any{"anthropic.system_blocks": json.RawMessage(raw)}
}

func encodeAnthropicTools(tools []canon.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m := map[string]any{
			"name":         t.Name,
			"input_schema": rawOrEmpty(t.Parameters),
		}
		if t.Description != "" {
			m["description"] = t.Description
		}
		for k, v := range t.Extra {
			m[k] = v
		}
		out = append(out, m)
	}
	return out
}

func decodeAnthropicTools(raws []json.RawMessage) ([]canon.Tool, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]canon.Tool, 0, len(raws))
	for _, raw := range raws {
		var wire struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, fmt.Errorf("anthropic-messages decode tool: %w", err)
		}
		out = append(out, canon.Tool{
			Name:        wire.Name,
			Description: wire.Description,
			Parameters:  wire.InputSchema,
		})
	}
	return out, nil
}

func encodeAnthropicToolChoice(tc canon.ToolChoice) (any, bool) {
	switch tc.Mode {
	case canon.ToolChoiceAuto:
		return map[string]any{"type": "auto"}, true
	case canon.ToolChoiceRequired:
		return map[string]any{"type": "any"}, true
	case canon.ToolChoiceNone:
		return map[string]any{"type": "none"}, true
	case canon.ToolChoiceSpecific:
		if tc.Name == "" {
			return nil, false
		}
		return map[string]any{"type": "tool", "name": tc.Name}, true
	}
	return nil, false
}

func decodeAnthropicToolChoice(raw json.RawMessage) (canon.ToolChoice, error) {
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return canon.ToolChoice{}, fmt.Errorf("anthropic-messages decode tool_choice: %w", err)
	}
	switch obj.Type {
	case "auto":
		return canon.ToolChoice{Mode: canon.ToolChoiceAuto}, nil
	case "any":
		return canon.ToolChoice{Mode: canon.ToolChoiceRequired}, nil
	case "none":
		return canon.ToolChoice{Mode: canon.ToolChoiceNone}, nil
	case "tool":
		if obj.Name == "" {
			return canon.ToolChoice{}, fmt.Errorf("anthropic-messages: tool_choice tool missing name")
		}
		return canon.ToolChoice{Mode: canon.ToolChoiceSpecific, Name: obj.Name}, nil
	}
	return canon.ToolChoice{}, fmt.Errorf("anthropic-messages: unsupported tool_choice type %q", obj.Type)
}

func anthropicStopToCanon(s string) canon.FinishReason {
	switch s {
	case "end_turn":
		return canon.FinishStop
	case "max_tokens":
		return canon.FinishLength
	case "tool_use":
		return canon.FinishToolCalls
	case "stop_sequence":
		return canon.FinishStopSequence
	case "pause_turn":
		return canon.FinishPaused
	case "refusal":
		return canon.FinishContentFilter
	case "model_context_window_exceeded":
		return canon.FinishLength
	}
	return canon.FinishStop
}

func canonToAnthropicStop(fr canon.FinishReason) (reason string, stopSequence any) {
	switch fr {
	case canon.FinishLength:
		return "max_tokens", nil
	case canon.FinishToolCalls:
		return "tool_use", nil
	case canon.FinishStopSequence:
		return "stop_sequence", nil
	case canon.FinishPaused:
		return "pause_turn", nil
	case canon.FinishContentFilter:
		return "refusal", nil
	}
	return "end_turn", nil
}

func rawOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// ---- streaming: decode ----

func (MessagesCodec) DecodeStream(r io.Reader) (<-chan canon.Event, error) {
	ch := make(chan canon.Event, 16)
	go func() {
		defer close(ch)
		decoder := &anthropicStreamDecoder{}
		sr := sse.NewReader(r)
		for {
			ev, err := sr.Next()
			if err != nil {
				if err != io.EOF {
					ch <- canon.ErrorEvent{Err: fmt.Errorf("anthropic-messages stream: %w", err)}
				}
				return
			}
			if ev.Data == "" {
				continue
			}
			var data map[string]any
			if json.Unmarshal([]byte(ev.Data), &data) != nil {
				continue
			}
			decoder.handle(ev.Type, data, ch)
		}
	}()
	return ch, nil
}

type anthropicStreamDecoder struct {
	usage canon.Usage
}

func (d *anthropicStreamDecoder) handle(typ string, data map[string]any, ch chan canon.Event) {
	switch typ {
	case "message_start":
		msg := getMap(data, "message")
		usage := getMap(msg, "usage")
		d.mergeUsage(usage)
		ch <- canon.MessageStartEvent{Response: &canon.Response{
			ID:    getStr(msg, "id"),
			Model: getStr(msg, "model"),
			Usage: d.usage,
		}}
	case "content_block_start":
		idx := getInt(data, "index")
		block := getMap(data, "content_block")
		ch <- canon.ContentBlockStartEvent{Index: idx, Block: anthropicDecodeStreamBlock(block)}
	case "content_block_delta":
		idx := getInt(data, "index")
		delta := getMap(data, "delta")
		ch <- canon.ContentBlockDeltaEvent{Index: idx, Delta: anthropicDecodeStreamDelta(delta)}
	case "content_block_stop":
		ch <- canon.ContentBlockStopEvent{Index: getInt(data, "index")}
	case "message_delta":
		delta := getMap(data, "delta")
		usage := getMap(data, "usage")
		ev := canon.MessageDeltaEvent{}
		if stopReason := getStr(delta, "stop_reason"); stopReason != "" {
			ev.FinishReason = anthropicStopToCanon(stopReason)
		}
		if usage != nil {
			d.mergeUsage(usage)
			usageCopy := d.usage
			ev.Usage = &usageCopy
		}
		ch <- ev
	case "message_stop":
		ch <- canon.MessageStopEvent{}
	case "error":
		errObj := getMap(data, "error")
		ch <- canon.ErrorEvent{Err: fmt.Errorf("anthropic-messages stream error: %s", getStr(errObj, "message"))}
	}
}

func (d *anthropicStreamDecoder) mergeUsage(usage map[string]any) {
	if usage == nil {
		return
	}
	if _, ok := usage["input_tokens"]; ok {
		d.usage.InputTokens = getInt(usage, "input_tokens")
	}
	if _, ok := usage["output_tokens"]; ok {
		d.usage.OutputTokens = getInt(usage, "output_tokens")
	}
	if _, ok := usage["cache_read_input_tokens"]; ok {
		d.usage.CacheRead = getInt(usage, "cache_read_input_tokens")
	}
	if _, ok := usage["cache_creation_input_tokens"]; ok {
		d.usage.CacheWrite = getInt(usage, "cache_creation_input_tokens")
	}
}

func anthropicDecodeStreamBlock(block map[string]any) canon.ContentBlock {
	switch getStr(block, "type") {
	case "tool_use":
		return &canon.ToolUseBlock{ID: getStr(block, "id"), Name: getStr(block, "name")}
	case "thinking":
		return &canon.ThinkingBlock{}
	case "redacted_thinking":
		return redactedThinkingBlock(block)
	}
	return &canon.TextBlock{}
}

func anthropicDecodeStreamDelta(delta map[string]any) canon.Delta {
	switch getStr(delta, "type") {
	case "text_delta":
		return canon.Delta{Type: canon.DeltaText, Text: getStr(delta, "text")}
	case "input_json_delta":
		return canon.Delta{Type: canon.DeltaInputJSON, Partial: getStr(delta, "partial_json")}
	case "thinking_delta":
		return canon.Delta{Type: canon.DeltaThinking, Text: getStr(delta, "thinking")}
	case "signature_delta":
		return canon.Delta{Type: canon.DeltaSignature, Signature: getStr(delta, "signature")}
	}
	return canon.Delta{}
}

// ---- streaming: encode ----

func (MessagesCodec) EncodeStream(w io.Writer, events <-chan canon.Event) error {
	e := &anthropicStreamEncoder{w: w}
	for ev := range events {
		if err := e.handle(ev); err != nil {
			return err
		}
	}
	return nil
}

type anthropicStreamEncoder struct {
	w       io.Writer
	id      string
	model   string
	inUsage *canon.Usage
}

func (e *anthropicStreamEncoder) emit(typ string, data map[string]any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return sse.WriteEvent(e.w, typ, string(payload))
}

func (e *anthropicStreamEncoder) handle(ev canon.Event) error {
	switch x := ev.(type) {
	case canon.MessageStartEvent:
		if x.Response != nil {
			e.id = x.Response.ID
			e.model = x.Response.Model
			e.inUsage = &x.Response.Usage
		}
		inputTokens := 0
		if e.inUsage != nil {
			inputTokens = e.inUsage.InputTokens
		}
		return e.emit("message_start", map[string]any{
			"message": map[string]any{
				"id":            e.id,
				"type":          "message",
				"role":          "assistant",
				"model":         e.model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  inputTokens,
					"output_tokens": 0,
				},
			},
		})
	case canon.ContentBlockStartEvent:
		block := anthropicEncodeStreamBlock(x.Block)
		return e.emit("content_block_start", map[string]any{
			"index":         x.Index,
			"content_block": block,
		})
	case canon.ContentBlockDeltaEvent:
		delta := anthropicEncodeStreamDelta(x.Delta)
		if delta == nil {
			return nil
		}
		return e.emit("content_block_delta", map[string]any{
			"index": x.Index,
			"delta": delta,
		})
	case canon.ContentBlockStopEvent:
		return e.emit("content_block_stop", map[string]any{"index": x.Index})
	case canon.MessageDeltaEvent:
		stopReason, _ := canonToAnthropicStop(x.FinishReason)
		if stopReason == "" {
			stopReason = "end_turn"
		}
		delta := map[string]any{"stop_reason": stopReason}
		data := map[string]any{"delta": delta}
		if x.Usage != nil {
			data["usage"] = map[string]any{
				"input_tokens":  x.Usage.InputTokens,
				"output_tokens": x.Usage.OutputTokens,
			}
		}
		return e.emit("message_delta", data)
	case canon.MessageStopEvent:
		return e.emit("message_stop", map[string]any{})
	case canon.ErrorEvent:
		msg := "stream error"
		if x.Err != nil {
			msg = x.Err.Error()
		}
		return e.emit("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": msg},
		})
	}
	return nil
}

func anthropicEncodeStreamBlock(b canon.ContentBlock) map[string]any {
	switch blk := b.(type) {
	case *canon.ToolUseBlock:
		return map[string]any{"type": "tool_use", "id": blk.ID, "name": blk.Name, "input": map[string]any{}}
	case *canon.ThinkingBlock:
		if item, ok := redactedThinkingItem(blk.Extra); ok {
			return item
		}
		return map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	}
	return map[string]any{"type": "text", "text": ""}
}

func anthropicEncodeStreamDelta(d canon.Delta) map[string]any {
	switch d.Type {
	case canon.DeltaText:
		return map[string]any{"type": "text_delta", "text": d.Text}
	case canon.DeltaInputJSON:
		return map[string]any{"type": "input_json_delta", "partial_json": d.Partial}
	case canon.DeltaThinking:
		return map[string]any{"type": "thinking_delta", "thinking": d.Text}
	case canon.DeltaSignature:
		return map[string]any{"type": "signature_delta", "signature": d.Signature}
	}
	return nil
}

// ---- shared helpers ----

func getMap(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

func getStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]any, k string) int {
	if m == nil {
		return 0
	}
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

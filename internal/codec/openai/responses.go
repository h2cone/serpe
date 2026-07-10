package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/sse"
)

// ResponsesCodec translates between canonical and the OpenAI Responses API wire
// shape (POST /v1/responses). encode/decode are inverse on one wire schema.
type ResponsesCodec struct{}

// Name returns the protocol identifier.
func (ResponsesCodec) Name() string { return "openai-responses" }

// EncodeRequest encodes a canonical Request into a Responses wire body.
func (ResponsesCodec) EncodeRequest(req *canon.Request) ([]byte, error) {
	body := map[string]any{
		"model": req.Model,
		"store": false,
		"input": encodeInputItems(req.Conversation),
	}
	include, err := mergeResponsesInclude(req.Extra["openai.responses.include"])
	if err != nil {
		return nil, err
	}
	body["include"] = include
	if req.Conversation.System != "" {
		body["instructions"] = req.Conversation.System
	}
	if tools := encodeResponsesTools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if c, ok := encodeResponsesToolChoice(req.ToolChoice); ok {
		body["tool_choice"] = c
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		return nil, fmt.Errorf("openai-responses: stop sequences are not supported")
	}
	if req.Stream {
		body["stream"] = true
	}
	if pid, ok := req.Extra["previous_response_id"].(string); ok && pid != "" {
		body["previous_response_id"] = pid
	}
	return json.Marshal(body)
}

// DecodeRequest parses a Responses wire body into a canonical Request.
func (ResponsesCodec) DecodeRequest(data []byte) (*canon.Request, error) {
	var wire struct {
		Model            string            `json:"model"`
		Instructions     string            `json:"instructions"`
		Input            json.RawMessage   `json:"input"`
		Tools            []json.RawMessage `json:"tools"`
		ToolChoice       json.RawMessage   `json:"tool_choice"`
		Temperature      *float64          `json:"temperature"`
		TopP             *float64          `json:"top_p"`
		MaxOutputTokens  int               `json:"max_output_tokens"`
		Stop             []string          `json:"stop"`
		Stream           bool              `json:"stream"`
		PreviousResponse string            `json:"previous_response_id"`
		Include          json.RawMessage   `json:"include"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("openai-responses decode request: %w", err)
	}
	msgs, err := decodeInputItems(wire.Input)
	if err != nil {
		return nil, err
	}
	req := &canon.Request{
		Model: wire.Model,
		Conversation: canon.Conversation{
			System:   wire.Instructions,
			Messages: msgs,
		},
		Temperature: wire.Temperature,
		TopP:        wire.TopP,
		MaxTokens:   wire.MaxOutputTokens,
		Stop:        wire.Stop,
		Stream:      wire.Stream,
	}
	req.Tools, err = decodeResponsesTools(wire.Tools)
	if err != nil {
		return nil, err
	}
	if len(wire.ToolChoice) > 0 && string(wire.ToolChoice) != "null" {
		req.ToolChoice, err = decodeResponsesToolChoice(wire.ToolChoice)
		if err != nil {
			return nil, err
		}
	}
	if wire.PreviousResponse != "" {
		if req.Extra == nil {
			req.Extra = map[string]any{}
		}
		req.Extra["previous_response_id"] = wire.PreviousResponse
	}
	if len(wire.Include) > 0 && string(wire.Include) != "null" {
		if req.Extra == nil {
			req.Extra = map[string]any{}
		}
		req.Extra["openai.responses.include"] = json.RawMessage(wire.Include)
	}
	return req, nil
}

// DecodeResponse parses a Responses wire body into a canonical Response. A
// missing "status" is treated as "completed" for compatibility with fixtures.
func (ResponsesCodec) DecodeResponse(data []byte) (*canon.Response, error) {
	var wire struct {
		ID                string `json:"id"`
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("openai-responses decode response: %w", err)
	}
	if wire.Status == "failed" || wire.Error != nil {
		msg := "response failed"
		if wire.Error != nil && wire.Error.Message != "" {
			msg = wire.Error.Message
		}
		return nil, fmt.Errorf("openai-responses: %s", msg)
	}

	resp := &canon.Response{
		ID:       wire.ID,
		Model:    wire.Model,
		Provider: "openai-responses",
		Usage: canon.Usage{
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
			CacheRead:    wire.Usage.InputTokensDetails.CachedTokens,
		},
	}
	hasToolUse := false
	for _, raw := range wire.Output {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &probe)
		switch probe.Type {
		case "message":
			blocks, err := decodeResponsesMessageOutput(raw)
			if err != nil {
				return nil, err
			}
			resp.Content = append(resp.Content, blocks...)
		case "function_call":
			blk, err := decodeResponsesFunctionCall(raw)
			if err != nil {
				return nil, err
			}
			resp.Content = append(resp.Content, blk)
			hasToolUse = true
		case "reasoning":
			resp.Content = append(resp.Content, decodeResponsesReasoning(raw))
		}
	}
	resp.FinishReason = responsesFinishFromStatus(wire.Status, wire.IncompleteDetails.Reason, hasToolUse)
	return resp, nil
}

// EncodeResponse renders a canonical Response into a Responses wire body.
func (ResponsesCodec) EncodeResponse(r *canon.Response) ([]byte, error) {
	output := make([]any, 0, len(r.Content))
	for _, b := range r.Content {
		switch blk := b.(type) {
		case *canon.TextBlock:
			output = append(output, map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "output_text",
					"text": blk.Text,
				}},
			})
		case *canon.ToolUseBlock:
			output = append(output, map[string]any{
				"type":      "function_call",
				"call_id":   blk.ID,
				"name":      blk.Name,
				"arguments": rawString(blk.Input),
			})
		case *canon.ThinkingBlock:
			summary := []any{}
			if blk.Text != "" {
				summary = append(summary, map[string]any{"type": "summary_text", "text": blk.Text})
			}
			item := map[string]any{"type": "reasoning", "summary": summary}
			if v, ok := blk.Extra["openai.responses.reasoning_item"]; ok {
				if m, ok := v.(map[string]any); ok {
					for k, val := range m {
						if k == "type" {
							continue
						}
						item[k] = val
					}
				}
			}
			output = append(output, item)
		}
	}
	status, incomplete := responsesEncodeStatus(r.FinishReason)
	body := map[string]any{
		"id":     r.ID,
		"object": "response",
		"model":  r.Model,
		"output": output,
		"status": status,
		"usage": map[string]any{
			"input_tokens":  r.Usage.InputTokens,
			"output_tokens": r.Usage.OutputTokens,
			"total_tokens":  r.Usage.InputTokens + r.Usage.OutputTokens,
			"input_tokens_details": map[string]any{
				"cached_tokens": r.Usage.CacheRead,
			},
		},
	}
	if incomplete != nil {
		body["incomplete_details"] = incomplete
	}
	return json.Marshal(body)
}

// EncodeError renders an OpenAI-shaped error body.
func (ResponsesCodec) EncodeError(status int, err error) ([]byte, int) {
	return encodeError(status, err)
}

// ---- request helpers ----

func encodeInputItems(conv canon.Conversation) []any {
	items := make([]any, 0)
	for _, msg := range conv.Messages {
		switch msg.Role {
		case canon.RoleUser:
			parts := make([]any, 0)
			for _, b := range msg.Content {
				switch blk := b.(type) {
				case *canon.TextBlock:
					parts = append(parts, map[string]any{"type": "input_text", "text": blk.Text})
				case *canon.ImageBlock:
					parts = append(parts, encodeResponsesImage(blk))
				}
			}
			if len(parts) > 0 {
				items = append(items, map[string]any{
					"type":    "message",
					"role":    "user",
					"content": parts,
				})
			}
			for _, b := range msg.Content {
				if tr, ok := b.(*canon.ToolResultBlock); ok {
					items = append(items, encodeResponsesToolResult(tr))
				}
			}
		case canon.RoleAssistant:
			parts := make([]any, 0)
			for _, b := range msg.Content {
				if tb, ok := b.(*canon.TextBlock); ok {
					parts = append(parts, map[string]any{"type": "input_text", "text": tb.Text})
				}
			}
			if len(parts) > 0 {
				items = append(items, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": parts,
				})
			}
			for _, b := range msg.Content {
				switch blk := b.(type) {
				case *canon.ToolUseBlock:
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   blk.ID,
						"name":      blk.Name,
						"arguments": rawString(blk.Input),
					})
				case *canon.ThinkingBlock:
					items = append(items, encodeResponsesReasoningItem(blk))
				}
			}
		}
	}
	return items
}

func encodeResponsesImage(blk *canon.ImageBlock) map[string]any {
	part := map[string]any{"type": "input_image"}
	if blk.URL != "" {
		part["image_url"] = blk.URL
	} else if blk.Data != "" {
		part["image_url"] = dataURL(blk.MediaType, blk.Data)
	}
	return part
}

func encodeResponsesToolResult(tr *canon.ToolResultBlock) map[string]any {
	return map[string]any{
		"type":    "function_call_output",
		"call_id": tr.ToolUseID,
		"output":  encodeResponsesToolResultOutput(tr),
	}
}

// encodeResponsesToolResultOutput emits a string for a single text block, or a
// content array for multi-block / image results.
func encodeResponsesToolResultOutput(tr *canon.ToolResultBlock) any {
	if len(tr.Content) == 1 {
		if tb, ok := tr.Content[0].(*canon.TextBlock); ok {
			return tb.Text
		}
	}
	parts := make([]any, 0, len(tr.Content))
	for _, b := range tr.Content {
		switch blk := b.(type) {
		case *canon.TextBlock:
			parts = append(parts, map[string]any{"type": "input_text", "text": blk.Text})
		case *canon.ImageBlock:
			parts = append(parts, encodeResponsesImage(blk))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts
}

func encodeResponsesReasoningItem(blk *canon.ThinkingBlock) map[string]any {
	if v, ok := blk.Extra["openai.responses.reasoning_item"]; ok {
		if m, ok := v.(map[string]any); ok {
			out := map[string]any{"type": "reasoning"}
			for k, val := range m {
				out[k] = val
			}
			return out
		}
	}
	summary := []any{}
	if blk.Text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": blk.Text})
	}
	return map[string]any{"type": "reasoning", "summary": summary}
}

func encodeResponsesTools(tools []canon.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		item := map[string]any{
			"type":       "function",
			"name":       t.Name,
			"strict":     true,
			"parameters": rawMessageOrEmpty(t.Parameters),
		}
		if t.Description != "" {
			item["description"] = t.Description
		}
		for k, v := range t.Extra {
			item[k] = v
		}
		out = append(out, item)
	}
	return out
}

func decodeResponsesTools(raws []json.RawMessage) ([]canon.Tool, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]canon.Tool, 0, len(raws))
	for _, raw := range raws {
		var wire struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, fmt.Errorf("openai-responses decode tool: %w", err)
		}
		// strict is a codec default (true on encode); canonical does not model it.
		out = append(out, canon.Tool{
			Name:        wire.Name,
			Description: wire.Description,
			Parameters:  wire.Parameters,
		})
	}
	return out, nil
}

func encodeResponsesToolChoice(tc canon.ToolChoice) (any, bool) {
	switch tc.Mode {
	case canon.ToolChoiceAuto:
		return "auto", true
	case canon.ToolChoiceRequired:
		return "required", true
	case canon.ToolChoiceNone:
		return "none", true
	case canon.ToolChoiceSpecific:
		if tc.Name == "" {
			return nil, false
		}
		return map[string]any{"type": "function", "name": tc.Name}, true
	}
	return nil, false
}

func decodeResponsesToolChoice(raw json.RawMessage) (canon.ToolChoice, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return canon.ToolChoice{Mode: canon.ToolChoiceAuto}, nil
		case "required":
			return canon.ToolChoice{Mode: canon.ToolChoiceRequired}, nil
		case "none":
			return canon.ToolChoice{Mode: canon.ToolChoiceNone}, nil
		}
		return canon.ToolChoice{}, fmt.Errorf("openai-responses: unknown tool_choice %q", s)
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return canon.ToolChoice{}, fmt.Errorf("openai-responses decode tool_choice: %w", err)
	}
	if obj.Type == "function" && obj.Name != "" {
		return canon.ToolChoice{Mode: canon.ToolChoiceSpecific, Name: obj.Name}, nil
	}
	return canon.ToolChoice{}, fmt.Errorf("openai-responses: unsupported tool_choice object")
}

// decodeInputItems converts a Responses "input" (string shorthand or item array)
// into canonical messages, merging function_call / function_call_output items
// back into their owning assistant / user messages.
func decodeInputItems(raw json.RawMessage) ([]canon.Message, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// String shorthand: a single user text message.
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return []canon.Message{{
			Role:    canon.RoleUser,
			Content: []canon.ContentBlock{&canon.TextBlock{Text: asStr}},
		}}, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openai-responses decode input: %w", err)
	}

	var msgs []canon.Message
	var cur *canon.Message
	flush := func() {
		if cur != nil {
			msgs = append(msgs, *cur)
			cur = nil
		}
	}
	onlyToolResults := func(m *canon.Message) bool {
		if m == nil || len(m.Content) == 0 {
			return false
		}
		for _, b := range m.Content {
			if _, ok := b.(*canon.ToolResultBlock); !ok {
				return false
			}
		}
		return true
	}

	for _, itemRaw := range items {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(itemRaw, &probe)
		switch probe.Type {
		case "message":
			var item struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				return nil, fmt.Errorf("openai-responses decode message item: %w", err)
			}
			flush()
			cur = &canon.Message{Role: canon.Role(item.Role)}
			blocks, err := decodeResponsesContentParts(item.Content)
			if err != nil {
				return nil, err
			}
			cur.Content = blocks
		case "function_call":
			var item struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				return nil, fmt.Errorf("openai-responses decode function_call: %w", err)
			}
			if cur == nil || cur.Role != canon.RoleAssistant {
				flush()
				cur = &canon.Message{Role: canon.RoleAssistant}
			}
			cur.Content = append(cur.Content, &canon.ToolUseBlock{
				ID:    item.CallID,
				Name:  item.Name,
				Input: json.RawMessage(argBytes(item.Arguments)),
			})
		case "function_call_output":
			var item struct {
				CallID string          `json:"call_id"`
				Output json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				return nil, fmt.Errorf("openai-responses decode function_call_output: %w", err)
			}
			if !onlyToolResults(cur) {
				flush()
				cur = &canon.Message{Role: canon.RoleUser}
			}
			cur.Content = append(cur.Content, &canon.ToolResultBlock{
				ToolUseID: item.CallID,
				Content:   decodeResponsesOutput(item.Output),
			})
		case "reasoning":
			blk := decodeResponsesReasoning(itemRaw)
			if cur == nil || cur.Role != canon.RoleAssistant {
				flush()
				cur = &canon.Message{Role: canon.RoleAssistant}
			}
			cur.Content = append(cur.Content, blk)
		}
	}
	flush()
	return msgs, nil
}

// decodeResponsesContentParts parses a message item's content (string shorthand
// or part array) into blocks. input_text/output_text become TextBlock.
func decodeResponsesContentParts(raw json.RawMessage) ([]canon.ContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return []canon.ContentBlock{&canon.TextBlock{Text: asStr}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("openai-responses decode content: %w", err)
	}
	out := make([]canon.ContentBlock, 0, len(parts))
	for _, partRaw := range parts {
		var probe struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Refusal  string `json:"refusal"`
			ImageURL string `json:"image_url"`
		}
		_ = json.Unmarshal(partRaw, &probe)
		switch probe.Type {
		case "input_text", "output_text":
			out = append(out, &canon.TextBlock{Text: probe.Text})
		case "refusal":
			out = append(out, &canon.TextBlock{Text: probe.Refusal})
		case "input_image":
			out = append(out, decodeResponsesImagePart(probe.ImageURL))
		}
	}
	return out, nil
}

func decodeResponsesImagePart(imageURL string) *canon.ImageBlock {
	if mt, data, ok := parseDataURL(imageURL); ok {
		return &canon.ImageBlock{MediaType: mt, Data: data}
	}
	return &canon.ImageBlock{URL: imageURL}
}

// decodeResponsesOutput parses a function_call_output "output" (string or content
// array) into blocks.
func decodeResponsesOutput(raw json.RawMessage) []canon.ContentBlock {
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return []canon.ContentBlock{&canon.TextBlock{Text: asStr}}
	}
	blocks, err := decodeResponsesContentParts(raw)
	if err != nil || len(blocks) == 0 {
		return []canon.ContentBlock{&canon.TextBlock{Text: ""}}
	}
	return blocks
}

func decodeResponsesMessageOutput(raw json.RawMessage) ([]canon.ContentBlock, error) {
	var item struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("openai-responses decode message output: %w", err)
	}
	return decodeResponsesContentParts(item.Content)
}

func decodeResponsesFunctionCall(raw json.RawMessage) (*canon.ToolUseBlock, error) {
	var item struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("openai-responses decode function_call: %w", err)
	}
	return &canon.ToolUseBlock{
		ID:    item.CallID,
		Name:  item.Name,
		Input: json.RawMessage(argBytes(item.Arguments)),
	}, nil
}

func decodeResponsesReasoning(raw json.RawMessage) *canon.ThinkingBlock {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return &canon.ThinkingBlock{}
	}
	return decodeResponsesReasoningItem(item)
}

func decodeResponsesReasoningItem(item map[string]any) *canon.ThinkingBlock {
	blk := &canon.ThinkingBlock{}
	if summaries, ok := item["summary"].([]any); ok {
		var texts []string
		for _, s := range summaries {
			if m, ok := s.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
		blk.Text = joinTexts(texts)
	}
	blk.Extra = map[string]any{"openai.responses.reasoning_item": item}
	return blk
}

// ---- finish reason helpers ----

func responsesFinishFromStatus(status, incompleteReason string, hasToolUse bool) canon.FinishReason {
	switch status {
	case "incomplete":
		switch incompleteReason {
		case "max_output_tokens":
			return canon.FinishLength
		case "content_filter":
			return canon.FinishContentFilter
		}
		return canon.FinishLength
	case "failed":
		return canon.FinishStop
	case "", "completed":
		if hasToolUse {
			return canon.FinishToolCalls
		}
		return canon.FinishStop
	}
	return canon.FinishStop
}

func responsesEncodeStatus(fr canon.FinishReason) (string, any) {
	switch fr {
	case canon.FinishLength:
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case canon.FinishContentFilter:
		return "incomplete", map[string]any{"reason": "content_filter"}
	}
	return "completed", nil
}

// ---- streaming: decode ----

func (ResponsesCodec) DecodeStream(r io.Reader) (<-chan canon.Event, error) {
	ch := make(chan canon.Event, 16)
	go func() {
		defer close(ch)
		d := &responsesStreamDecoder{
			textBlocks:  map[string]int{},
			itemBlocks:  map[int]int{},
			itemTypes:   map[int]string{},
			reasoning:   map[int]*strings.Builder{},
			reasonItems: map[int]map[string]any{},
			closed:      map[int]bool{},
		}
		sr := sse.NewReader(r)
		for {
			ev, err := sr.Next()
			if err != nil {
				if err != io.EOF {
					ch <- canon.ErrorEvent{Err: fmt.Errorf("openai-responses stream: %w", err)}
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
			d.handle(ev.Type, data, ch)
		}
	}()
	return ch, nil
}

type responsesStreamDecoder struct {
	textBlocks  map[string]int // "outIdx:contentIdx" -> canonical index
	itemBlocks  map[int]int    // output_index -> canonical index (function_call/reasoning)
	itemTypes   map[int]string
	reasoning   map[int]*strings.Builder
	reasonItems map[int]map[string]any
	closed      map[int]bool
	next        int
	hasToolUse  bool
}

func (d *responsesStreamDecoder) newIndex() int {
	idx := d.next
	d.next++
	return idx
}

func (d *responsesStreamDecoder) handle(typ string, data map[string]any, ch chan canon.Event) {
	switch typ {
	case "response.created", "response.in_progress":
		resp := getMap(data, "response")
		ch <- canon.MessageStartEvent{Response: &canon.Response{
			ID:    getStr(resp, "id"),
			Model: getStr(resp, "model"),
		}}
	case "response.output_item.added":
		item := getMap(data, "item")
		outIdx := getInt(data, "output_index")
		itemType := getStr(item, "type")
		d.itemTypes[outIdx] = itemType
		switch itemType {
		case "function_call":
			idx := d.newIndex()
			d.itemBlocks[outIdx] = idx
			d.hasToolUse = true
			ch <- canon.ContentBlockStartEvent{Index: idx, Block: &canon.ToolUseBlock{
				ID:   getStr(item, "call_id"),
				Name: getStr(item, "name"),
			}}
		case "reasoning":
			idx := d.newIndex()
			d.itemBlocks[outIdx] = idx
			d.reasoning[outIdx] = &strings.Builder{}
			d.reasonItems[outIdx] = cloneAnyMap(item)
		}
	case "response.content_part.added":
		outIdx := getInt(data, "output_index")
		contentIdx := getInt(data, "content_index")
		partType := getStr(getMap(data, "part"), "type")
		if partType != "output_text" && partType != "refusal" {
			return
		}
		idx := d.newIndex()
		d.textBlocks[textKey(outIdx, contentIdx)] = idx
		ch <- canon.ContentBlockStartEvent{Index: idx, Block: &canon.TextBlock{}}
	case "response.output_text.delta", "response.refusal.delta":
		idx, ok := d.textBlocks[textKey(getInt(data, "output_index"), getInt(data, "content_index"))]
		if ok {
			ch <- canon.ContentBlockDeltaEvent{Index: idx, Delta: canon.Delta{Type: canon.DeltaText, Text: getStr(data, "delta")}}
		}
	case "response.function_call_arguments.delta":
		if idx, ok := d.itemBlocks[getInt(data, "output_index")]; ok {
			ch <- canon.ContentBlockDeltaEvent{Index: idx, Delta: canon.Delta{Type: canon.DeltaInputJSON, Partial: getStr(data, "delta")}}
		}
	case "response.reasoning_summary_text.delta":
		outIdx := getInt(data, "output_index")
		if buf := d.reasoning[outIdx]; buf != nil {
			buf.WriteString(getStr(data, "delta"))
		}
	case "response.output_text.done", "response.refusal.done", "response.content_part.done":
		d.closeText(getInt(data, "output_index"), getInt(data, "content_index"), ch)
	case "response.function_call_arguments.done":
		d.closeItem(getInt(data, "output_index"), ch)
	case "response.output_item.done":
		outIdx := getInt(data, "output_index")
		if d.itemTypes[outIdx] == "reasoning" {
			d.finishReasoning(outIdx, getMap(data, "item"), ch)
		} else {
			d.closeItem(outIdx, ch)
		}
		d.closeTextsForOutput(outIdx, ch)
	case "response.completed":
		resp := getMap(data, "response")
		ch <- canon.MessageDeltaEvent{
			FinishReason: responsesFinishFromStatus(getStr(resp, "status"), getStr(getMap(resp, "incomplete_details"), "reason"), d.hasToolUse),
			Usage:        responsesUsage(resp),
		}
		ch <- canon.MessageStopEvent{}
	case "response.incomplete":
		resp := getMap(data, "response")
		ch <- canon.MessageDeltaEvent{
			FinishReason: responsesFinishFromStatus("incomplete", getStr(getMap(resp, "incomplete_details"), "reason"), d.hasToolUse),
			Usage:        responsesUsage(resp),
		}
		ch <- canon.MessageStopEvent{}
	case "response.failed":
		ch <- canon.ErrorEvent{Err: responsesStreamError(getMap(getMap(data, "response"), "error"))}
	case "response.error":
		ch <- canon.ErrorEvent{Err: responsesStreamError(getMap(data, "error"))}
	}
}

func responsesStreamError(errObj map[string]any) error {
	message := getStr(errObj, "message")
	if message == "" {
		message = "unknown upstream error"
	}
	return fmt.Errorf("openai-responses stream error: %s", message)
}

func (d *responsesStreamDecoder) finishReasoning(outIdx int, completed map[string]any, ch chan canon.Event) {
	idx, ok := d.itemBlocks[outIdx]
	if !ok || d.closed[idx] {
		return
	}
	item := d.reasonItems[outIdx]
	if item == nil {
		item = map[string]any{"type": "reasoning"}
	}
	for k, v := range completed {
		item[k] = v
	}
	blk := decodeResponsesReasoningItem(item)
	if blk.Text == "" {
		if buf := d.reasoning[outIdx]; buf != nil {
			blk.Text = buf.String()
		}
	}
	ch <- canon.ContentBlockStartEvent{Index: idx, Block: blk}
	d.closed[idx] = true
	ch <- canon.ContentBlockStopEvent{Index: idx}
	delete(d.reasoning, outIdx)
	delete(d.reasonItems, outIdx)
}

func (d *responsesStreamDecoder) closeText(outIdx, contentIdx int, ch chan canon.Event) {
	idx, ok := d.textBlocks[textKey(outIdx, contentIdx)]
	if !ok || d.closed[idx] {
		return
	}
	d.closed[idx] = true
	ch <- canon.ContentBlockStopEvent{Index: idx}
}

func (d *responsesStreamDecoder) closeItem(outIdx int, ch chan canon.Event) {
	idx, ok := d.itemBlocks[outIdx]
	if !ok || d.closed[idx] {
		return
	}
	d.closed[idx] = true
	ch <- canon.ContentBlockStopEvent{Index: idx}
}

func (d *responsesStreamDecoder) closeTextsForOutput(outIdx int, ch chan canon.Event) {
	for key, idx := range d.textBlocks {
		out, _ := splitTextKey(key)
		if out == outIdx && !d.closed[idx] {
			d.closed[idx] = true
			ch <- canon.ContentBlockStopEvent{Index: idx}
		}
	}
}

func responsesUsage(resp map[string]any) *canon.Usage {
	u := getMap(resp, "usage")
	if u == nil {
		return nil
	}
	return &canon.Usage{
		InputTokens:  getInt(u, "input_tokens"),
		OutputTokens: getInt(u, "output_tokens"),
		CacheRead:    getInt(getMap(u, "input_tokens_details"), "cached_tokens"),
	}
}

func textKey(outIdx, contentIdx int) string {
	return strconv.Itoa(outIdx) + ":" + strconv.Itoa(contentIdx)
}

func splitTextKey(key string) (int, int) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			out, _ := strconv.Atoi(key[:i])
			content, _ := strconv.Atoi(key[i+1:])
			return out, content
		}
	}
	return 0, 0
}

// ---- streaming: encode ----

func (ResponsesCodec) EncodeStream(w io.Writer, events <-chan canon.Event) error {
	e := &responsesStreamEncoder{
		w:         w,
		outIdx:    map[int]int{},
		blockType: map[int]string{},
		nextOut:   0,
	}
	for ev := range events {
		if err := e.handle(ev); err != nil {
			return err
		}
	}
	return e.finish()
}

type responsesStreamEncoder struct {
	w            io.Writer
	outIdx       map[int]int // canonical index -> wire output_index
	blockType    map[int]string
	nextOut      int
	id           string
	model        string
	finishReason canon.FinishReason
	usage        *canon.Usage
	terminal     bool
}

func (e *responsesStreamEncoder) emit(typ string, data map[string]any) error {
	data["type"] = typ
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return sse.WriteEvent(e.w, typ, string(payload))
}

func (e *responsesStreamEncoder) handle(ev canon.Event) error {
	switch x := ev.(type) {
	case canon.MessageStartEvent:
		if x.Response != nil {
			e.id = x.Response.ID
			e.model = x.Response.Model
		}
		return e.emit("response.created", map[string]any{
			"response": map[string]any{
				"id":     e.id,
				"object": "response",
				"model":  e.model,
				"status": "in_progress",
			},
		})
	case canon.ContentBlockStartEvent:
		outIdx := e.nextOut
		e.nextOut++
		e.outIdx[x.Index] = outIdx
		switch b := x.Block.(type) {
		case *canon.TextBlock:
			e.blockType[x.Index] = "text"
			if err := e.emit("response.output_item.added", map[string]any{
				"output_index": outIdx,
				"item":         map[string]any{"type": "message", "role": "assistant", "content": []any{}},
			}); err != nil {
				return err
			}
			return e.emit("response.content_part.added", map[string]any{
				"output_index":  outIdx,
				"content_index": 0,
				"part":          map[string]any{"type": "output_text", "text": b.Text},
			})
		case *canon.ToolUseBlock:
			e.blockType[x.Index] = "tool_use"
			return e.emit("response.output_item.added", map[string]any{
				"output_index": outIdx,
				"item": map[string]any{
					"type":      "function_call",
					"call_id":   b.ID,
					"name":      b.Name,
					"arguments": "",
				},
			})
		case *canon.ThinkingBlock:
			e.blockType[x.Index] = "thinking"
			return e.emit("response.output_item.added", map[string]any{
				"output_index": outIdx,
				"item":         map[string]any{"type": "reasoning", "summary": []any{}},
			})
		}
	case canon.ContentBlockDeltaEvent:
		outIdx := e.outIdx[x.Index]
		switch x.Delta.Type {
		case canon.DeltaText:
			return e.emit("response.output_text.delta", map[string]any{
				"output_index":  outIdx,
				"content_index": 0,
				"delta":         x.Delta.Text,
			})
		case canon.DeltaInputJSON:
			return e.emit("response.function_call_arguments.delta", map[string]any{
				"output_index": outIdx,
				"delta":        x.Delta.Partial,
			})
		case canon.DeltaThinking:
			return e.emit("response.reasoning_summary_text.delta", map[string]any{
				"output_index": outIdx,
				"delta":        x.Delta.Text,
			})
		}
	case canon.ContentBlockStopEvent:
		outIdx := e.outIdx[x.Index]
		switch e.blockType[x.Index] {
		case "text":
			if err := e.emit("response.output_text.done", map[string]any{"output_index": outIdx, "content_index": 0, "text": ""}); err != nil {
				return err
			}
			if err := e.emit("response.content_part.done", map[string]any{"output_index": outIdx, "content_index": 0}); err != nil {
				return err
			}
			return e.emit("response.output_item.done", map[string]any{"output_index": outIdx, "item": map[string]any{"type": "message"}})
		case "tool_use":
			if err := e.emit("response.function_call_arguments.done", map[string]any{"output_index": outIdx, "arguments": ""}); err != nil {
				return err
			}
			return e.emit("response.output_item.done", map[string]any{"output_index": outIdx, "item": map[string]any{"type": "function_call"}})
		case "thinking":
			return e.emit("response.output_item.done", map[string]any{"output_index": outIdx, "item": map[string]any{"type": "reasoning"}})
		}
	case canon.MessageDeltaEvent:
		e.finishReason = x.FinishReason
		if x.Usage != nil {
			e.usage = x.Usage
		}
	case canon.MessageStopEvent:
		return e.emitTerminal()
	case canon.ErrorEvent:
		e.terminal = true
		msg := "stream error"
		if x.Err != nil {
			msg = x.Err.Error()
		}
		return e.emit("response.failed", map[string]any{
			"response": map[string]any{
				"status": "failed",
				"error":  map[string]any{"message": msg, "code": "stream_error"},
			},
		})
	}
	return nil
}

func (e *responsesStreamEncoder) emitTerminal() error {
	if e.terminal {
		return nil
	}
	e.terminal = true
	status, incomplete := responsesEncodeStatus(e.finishReason)
	resp := map[string]any{
		"id":     e.id,
		"object": "response",
		"model":  e.model,
		"status": status,
	}
	if incomplete != nil {
		resp["incomplete_details"] = incomplete
	}
	if e.usage != nil {
		resp["usage"] = map[string]any{
			"input_tokens":  e.usage.InputTokens,
			"output_tokens": e.usage.OutputTokens,
		}
	}
	eventType := "response.completed"
	if status == "incomplete" {
		eventType = "response.incomplete"
	}
	return e.emit(eventType, map[string]any{"response": resp})
}

func (e *responsesStreamEncoder) finish() error {
	if !e.terminal {
		return e.emitTerminal()
	}
	return nil
}

// ---- shared openai helpers ----

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func argBytes(s string) []byte {
	if s == "" {
		return []byte("{}")
	}
	return []byte(s)
}

func rawMessageOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func joinTexts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

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

func mergeResponsesInclude(value any) ([]string, error) {
	include := make([]string, 0, 1)
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("openai-responses encode include: %w", err)
		}
		if err := json.Unmarshal(data, &include); err != nil {
			return nil, fmt.Errorf("openai-responses encode include: expected an array of strings: %w", err)
		}
	}

	const encryptedReasoning = "reasoning.encrypted_content"
	for _, item := range include {
		if item == encryptedReasoning {
			return include, nil
		}
	}
	return append(include, encryptedReasoning), nil
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

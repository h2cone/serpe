package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/sse"
)

// ChatCodec translates between canonical and the OpenAI Chat Completions wire
// shape (POST /v1/chat/completions) -- the de-facto "OpenAI compatible" surface.
type ChatCodec struct{}

// Name returns the protocol identifier.
func (ChatCodec) Name() string { return "openai-chat" }

// EncodeRequest encodes a canonical Request into a Chat Completions wire body.
func (ChatCodec) EncodeRequest(req *canon.Request) ([]byte, error) {
	messages := make([]any, 0)
	if req.Conversation.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.Conversation.System})
	}
	for _, msg := range req.Conversation.Messages {
		messages = append(messages, encodeChatMessage(msg)...)
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": messages,
	}
	if tools := encodeChatTools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if c, ok := encodeChatToolChoice(req.ToolChoice); ok {
		body["tool_choice"] = c
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		body["max_completion_tokens"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if req.Stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(body)
}

// DecodeRequest parses a Chat Completions wire body into a canonical Request.
func (ChatCodec) DecodeRequest(data []byte) (*canon.Request, error) {
	var wire struct {
		Model           string            `json:"model"`
		Messages        []json.RawMessage `json:"messages"`
		Tools           []json.RawMessage `json:"tools"`
		ToolChoice      json.RawMessage   `json:"tool_choice"`
		Temperature     *float64          `json:"temperature"`
		TopP            *float64          `json:"top_p"`
		MaxTokens       int               `json:"max_completion_tokens"`
		LegacyMaxTokens int               `json:"max_tokens"`
		Stop            []string          `json:"stop"`
		Stream          bool              `json:"stream"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("openai-chat decode request: %w", err)
	}
	maxTokens := wire.MaxTokens
	if maxTokens == 0 {
		maxTokens = wire.LegacyMaxTokens
	}
	var systemParts []string
	var msgs []canon.Message
	var cur *canon.Message
	flush := func() {
		if cur != nil {
			msgs = append(msgs, *cur)
			cur = nil
		}
	}
	for _, raw := range wire.Messages {
		var probe struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(raw, &probe)
		switch probe.Role {
		case "system":
			var m struct {
				Content json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal(raw, &m)
			systemParts = append(systemParts, contentToString(m.Content))
		case "user":
			var m struct {
				Content json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal(raw, &m)
			flush()
			cur = &canon.Message{Role: canon.RoleUser}
			cur.Content = decodeChatContent(m.Content)
		case "assistant":
			var m struct {
				Content   json.RawMessage   `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			}
			_ = json.Unmarshal(raw, &m)
			flush()
			cur = &canon.Message{Role: canon.RoleAssistant}
			cur.Content = decodeChatContent(m.Content)
			for _, tcRaw := range m.ToolCalls {
				tu, err := decodeChatToolCall(tcRaw)
				if err != nil {
					return nil, err
				}
				cur.Content = append(cur.Content, tu)
			}
		case "tool":
			var m struct {
				ToolCallID string          `json:"tool_call_id"`
				Content    json.RawMessage `json:"content"`
			}
			_ = json.Unmarshal(raw, &m)
			if cur == nil || cur.Role != canon.RoleUser || !onlyToolResults(cur) {
				flush()
				cur = &canon.Message{Role: canon.RoleUser}
			}
			cur.Content = append(cur.Content, &canon.ToolResultBlock{
				ToolUseID: m.ToolCallID,
				Content:   []canon.ContentBlock{&canon.TextBlock{Text: contentToString(m.Content)}},
			})
		}
	}
	flush()

	req := &canon.Request{
		Model: wire.Model,
		Conversation: canon.Conversation{
			System:   strings.Join(systemParts, "\n"),
			Messages: msgs,
		},
		Temperature: wire.Temperature,
		TopP:        wire.TopP,
		MaxTokens:   maxTokens,
		Stop:        wire.Stop,
		Stream:      wire.Stream,
	}
	var err error
	req.Tools, err = decodeChatTools(wire.Tools)
	if err != nil {
		return nil, err
	}
	if len(wire.ToolChoice) > 0 && string(wire.ToolChoice) != "null" {
		req.ToolChoice, err = decodeChatToolChoice(wire.ToolChoice)
		if err != nil {
			return nil, err
		}
	}
	return req, nil
}

// DecodeResponse parses a Chat Completions wire body into a canonical Response.
func (ChatCodec) DecodeResponse(data []byte) (*canon.Response, error) {
	var wire struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Content          json.RawMessage   `json:"content"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
				ReasoningContent string            `json:"reasoning_content"`
				Refusal          string            `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("openai-chat decode response: %w", err)
	}
	if wire.Error != nil {
		return nil, fmt.Errorf("openai-chat: %s", wire.Error.Message)
	}
	resp := &canon.Response{ID: wire.ID, Model: wire.Model, Provider: "openai-chat"}
	if len(wire.Choices) > 0 {
		ch := wire.Choices[0]
		resp.Content = decodeChatContent(ch.Message.Content)
		if ch.Message.Refusal != "" {
			resp.Content = append(resp.Content, &canon.TextBlock{Text: ch.Message.Refusal})
		}
		for _, tcRaw := range ch.Message.ToolCalls {
			tu, err := decodeChatToolCall(tcRaw)
			if err != nil {
				return nil, err
			}
			resp.Content = append(resp.Content, tu)
		}
		if ch.Message.ReasoningContent != "" {
			resp.Content = append(resp.Content, &canon.ThinkingBlock{Text: ch.Message.ReasoningContent})
		}
		resp.FinishReason = chatFinishToCanon(ch.FinishReason)
	}
	resp.Usage = canon.Usage{
		InputTokens:  wire.Usage.PromptTokens,
		OutputTokens: wire.Usage.CompletionTokens,
		CacheRead:    wire.Usage.PromptTokensDetails.CachedTokens,
	}
	return resp, nil
}

// EncodeResponse renders a canonical Response into a Chat Completions wire body.
func (ChatCodec) EncodeResponse(r *canon.Response) ([]byte, error) {
	var textChunks []string
	var toolCalls []any
	var reasoning string
	for _, b := range r.Content {
		switch blk := b.(type) {
		case *canon.TextBlock:
			textChunks = append(textChunks, blk.Text)
		case *canon.ToolUseBlock:
			toolCalls = append(toolCalls, map[string]any{
				"id":   blk.ID,
				"type": "function",
				"function": map[string]any{
					"name":      blk.Name,
					"arguments": rawString(blk.Input),
				},
			})
		case *canon.ThinkingBlock:
			reasoning += blk.Text
		}
	}
	message := map[string]any{"role": "assistant"}
	if len(textChunks) > 0 {
		message["content"] = strings.Join(textChunks, "")
	} else {
		message["content"] = nil
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	body := map[string]any{
		"id":     r.ID,
		"object": "chat.completion",
		"model":  r.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": canonToChatFinish(r.FinishReason),
		}},
		"usage": map[string]any{
			"prompt_tokens":     r.Usage.InputTokens,
			"completion_tokens": r.Usage.OutputTokens,
			"total_tokens":      r.Usage.InputTokens + r.Usage.OutputTokens,
			"prompt_tokens_details": map[string]any{
				"cached_tokens": r.Usage.CacheRead,
			},
		},
	}
	return json.Marshal(body)
}

// EncodeError renders an OpenAI-shaped error body.
func (ChatCodec) EncodeError(status int, err error) ([]byte, int) {
	return encodeError(status, err)
}

// ---- request helpers ----

func encodeChatMessage(msg canon.Message) []any {
	var out []any
	switch msg.Role {
	case canon.RoleUser:
		var texts []string
		parts := make([]any, 0)
		hasImage := false
		for _, b := range msg.Content {
			switch blk := b.(type) {
			case *canon.TextBlock:
				texts = append(texts, blk.Text)
				parts = append(parts, map[string]any{"type": "text", "text": blk.Text})
			case *canon.ImageBlock:
				hasImage = true
				parts = append(parts, encodeChatImage(blk))
			}
		}
		if len(parts) > 0 {
			if !hasImage && len(texts) == 1 {
				out = append(out, map[string]any{"role": "user", "content": texts[0]})
			} else {
				out = append(out, map[string]any{"role": "user", "content": parts})
			}
		}
		// Each ToolResultBlock becomes its own {role:"tool"} message, ordered
		// right after the assistant turn that issued the calls.
		for _, b := range msg.Content {
			if tr, ok := b.(*canon.ToolResultBlock); ok {
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": tr.ToolUseID,
					"content":      chatToolResultContent(tr),
				})
			}
		}
	case canon.RoleAssistant:
		var textChunks []string
		var toolCalls []any
		for _, b := range msg.Content {
			switch blk := b.(type) {
			case *canon.TextBlock:
				textChunks = append(textChunks, blk.Text)
			case *canon.ToolUseBlock:
				toolCalls = append(toolCalls, map[string]any{
					"id":   blk.ID,
					"type": "function",
					"function": map[string]any{
						"name":      blk.Name,
						"arguments": rawString(blk.Input),
					},
				})
			}
		}
		m := map[string]any{"role": "assistant"}
		if len(textChunks) > 0 {
			m["content"] = strings.Join(textChunks, "")
		} else {
			m["content"] = nil
		}
		if len(toolCalls) > 0 {
			m["tool_calls"] = toolCalls
		}
		out = append(out, m)
	}
	return out
}

// chatToolResultContent degrades a ToolResultBlock to the string content Chat's
// role:"tool" message requires. Non-text blocks become a summary marker.
func chatToolResultContent(tr *canon.ToolResultBlock) string {
	var chunks []string
	for _, b := range tr.Content {
		switch blk := b.(type) {
		case *canon.TextBlock:
			chunks = append(chunks, blk.Text)
		case *canon.ImageBlock:
			chunks = append(chunks, fmt.Sprintf("[image %s]", blk.MediaType))
		}
	}
	return strings.Join(chunks, "\n")
}

func encodeChatImage(blk *canon.ImageBlock) map[string]any {
	url := blk.URL
	if url == "" && blk.Data != "" {
		url = dataURL(blk.MediaType, blk.Data)
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func decodeChatContent(raw json.RawMessage) []canon.ContentBlock {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		if asStr == "" {
			return nil
		}
		return []canon.ContentBlock{&canon.TextBlock{Text: asStr}}
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	out := make([]canon.ContentBlock, 0, len(parts))
	for _, partRaw := range parts {
		var probe struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			ImageURL json.RawMessage `json:"image_url"`
		}
		_ = json.Unmarshal(partRaw, &probe)
		switch probe.Type {
		case "text":
			out = append(out, &canon.TextBlock{Text: probe.Text})
		case "image_url":
			out = append(out, decodeChatImageURL(probe.ImageURL))
		}
	}
	return out
}

func decodeChatImageURL(raw json.RawMessage) *canon.ImageBlock {
	url := ""
	if len(raw) > 0 {
		var obj struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(raw, &obj)
		url = obj.URL
	}
	if mt, data, ok := parseDataURL(url); ok {
		return &canon.ImageBlock{MediaType: mt, Data: data}
	}
	return &canon.ImageBlock{URL: url}
}

func decodeChatToolCall(raw json.RawMessage) (*canon.ToolUseBlock, error) {
	var tc struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, fmt.Errorf("openai-chat decode tool_call: %w", err)
	}
	return &canon.ToolUseBlock{
		ID:    tc.ID,
		Name:  tc.Function.Name,
		Input: json.RawMessage(argBytes(tc.Function.Arguments)),
	}, nil
}

func contentToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array of parts: join text.
	blocks := decodeChatContent(raw)
	var chunks []string
	for _, b := range blocks {
		if tb, ok := b.(*canon.TextBlock); ok {
			chunks = append(chunks, tb.Text)
		}
	}
	return strings.Join(chunks, "")
}

func encodeChatTools(tools []canon.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{
			"name":       t.Name,
			"strict":     true,
			"parameters": rawMessageOrEmpty(t.Parameters),
		}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		for k, v := range t.Extra {
			fn[k] = v
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func decodeChatTools(raws []json.RawMessage) ([]canon.Tool, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]canon.Tool, 0, len(raws))
	for _, raw := range raws {
		var wire struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, fmt.Errorf("openai-chat decode tool: %w", err)
		}
		// strict is a codec default (true on encode); canonical does not model it.
		out = append(out, canon.Tool{
			Name:        wire.Function.Name,
			Description: wire.Function.Description,
			Parameters:  wire.Function.Parameters,
		})
	}
	return out, nil
}

func encodeChatToolChoice(tc canon.ToolChoice) (any, bool) {
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
		return map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}, true
	}
	return nil, false
}

func decodeChatToolChoice(raw json.RawMessage) (canon.ToolChoice, error) {
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
		return canon.ToolChoice{}, fmt.Errorf("openai-chat: unknown tool_choice %q", s)
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return canon.ToolChoice{}, fmt.Errorf("openai-chat decode tool_choice: %w", err)
	}
	if obj.Type == "function" && obj.Function.Name != "" {
		return canon.ToolChoice{Mode: canon.ToolChoiceSpecific, Name: obj.Function.Name}, nil
	}
	return canon.ToolChoice{}, fmt.Errorf("openai-chat: unsupported tool_choice object")
}

func chatFinishToCanon(s string) canon.FinishReason {
	switch s {
	case "stop":
		return canon.FinishStop
	case "length":
		return canon.FinishLength
	case "tool_calls", "function_call":
		return canon.FinishToolCalls
	case "content_filter":
		return canon.FinishContentFilter
	}
	return canon.FinishStop
}

func canonToChatFinish(fr canon.FinishReason) string {
	switch fr {
	case canon.FinishLength:
		return "length"
	case canon.FinishToolCalls:
		return "tool_calls"
	case canon.FinishContentFilter:
		return "content_filter"
	}
	return "stop"
}

func onlyToolResults(m *canon.Message) bool {
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

// ---- streaming: decode (delta state machine) ----

func (ChatCodec) DecodeStream(r io.Reader) (<-chan canon.Event, error) {
	ch := make(chan canon.Event, 16)
	go func() {
		defer close(ch)
		d := &chatStreamDecoder{
			toolBlocks: map[int]int{},
			openTools:  []int{},
		}
		sr := sse.NewReader(r)
		for {
			ev, err := sr.Next()
			if err != nil {
				if err != io.EOF {
					ch <- canon.ErrorEvent{Err: fmt.Errorf("openai-chat stream: %w", err)}
				}
				return
			}
			if ev.Data == "" {
				continue
			}
			if strings.TrimSpace(ev.Data) == "[DONE]" {
				ch <- canon.MessageStopEvent{}
				return
			}
			var data map[string]any
			if json.Unmarshal([]byte(ev.Data), &data) != nil {
				continue
			}
			if err := d.handle(data, ch); err != nil {
				ch <- canon.ErrorEvent{Err: err}
				return
			}
		}
	}()
	return ch, nil
}

type chatStreamDecoder struct {
	id, model      string
	started        bool
	next           int
	activeText     int         // -1 if none
	activeThinking int         // -1 if none
	toolBlocks     map[int]int // chat tool_calls index -> canonical index
	openTools      []int       // canonical indices of open tool blocks, in start order
}

func (d *chatStreamDecoder) newIndex() int {
	idx := d.next
	d.next++
	return idx
}

func (d *chatStreamDecoder) handle(data map[string]any, ch chan canon.Event) error {
	if !d.started {
		d.id = getStr(data, "id")
		d.model = getStr(data, "model")
		d.started = true
		d.activeText = -1
		d.activeThinking = -1
		ch <- canon.MessageStartEvent{Response: &canon.Response{ID: d.id, Model: d.model}}
	}

	choices, _ := data["choices"].([]any)
	usage := getUsage(data, "usage")

	if len(choices) == 0 {
		if usage != nil {
			ch <- canon.MessageDeltaEvent{Usage: usage}
		}
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil
	}
	delta, _ := choice["delta"].(map[string]any)
	finishReason := getStr(choice, "finish_reason")

	if finishReason != "" {
		d.closeActive(ch)
		d.closeAllTools(ch)
		ev := canon.MessageDeltaEvent{FinishReason: chatFinishToCanon(finishReason)}
		if usage != nil {
			ev.Usage = usage
		}
		ch <- ev
		return nil
	}
	if delta == nil {
		return nil
	}

	// Text content.
	if content := getStr(delta, "content"); content != "" {
		if d.activeText == -1 {
			idx := d.newIndex()
			d.activeText = idx
			ch <- canon.ContentBlockStartEvent{Index: idx, Block: &canon.TextBlock{}}
		}
		ch <- canon.ContentBlockDeltaEvent{Index: d.activeText, Delta: canon.Delta{Type: canon.DeltaText, Text: content}}
	}

	// Reasoning content.
	if reasoning := getStr(delta, "reasoning_content"); reasoning != "" {
		if d.activeThinking == -1 {
			idx := d.newIndex()
			d.activeThinking = idx
			ch <- canon.ContentBlockStartEvent{Index: idx, Block: &canon.ThinkingBlock{}}
		}
		ch <- canon.ContentBlockDeltaEvent{Index: d.activeThinking, Delta: canon.Delta{Type: canon.DeltaThinking, Text: reasoning}}
	}

	// Tool calls.
	if tcs, ok := delta["tool_calls"].([]any); ok {
		for _, raw := range tcs {
			tc, _ := raw.(map[string]any)
			if tc == nil {
				continue
			}
			chatIdx := int(getInt(tc, "index"))
			fn := getMap(tc, "function")
			args := getStr(fn, "arguments")
			if _, exists := d.toolBlocks[chatIdx]; !exists {
				// New tool block: close any active text/thinking block first.
				d.closeActive(ch)
				idx := d.newIndex()
				d.toolBlocks[chatIdx] = idx
				d.openTools = append(d.openTools, idx)
				ch <- canon.ContentBlockStartEvent{Index: idx, Block: &canon.ToolUseBlock{
					ID:   getStr(tc, "id"),
					Name: getStr(fn, "name"),
				}}
			}
			if args != "" {
				idx := d.toolBlocks[chatIdx]
				ch <- canon.ContentBlockDeltaEvent{Index: idx, Delta: canon.Delta{Type: canon.DeltaInputJSON, Partial: args}}
			}
		}
	}
	return nil
}

func (d *chatStreamDecoder) closeActive(ch chan canon.Event) {
	if d.activeText != -1 {
		ch <- canon.ContentBlockStopEvent{Index: d.activeText}
		d.activeText = -1
	}
	if d.activeThinking != -1 {
		ch <- canon.ContentBlockStopEvent{Index: d.activeThinking}
		d.activeThinking = -1
	}
}

func (d *chatStreamDecoder) closeAllTools(ch chan canon.Event) {
	indices := append([]int(nil), d.openTools...)
	sort.Ints(indices)
	for _, idx := range indices {
		ch <- canon.ContentBlockStopEvent{Index: idx}
	}
	d.openTools = nil
	d.toolBlocks = map[int]int{}
}

func getUsage(m map[string]any, k string) *canon.Usage {
	u := getMap(m, k)
	if u == nil {
		return nil
	}
	return &canon.Usage{
		InputTokens:  getInt(u, "prompt_tokens"),
		OutputTokens: getInt(u, "completion_tokens"),
		CacheRead:    getInt(getMap(u, "prompt_tokens_details"), "cached_tokens"),
	}
}

// ---- streaming: encode ----

func (ChatCodec) EncodeStream(w io.Writer, events <-chan canon.Event) error {
	e := &chatStreamEncoder{
		w:        w,
		toolIdx:  map[int]int{},
		nextTool: 0,
	}
	for ev := range events {
		if err := e.handle(ev); err != nil {
			return err
		}
	}
	return nil
}

type chatStreamEncoder struct {
	w        io.Writer
	id       string
	model    string
	toolIdx  map[int]int // canonical index -> chat tool_calls index
	nextTool int
	finish   canon.FinishReason
	usage    *canon.Usage
	terminal bool
}

func (e *chatStreamEncoder) chunk(delta any) error {
	obj := map[string]any{
		"id":     e.id,
		"object": "chat.completion.chunk",
		"model":  e.model,
	}
	if delta != nil {
		obj["choices"] = []any{map[string]any{"index": 0, "delta": delta}}
	} else {
		obj["choices"] = []any{}
	}
	payload, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return sse.WriteEvent(e.w, "", string(payload))
}

func (e *chatStreamEncoder) handle(ev canon.Event) error {
	switch x := ev.(type) {
	case canon.MessageStartEvent:
		if x.Response != nil {
			e.id = x.Response.ID
			e.model = x.Response.Model
		}
		return e.chunk(map[string]any{"role": "assistant"})
	case canon.ContentBlockStartEvent:
		if tu, ok := x.Block.(*canon.ToolUseBlock); ok {
			idx := e.nextTool
			e.nextTool++
			e.toolIdx[x.Index] = idx
			return e.chunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    idx,
					"id":       tu.ID,
					"type":     "function",
					"function": map[string]any{"name": tu.Name},
				}},
			})
		}
		return nil
	case canon.ContentBlockDeltaEvent:
		switch x.Delta.Type {
		case canon.DeltaText:
			return e.chunk(map[string]any{"content": x.Delta.Text})
		case canon.DeltaInputJSON:
			idx := e.toolIdx[x.Index]
			return e.chunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    idx,
					"function": map[string]any{"arguments": x.Delta.Partial},
				}},
			})
		case canon.DeltaThinking:
			return e.chunk(map[string]any{"reasoning_content": x.Delta.Text})
		}
	case canon.ContentBlockStopEvent:
		return nil
	case canon.MessageDeltaEvent:
		if x.FinishReason != "" {
			e.finish = x.FinishReason
			e.terminal = true
			delta := map[string]any{}
			choices := []any{map[string]any{"index": 0, "delta": delta, "finish_reason": canonToChatFinish(e.finish)}}
			obj := map[string]any{
				"id":      e.id,
				"object":  "chat.completion.chunk",
				"model":   e.model,
				"choices": choices,
			}
			payload, err := json.Marshal(obj)
			if err != nil {
				return err
			}
			if err := sse.WriteEvent(e.w, "", string(payload)); err != nil {
				return err
			}
			if x.Usage != nil {
				e.usage = x.Usage
			}
			if e.usage != nil {
				return e.usageOnly()
			}
		} else if x.Usage != nil {
			e.usage = x.Usage
			return e.usageOnly()
		}
	case canon.MessageStopEvent:
		return sse.WriteEvent(e.w, "", "[DONE]")
	case canon.ErrorEvent:
		e.terminal = true
		msg := "stream error"
		if x.Err != nil {
			msg = x.Err.Error()
		}
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
		return sse.WriteEvent(e.w, "", string(body))
	}
	return nil
}

func (e *chatStreamEncoder) usageOnly() error {
	if e.usage == nil {
		return nil
	}
	obj := map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"model":   e.model,
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     e.usage.InputTokens,
			"completion_tokens": e.usage.OutputTokens,
			"total_tokens":      e.usage.InputTokens + e.usage.OutputTokens,
		},
	}
	payload, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return sse.WriteEvent(e.w, "", string(payload))
}

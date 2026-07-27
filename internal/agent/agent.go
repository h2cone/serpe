package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/provider"
)

// Provider is the canonical backend an Agent needs.
type Provider interface {
	provider.Provider
}

// Config contains the dependencies and request settings for an Agent.
type Config struct {
	Provider     Provider
	Tools        ToolExecutor
	Model        string
	Instructions string
	MaxTokens    int
}

// Agent runs a protocol- and shell-neutral tool loop.
type Agent struct {
	provider     Provider
	tools        ToolExecutor
	model        string
	instructions string
	maxTokens    int
}

// TurnResult is the shell-neutral result of one user turn. Conversation
// contains the complete transcript after all model and tool-use rounds, so a
// CLI, TUI, or Web UI can persist it without reconstructing hidden tool turns.
type TurnResult struct {
	Text         string
	Response     *canon.Response
	Conversation canon.Conversation
}

// New creates an Agent from config.
func New(config Config) *Agent {
	return &Agent{
		provider:     config.Provider,
		tools:        config.Tools,
		model:        config.Model,
		instructions: config.Instructions,
		maxTokens:    config.MaxTokens,
	}
}

// Run is a single-turn convenience entry point. It creates a fresh transcript
// from one user message and returns the final text response.
func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	result, err := a.RunTurn(ctx, a.newRequest(userMessage))
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// RunStream is a single-turn streaming convenience entry point. The writer can
// be backed by a terminal, a TUI model, or a Web transport.
func (a *Agent) RunStream(ctx context.Context, userMessage string, w io.Writer) (string, error) {
	return a.RunStreamRequest(ctx, a.newRequest(userMessage), w)
}

func (a *Agent) newRequest(text string) *canon.Request {
	return &canon.Request{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		Conversation: canon.Conversation{
			System: a.instructions,
			Messages: []canon.Message{{
				Role:    canon.RoleUser,
				Content: []canon.ContentBlock{&canon.TextBlock{Text: text}},
			}},
		},
	}
}

// RunRequest is the text-only compatibility wrapper around RunTurn.
func (a *Agent) RunRequest(ctx context.Context, seed *canon.Request) (string, error) {
	result, err := a.RunTurn(ctx, seed)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// RunTurn continues a caller-provided canonical transcript and returns the
// updated transcript together with the final response. Interactive shells
// should use this method directly or create a Session.
func (a *Agent) RunTurn(ctx context.Context, seed *canon.Request) (*TurnResult, error) {
	return a.runTurn(ctx, seed, nil)
}

// RunStreamRequest continues a caller-provided canonical transcript through the
// provider's streaming path. Tool-use turns are internal: only the final
// assistant text from a no-tool turn is written to w and returned.
func (a *Agent) RunStreamRequest(ctx context.Context, seed *canon.Request, w io.Writer) (string, error) {
	result, err := a.RunStreamTurn(ctx, seed, w)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// RunStreamTurn is the structured streaming entry point for interactive
// shells. It returns the same complete transcript as RunTurn while sending the
// final visible answer to w.
func (a *Agent) RunStreamTurn(ctx context.Context, seed *canon.Request, w io.Writer) (*TurnResult, error) {
	if w == nil {
		w = io.Discard
	}
	return a.runTurn(ctx, seed, w)
}

// runTurn is the ReAct-like loop: ask, act on tool calls, append observations,
// and repeat until the model answers without an action.
func (a *Agent) runTurn(ctx context.Context, seed *canon.Request, output io.Writer) (*TurnResult, error) {
	if seed == nil {
		return nil, errors.New("nil request")
	}
	if a.provider == nil {
		return nil, errors.New("nil provider")
	}

	base := cloneRequest(seed)
	if base.Model == "" {
		base.Model = a.model
	}
	if base.MaxTokens == 0 {
		base.MaxTokens = a.maxTokens
	}
	base.Stream = output != nil

	conv := base.Conversation
	if conv.System == "" {
		conv.System = a.instructions
	}

	var lastStep *stepFingerprint
	for {
		request, err := a.buildRequest(base, conv)
		if err != nil {
			return nil, err
		}

		response, err := a.complete(ctx, request)
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, errors.New("provider returned nil response")
		}

		conv.Messages = append(conv.Messages, canon.Message{
			Role:    canon.RoleAssistant,
			Content: cloneBlocks(response.Content),
		})

		toolUses := collectToolUses(response.Content)
		if len(toolUses) == 0 {
			text := collectText(response.Content)
			if text == "" {
				return nil, fmt.Errorf("response did not contain tool calls or message text")
			}
			if output != nil {
				if _, err := io.WriteString(output, text); err != nil {
					return nil, err
				}
			}
			return newTurnResult(text, response, conv), nil
		}

		results, currentStep, err := a.executeToolUses(ctx, toolUses)
		if err != nil {
			return nil, err
		}
		if lastStep != nil && reflect.DeepEqual(*lastStep, currentStep) {
			return nil, fmt.Errorf("semantic stop condition triggered: repeated identical tool calls produced identical outputs")
		}

		lastStep = &currentStep
		relaxForcedToolChoice(&base)
		conv.Messages = append(conv.Messages, canon.Message{
			Role:    canon.RoleUser,
			Content: results,
		})
	}
}

func (a *Agent) complete(ctx context.Context, request *canon.Request) (*canon.Response, error) {
	if !request.Stream {
		return a.provider.Complete(ctx, request)
	}
	events, err := a.provider.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	return canon.Assemble(events)
}

func newTurnResult(text string, response *canon.Response, conv canon.Conversation) *TurnResult {
	return &TurnResult{
		Text:         text,
		Response:     cloneResponse(response),
		Conversation: cloneConversation(conv),
	}
}

func (a *Agent) executeToolUses(ctx context.Context, toolUses []canon.ToolUseBlock) ([]canon.ContentBlock, stepFingerprint, error) {
	results := make([]canon.ContentBlock, 0, len(toolUses))
	currentStep := stepFingerprint{outcomes: make([]toolCallOutcome, 0, len(toolUses))}
	for _, toolUse := range toolUses {
		args, err := parseInput(toolUse.Input)
		if err != nil {
			return nil, stepFingerprint{}, err
		}
		input, err := normalizedInput(args)
		if err != nil {
			return nil, stepFingerprint{}, err
		}

		result, execErr := a.tools.execute(ctx, toolUse.Name, args)
		if execErr != nil {
			result = Result{
				Content: []canon.ContentBlock{&canon.TextBlock{Text: "Error: " + execErr.Error()}},
				IsError: true,
			}
		}

		currentStep.push(toolUse.Name, input, result)
		results = append(results, &canon.ToolResultBlock{
			ToolUseID: toolUse.ID,
			Content:   cloneBlocks(result.Content),
			IsError:   result.IsError,
		})
	}
	return results, currentStep, nil
}

func (a *Agent) buildRequest(base canon.Request, conv canon.Conversation) (*canon.Request, error) {
	req := base
	req.Conversation = conv
	req.Tools = a.tools.Definitions()
	if req.ToolChoice.Mode == "" {
		req.ToolChoice = canon.ToolChoice{Mode: canon.ToolChoiceAuto}
	}
	switch req.ToolChoice.Mode {
	case canon.ToolChoiceAuto, canon.ToolChoiceRequired, canon.ToolChoiceNone:
	case canon.ToolChoiceSpecific:
		if !hasToolDefinition(req.Tools, req.ToolChoice.Name) {
			return nil, fmt.Errorf("unsupported tool_choice specific %q: tool is not registered", req.ToolChoice.Name)
		}
	default:
		return nil, fmt.Errorf("unsupported tool_choice mode %q", req.ToolChoice.Mode)
	}
	return &req, nil
}

func relaxForcedToolChoice(req *canon.Request) {
	if req == nil {
		return
	}
	switch req.ToolChoice.Mode {
	case canon.ToolChoiceRequired, canon.ToolChoiceSpecific:
		req.ToolChoice = canon.ToolChoice{Mode: canon.ToolChoiceAuto}
	}
}

func hasToolDefinition(defs []canon.Tool, name string) bool {
	if name == "" {
		return false
	}
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func collectToolUses(blocks []canon.ContentBlock) []canon.ToolUseBlock {
	var out []canon.ToolUseBlock
	for _, b := range blocks {
		if tu, ok := b.(*canon.ToolUseBlock); ok {
			out = append(out, *tu)
		}
	}
	return out
}

func collectText(blocks []canon.ContentBlock) string {
	var chunks []string
	for _, b := range blocks {
		if t, ok := b.(*canon.TextBlock); ok {
			chunks = append(chunks, t.Text)
		}
	}
	return strings.Join(chunks, "\n")
}

func parseInput(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid JSON arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("invalid JSON arguments: %w", err)
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

func normalizedInput(args map[string]any) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("normalize JSON arguments: %w", err)
	}
	return string(data), nil
}

func cloneRequest(in *canon.Request) canon.Request {
	out := *in
	out.Conversation = cloneConversation(in.Conversation)
	if in.Tools != nil {
		out.Tools = make([]canon.Tool, len(in.Tools))
		for i, tool := range in.Tools {
			out.Tools[i] = tool
			out.Tools[i].Parameters = append(json.RawMessage(nil), tool.Parameters...)
			out.Tools[i].Extra = cloneMap(tool.Extra)
		}
	}
	if in.Temperature != nil {
		value := *in.Temperature
		out.Temperature = &value
	}
	if in.TopP != nil {
		value := *in.TopP
		out.TopP = &value
	}
	if in.Stop != nil {
		out.Stop = append([]string(nil), in.Stop...)
	}
	if in.Extra != nil {
		out.Extra = cloneMap(in.Extra)
	}
	return out
}

func cloneResponse(in *canon.Response) *canon.Response {
	if in == nil {
		return nil
	}
	out := *in
	out.Content = cloneBlocks(in.Content)
	if in.Extra != nil {
		out.Extra = cloneMap(in.Extra)
	}
	return &out
}

func cloneConversation(in canon.Conversation) canon.Conversation {
	out := in
	out.Messages = append([]canon.Message(nil), in.Messages...)
	for i := range out.Messages {
		out.Messages[i].Content = cloneBlocks(out.Messages[i].Content)
		if out.Messages[i].Extra != nil {
			out.Messages[i].Extra = cloneMap(out.Messages[i].Extra)
		}
	}
	if in.Extra != nil {
		out.Extra = cloneMap(in.Extra)
	}
	return out
}

func cloneBlocks(in []canon.ContentBlock) []canon.ContentBlock {
	if in == nil {
		return nil
	}
	out := make([]canon.ContentBlock, len(in))
	for i, block := range in {
		switch b := block.(type) {
		case *canon.TextBlock:
			if b != nil {
				out[i] = &canon.TextBlock{Text: b.Text, Extra: cloneMap(b.Extra)}
			}
		case *canon.ImageBlock:
			if b != nil {
				out[i] = &canon.ImageBlock{
					MediaType: b.MediaType,
					URL:       b.URL,
					Data:      b.Data,
					Extra:     cloneMap(b.Extra),
				}
			}
		case *canon.ToolUseBlock:
			if b != nil {
				out[i] = &canon.ToolUseBlock{
					ID:    b.ID,
					Name:  b.Name,
					Input: append(json.RawMessage(nil), b.Input...),
					Extra: cloneMap(b.Extra),
				}
			}
		case *canon.ToolResultBlock:
			if b != nil {
				out[i] = &canon.ToolResultBlock{
					ToolUseID: b.ToolUseID,
					Content:   cloneBlocks(b.Content),
					IsError:   b.IsError,
					Extra:     cloneMap(b.Extra),
				}
			}
		case *canon.ThinkingBlock:
			if b != nil {
				out[i] = &canon.ThinkingBlock{
					Text:      b.Text,
					Signature: b.Signature,
					Extra:     cloneMap(b.Extra),
				}
			}
		}
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(in any) any {
	switch value := in.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), value...)
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	case []byte:
		return append([]byte(nil), value...)
	default:
		return value
	}
}

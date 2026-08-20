package chatcompletions

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

type chatSource struct {
	reader        *sse.Reader
	requestID     string
	fallbackModel string
	queue         shared.EventQueue
	choices       map[int]*choiceStreamState
	started       bool
	finished      bool
	responseID    string
	model         string
	created       int64
	usage         *models.Usage
	toolGuard     *shared.ToolCallGuard
}

type choiceStreamState struct {
	nextPart    int
	textPart    int
	refusalPart int
	tools       map[int]*toolStreamState
	open        map[int]bool
	finish      models.FinishReason
	rawFinish   string
}

type toolStreamState struct {
	part    int
	id      string
	name    string
	pending strings.Builder
	started bool
	hadArgs bool
}

// NewSSEStreamSource builds a Chat Completions event source from a raw SSE
// response obtained through an official SDK request.
func NewSSEStreamSource(reader *sse.Reader, requestID, fallbackModel string, toolLimits ...shared.ToolCallLimits) models.EventSource {
	limits := shared.DefaultToolCallLimits()
	if len(toolLimits) > 0 {
		limits = toolLimits[0]
	}
	return &chatSource{reader: reader, requestID: requestID, fallbackModel: fallbackModel, choices: make(map[int]*choiceStreamState), toolGuard: shared.NewToolCallGuard(limits)}
}

func (s *chatSource) Next() (models.Event, error) {
	for {
		if event, ok := s.queue.Shift(); ok {
			return event, nil
		}
		if s.finished {
			return models.Event{}, io.EOF
		}
		event, err := s.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return models.Event{}, io.EOF
			}
			return models.Event{}, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "sse_read_error", Message: "failed to parse Chat Completions SSE event", Cause: err}
		}
		if len(event.Data) == 0 {
			continue
		}
		if bytes.Equal(event.Data, []byte("[DONE]")) {
			if !s.started {
				return models.Event{}, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "missing_response", Message: "received [DONE] before a response chunk"}
			}
			if err := s.complete(event.ID); err != nil {
				return models.Event{}, err
			}
			s.finished = true
			continue
		}
		if err := shared.ValidateStreamJSON(event.Data); err != nil {
			return models.Event{}, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "invalid_json", Message: "Chat Completions stream event is not strict JSON", Cause: err}
		}
		var chunk chatResponse
		if err := shared.DecodeJSON(event.Data, &chunk); err != nil {
			return models.Event{}, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "invalid_json", Message: "Chat Completions stream event is not valid JSON", Cause: err}
		}
		if chunk.Error != nil {
			return models.Event{}, normalizeWireError(chunk.Error, "stream_next", s.requestID)
		}
		if err := s.consumeChunk(chunk, event.ID); err != nil {
			return models.Event{}, err
		}
	}
}

func (s *chatSource) consumeChunk(chunk chatResponse, eventID string) error {
	if chunk.ID != "" {
		s.responseID = chunk.ID
	}
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Created != 0 {
		s.created = chunk.Created
	}
	if !s.started {
		modelName := s.model
		if modelName == "" {
			modelName = s.fallbackModel
		}
		info := &models.ResponseInfo{Provider: "openai", ID: s.responseID, Model: modelName, RequestID: s.requestID}
		if s.created != 0 {
			info.CreatedAt = time.Unix(s.created, 0).UTC()
		}
		s.queue.Push(models.Event{Kind: models.EventResponseStart, ProviderEventID: eventID, Response: info})
		s.started = true
	}
	for _, choice := range chunk.Choices {
		state := s.choice(choice.Index)
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			part := s.ensureSimplePart(choice.Index, state, models.ContentText)
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: choice.Index, PartIndex: part, Delta: models.Delta{Kind: models.DeltaText, Text: *choice.Delta.Content}, ProviderEventID: eventID})
		}
		if choice.Delta.Refusal != nil && *choice.Delta.Refusal != "" {
			part := s.ensureSimplePart(choice.Index, state, models.ContentRefusal)
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: choice.Index, PartIndex: part, Delta: models.Delta{Kind: models.DeltaRefusal, Text: *choice.Delta.Refusal}, ProviderEventID: eventID})
		}
		for _, delta := range choice.Delta.ToolCalls {
			if err := s.consumeToolDelta(choice.Index, state, delta.Index, delta.ID, delta.Function.Name, delta.Function.Arguments, eventID); err != nil {
				return err
			}
		}
		if choice.Delta.FunctionCall != nil {
			if err := s.consumeToolDelta(choice.Index, state, -1, "", choice.Delta.FunctionCall.Name, choice.Delta.FunctionCall.Arguments, eventID); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil {
			state.rawFinish = *choice.FinishReason
			state.finish = mapFinish(*choice.FinishReason)
			if err := s.closeChoice(choice.Index, state, eventID); err != nil {
				return err
			}
		}
	}
	if chunk.Usage != nil {
		usage := decodeUsage(chunk.Usage)
		s.usage = &usage
	}
	return nil
}

func (s *chatSource) choice(index int) *choiceStreamState {
	state := s.choices[index]
	if state == nil {
		state = &choiceStreamState{textPart: -1, refusalPart: -1, tools: make(map[int]*toolStreamState), open: make(map[int]bool), finish: models.FinishUnknown}
		s.choices[index] = state
	}
	return state
}

func (s *chatSource) allocatePart(state *choiceStreamState) int {
	part := state.nextPart
	state.nextPart++
	state.open[part] = true
	return part
}

func (s *chatSource) ensureSimplePart(candidate int, state *choiceStreamState, kind models.ContentKind) int {
	pointer := &state.textPart
	content := models.Text("")
	if kind == models.ContentRefusal {
		pointer = &state.refusalPart
		content = models.Refusal("")
	}
	if *pointer >= 0 {
		return *pointer
	}
	*pointer = s.allocatePart(state)
	s.queue.Push(models.Event{Kind: models.EventPartStart, CandidateIndex: candidate, PartIndex: *pointer, Part: content})
	return *pointer
}

func (s *chatSource) consumeToolDelta(candidate int, state *choiceStreamState, toolIndex int, id, name, arguments, eventID string) error {
	key := fmt.Sprintf("%d/%d", candidate, toolIndex)
	tool := state.tools[toolIndex]
	if tool == nil {
		if err := s.toolGuard.Start(key, id, name); err != nil {
			return chatResponseLimit(err)
		}
		tool = &toolStreamState{part: -1}
		state.tools[toolIndex] = tool
	} else {
		nextID, nextName := tool.id, tool.name
		if id != "" {
			nextID = id
		}
		if name != "" {
			nextName = name
		}
		if err := s.toolGuard.Identity(key, nextID, nextName); err != nil {
			return chatResponseLimit(err)
		}
	}
	if arguments != "" {
		if err := s.toolGuard.AddArguments(key, len(arguments)); err != nil {
			return chatResponseLimit(err)
		}
	}
	if id != "" {
		tool.id = id
	}
	if name != "" {
		tool.name = name
	}
	if arguments != "" {
		tool.pending.WriteString(arguments)
		tool.hadArgs = true
	}
	if !tool.started && tool.name != "" {
		tool.part = s.allocatePart(state)
		tool.started = true
		s.queue.Push(models.Event{Kind: models.EventPartStart, CandidateIndex: candidate, PartIndex: tool.part, Part: models.ToolCallContent(tool.id, tool.name, nil), CallID: tool.id, ProviderEventID: eventID})
	}
	if tool.started && tool.pending.Len() > 0 {
		fragment := tool.pending.String()
		tool.pending.Reset()
		s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: candidate, PartIndex: tool.part, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: fragment}, CallID: tool.id, ProviderEventID: eventID})
	}
	return nil
}

func (s *chatSource) closeChoice(candidate int, state *choiceStreamState, eventID string) error {
	for index, tool := range state.tools {
		if tool.started && !tool.hadArgs {
			if err := s.toolGuard.AddArguments(fmt.Sprintf("%d/%d", candidate, index), 2); err != nil {
				return chatResponseLimit(err)
			}
			tool.hadArgs = true
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: candidate, PartIndex: tool.part, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: "{}"}, CallID: tool.id, ProviderEventID: eventID})
		}
	}
	for _, part := range slices.Sorted(maps.Keys(state.open)) {
		if state.open[part] {
			s.queue.Push(models.Event{Kind: models.EventPartEnd, CandidateIndex: candidate, PartIndex: part, ProviderEventID: eventID})
			state.open[part] = false
		}
	}
	return nil
}

func (s *chatSource) complete(eventID string) error {
	indexes := slices.Sorted(maps.Keys(s.choices))
	finishes := make([]models.CandidateFinish, 0, len(indexes))
	for _, index := range indexes {
		state := s.choices[index]
		for _, tool := range state.tools {
			if !tool.started {
				return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "incomplete_tool_call", Message: "tool call ended before its function name was received"}
			}
		}
		if err := s.closeChoice(index, state, eventID); err != nil {
			return err
		}
		finishes = append(finishes, models.CandidateFinish{CandidateIndex: index, Reason: state.finish, RawReason: state.rawFinish})
	}
	modelName := s.model
	if modelName == "" {
		modelName = s.fallbackModel
	}
	s.queue.Push(models.Event{Kind: models.EventResponseEnd, ProviderEventID: eventID, Response: &models.ResponseInfo{Provider: "openai", ID: s.responseID, Model: modelName, Status: models.ResponseStatusCompleted, RequestID: s.requestID}, Finishes: finishes, Usage: s.usage})
	return nil
}

func chatResponseLimit(cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "response_limit", Message: "Chat Completions tool-call response exceeds configured limit", Cause: cause}
}

func (s *chatSource) Close() error {
	if s.reader == nil {
		return nil
	}
	return s.reader.Close()
}

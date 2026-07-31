package chatcompletions

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
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
	closeOnce     sync.Once
	closeErr      error
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

func newChatSource(reader *sse.Reader, requestID, fallbackModel string) *chatSource {
	return &chatSource{reader: reader, requestID: requestID, fallbackModel: fallbackModel, choices: make(map[int]*choiceStreamState)}
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
		data := strings.TrimSpace(string(event.Data))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if !s.started {
				return models.Event{}, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "missing_response", Message: "received [DONE] before a response chunk"}
			}
			if err := s.complete(event.ID); err != nil {
				return models.Event{}, err
			}
			s.finished = true
			continue
		}
		var chunk chatResponse
		if err := json.Unmarshal(event.Data, &chunk); err != nil {
			return models.Event{}, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "invalid_json", Message: "Chat Completions stream event is not valid JSON", Cause: err}
		}
		if chunk.Error != nil {
			return models.Event{}, normalizeWireError(chunk.Error, "stream_next", s.requestID)
		}
		s.consumeChunk(chunk, event.ID)
	}
}

func (s *chatSource) consumeChunk(chunk chatResponse, eventID string) {
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
			s.consumeToolDelta(choice.Index, state, delta.Index, delta.ID, delta.Function.Name, delta.Function.Arguments, eventID)
		}
		if choice.Delta.FunctionCall != nil {
			s.consumeToolDelta(choice.Index, state, -1, "", choice.Delta.FunctionCall.Name, choice.Delta.FunctionCall.Arguments, eventID)
		}
		if choice.FinishReason != nil {
			state.rawFinish = *choice.FinishReason
			state.finish = mapFinish(*choice.FinishReason)
			s.closeChoice(choice.Index, state, eventID)
		}
	}
	if chunk.Usage != nil {
		usage := decodeUsage(chunk.Usage)
		s.usage = &usage
	}
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

func (s *chatSource) consumeToolDelta(candidate int, state *choiceStreamState, toolIndex int, id, name, arguments, eventID string) {
	tool := state.tools[toolIndex]
	if tool == nil {
		tool = &toolStreamState{part: -1}
		state.tools[toolIndex] = tool
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
}

func (s *chatSource) closeChoice(candidate int, state *choiceStreamState, eventID string) {
	for _, tool := range state.tools {
		if tool.started && !tool.hadArgs {
			tool.hadArgs = true
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: candidate, PartIndex: tool.part, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: "{}"}, CallID: tool.id, ProviderEventID: eventID})
		}
	}
	indexes := make([]int, 0, len(state.open))
	for part, open := range state.open {
		if open {
			indexes = append(indexes, part)
		}
	}
	sort.Ints(indexes)
	for _, part := range indexes {
		s.queue.Push(models.Event{Kind: models.EventPartEnd, CandidateIndex: candidate, PartIndex: part, ProviderEventID: eventID})
		state.open[part] = false
	}
}

func (s *chatSource) complete(eventID string) error {
	indexes := make([]int, 0, len(s.choices))
	for index := range s.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	finishes := make([]models.CandidateFinish, 0, len(indexes))
	for _, index := range indexes {
		state := s.choices[index]
		for _, tool := range state.tools {
			if !tool.started {
				return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream_next", Code: "incomplete_tool_call", Message: "tool call ended before its function name was received"}
			}
		}
		s.closeChoice(index, state, eventID)
		finishes = append(finishes, models.CandidateFinish{CandidateIndex: index, Reason: state.finish, RawReason: state.rawFinish})
	}
	modelName := s.model
	if modelName == "" {
		modelName = s.fallbackModel
	}
	s.queue.Push(models.Event{Kind: models.EventResponseEnd, ProviderEventID: eventID, Response: &models.ResponseInfo{Provider: "openai", ID: s.responseID, Model: modelName, Status: models.ResponseStatusCompleted, RequestID: s.requestID}, Finishes: finishes, Usage: s.usage})
	return nil
}

func (s *chatSource) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.reader.Close() })
	return s.closeErr
}

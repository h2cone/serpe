package anthropic

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

type messageSource struct {
	reader        *sse.Reader
	requestID     string
	fallbackModel string
	ignoreUnknown bool
	stateLimit    int64
	queue         shared.EventQueue
	blocks        map[int]*blockState
	started       bool
	finished      bool
	responseID    string
	model         string
	finish        models.FinishReason
	rawFinish     string
	usage         models.Usage
	wireUsage     usageWire
	stateBlocks   map[int]json.RawMessage
	hasState      bool
}

type blockState struct {
	kind        models.ContentKind
	open        bool
	ignored     bool
	hadArgs     bool
	initialArgs json.RawMessage
	text        strings.Builder
	args        strings.Builder
	thinking    strings.Builder
	signature   strings.Builder
	wire        contentWire
}

// NewSSEStreamSource builds an Anthropic event source from a raw SSE response
// obtained through an official SDK request.
func NewSSEStreamSource(reader *sse.Reader, requestID, fallbackModel string, ignoreUnknown bool, stateLimit int64) models.EventSource {
	return &messageSource{reader: reader, requestID: requestID, fallbackModel: fallbackModel, ignoreUnknown: ignoreUnknown, stateLimit: stateLimit, blocks: make(map[int]*blockState), stateBlocks: make(map[int]json.RawMessage), finish: models.FinishUnknown}
}

func (s *messageSource) Next() (models.Event, error) {
	for {
		if event, ok := s.queue.Shift(); ok {
			return event, nil
		}
		if s.finished {
			return models.Event{}, io.EOF
		}
		wireEvent, err := s.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return models.Event{}, io.EOF
			}
			return models.Event{}, streamProtocol("sse_read_error", "failed to parse Anthropic SSE event", err)
		}
		if len(wireEvent.Data) == 0 {
			continue
		}
		var event streamEventWire
		if err := json.Unmarshal(wireEvent.Data, &event); err != nil {
			return models.Event{}, streamProtocol("invalid_json", "Anthropic stream event is not valid JSON", err)
		}
		if err := s.consume(event, wireEvent.ID); err != nil {
			return models.Event{}, err
		}
	}
}

func (s *messageSource) consume(event streamEventWire, eventID string) error {
	switch event.Type {
	case "message_start":
		if s.started {
			return streamProtocol("duplicate_start", "duplicate message_start event", nil)
		}
		var message messageWire
		if err := json.Unmarshal(event.Message, &message); err != nil {
			return streamProtocol("invalid_start", "message_start has invalid metadata", err)
		}
		s.responseID = message.ID
		s.model = message.Model
		if s.model == "" {
			s.model = s.fallbackModel
		}
		if message.Usage != nil {
			s.wireUsage = *message.Usage
			s.usage = decodeUsage(&s.wireUsage)
		}
		s.queue.Push(models.Event{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "anthropic", ID: message.ID, Model: s.model, RequestID: s.requestID}, ProviderEventID: eventID})
		s.started = true
	case "content_block_start":
		if !s.started {
			return streamProtocol("missing_start", "content block before message_start", nil)
		}
		if _, exists := s.blocks[event.Index]; exists {
			return streamProtocol("duplicate_part", "duplicate content_block_start", nil)
		}
		var block contentWire
		if err := json.Unmarshal(event.ContentBlock, &block); err != nil {
			return streamProtocol("invalid_part", "content_block_start has invalid content", err)
		}
		state := &blockState{open: true, wire: block}
		var content models.Content
		switch block.Type {
		case "text":
			state.kind = models.ContentText
			state.text.WriteString(block.Text)
			content = models.Text("")
		case "tool_use":
			if block.Name == "" {
				return streamProtocol("invalid_tool_call", "tool_use block has no name", nil)
			}
			input, ok := normalizeToolInput(block.Input)
			if !ok {
				return streamProtocol("invalid_tool_call", "Anthropic tool input is not a JSON object", nil)
			}
			state.kind = models.ContentToolCall
			state.initialArgs = input
			content = models.ToolCallContent(block.ID, block.Name, nil)
		case "thinking":
			state.kind = models.ContentReasoningSummary
			state.thinking.WriteString(block.Thinking)
			state.signature.WriteString(block.Signature)
			s.hasState = true
			content = models.ReasoningSummary("")
		case "redacted_thinking":
			s.stateBlocks[event.Index] = append(json.RawMessage(nil), event.ContentBlock...)
			s.hasState = true
			s.blocks[event.Index] = &blockState{open: true, ignored: true}
			return nil
		default:
			if s.ignoreUnknown {
				s.blocks[event.Index] = &blockState{open: true, ignored: true}
				return nil
			}
			return streamProtocol("unknown_content", "unknown Anthropic content block type "+block.Type, nil)
		}
		s.blocks[event.Index] = state
		s.queue.Push(models.Event{Kind: models.EventPartStart, CandidateIndex: 0, PartIndex: event.Index, Part: content, ItemID: block.ID, CallID: block.ID, ProviderEventID: eventID})
		if block.Type == "text" && block.Text != "" {
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: event.Index, Delta: models.Delta{Kind: models.DeltaText, Text: block.Text}, ProviderEventID: eventID})
		}
		if block.Type == "thinking" && block.Thinking != "" {
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: event.Index, Delta: models.Delta{Kind: models.DeltaReasoningSummary, Text: block.Thinking}, ProviderEventID: eventID})
		}
	case "content_block_delta":
		state := s.blocks[event.Index]
		if state == nil || !state.open {
			return streamProtocol("missing_part", "content_block_delta before start or after stop", nil)
		}
		if state.ignored {
			return nil
		}
		switch event.Delta.Type {
		case "text_delta":
			state.text.WriteString(event.Delta.Text)
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: event.Index, Delta: models.Delta{Kind: models.DeltaText, Text: event.Delta.Text}, ProviderEventID: eventID})
		case "input_json_delta":
			if event.Delta.PartialJSON != "" {
				state.hadArgs = true
				state.args.WriteString(event.Delta.PartialJSON)
				s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: event.Index, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: event.Delta.PartialJSON}, ProviderEventID: eventID})
			}
		case "thinking_delta":
			state.thinking.WriteString(event.Delta.Thinking)
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: event.Index, Delta: models.Delta{Kind: models.DeltaReasoningSummary, Text: event.Delta.Thinking}, ProviderEventID: eventID})
		case "signature_delta":
			state.signature.WriteString(event.Delta.Signature)
		default:
			if !s.ignoreUnknown {
				return streamProtocol("unknown_delta", "unknown Anthropic delta type "+event.Delta.Type, nil)
			}
		}
	case "content_block_stop":
		state := s.blocks[event.Index]
		if state == nil || !state.open {
			return streamProtocol("missing_part", "content_block_stop before start or duplicated", nil)
		}
		if state.ignored {
			state.open = false
			return nil
		}
		if state.kind == models.ContentToolCall && !state.hadArgs {
			arguments := string(state.initialArgs)
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: 0, PartIndex: event.Index, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: arguments}, ProviderEventID: eventID})
		}
		switch state.kind {
		case models.ContentText:
			state.wire.Text = state.text.String()
		case models.ContentToolCall:
			if state.hadArgs {
				state.wire.Input = json.RawMessage(state.args.String())
			}
		}
		if state.kind == models.ContentReasoningSummary {
			state.wire.Thinking = state.thinking.String()
			state.wire.Signature = state.signature.String()
		}
		raw, _ := json.Marshal(state.wire)
		s.stateBlocks[event.Index] = raw
		state.open = false
		s.queue.Push(models.Event{Kind: models.EventPartEnd, CandidateIndex: 0, PartIndex: event.Index, ProviderEventID: eventID})
	case "message_delta":
		if event.Delta.StopReason != "" {
			s.rawFinish = event.Delta.StopReason
			s.finish = mapFinish(event.Delta.StopReason)
		}
		if event.Usage != nil {
			if event.Usage.OutputTokens != nil {
				s.wireUsage.OutputTokens = event.Usage.OutputTokens
			}
			if event.Usage.InputTokens != nil {
				s.wireUsage.InputTokens = event.Usage.InputTokens
			}
			if event.Usage.CacheCreationInputTokens != nil {
				s.wireUsage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
			}
			if event.Usage.CacheReadInputTokens != nil {
				s.wireUsage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
			}
			s.usage = decodeUsage(&s.wireUsage)
		}
	case "message_stop":
		if !s.started {
			return streamProtocol("missing_start", "message_stop before message_start", nil)
		}
		for _, state := range s.blocks {
			if state.open {
				return streamProtocol("open_part", "message_stop while a content block is open", nil)
			}
		}
		var providerState *models.ProviderState
		if s.hasState {
			blockIndexes := slices.Sorted(maps.Keys(s.stateBlocks))
			rawBlocks := make([]json.RawMessage, 0, len(blockIndexes))
			for _, index := range blockIndexes {
				rawBlocks = append(rawBlocks, s.stateBlocks[index])
			}
			data, _ := json.Marshal(rawBlocks)
			if int64(len(data)) > s.stateLimit {
				return streamProtocol("provider_state_too_large", "Anthropic provider state exceeds configured limit", nil)
			}
			providerState = &models.ProviderState{Provider: protocol, Data: data}
		}
		status := models.ResponseStatusCompleted
		if s.finish == models.FinishIncomplete {
			status = models.ResponseStatusIncomplete
		}
		finish := models.CandidateFinish{CandidateIndex: 0, Reason: s.finish, RawReason: s.rawFinish, ProviderState: providerState}
		s.queue.Push(models.Event{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "anthropic", ID: s.responseID, Model: s.model, Status: status, RequestID: s.requestID}, Finishes: []models.CandidateFinish{finish}, Usage: &s.usage, ProviderEventID: eventID})
		s.finished = true
	case "ping":
		return nil
	case "error":
		if event.Error != nil {
			return normalizeWireError(event.Error, "stream_next", s.requestID)
		}
		return &models.Error{Kind: models.ErrorUnknown, Provider: "anthropic", Operation: "stream_next", Code: "stream_error", Message: "Anthropic stream reported an error", RequestID: s.requestID}
	default:
		if !s.ignoreUnknown {
			return streamProtocol("unknown_event", "unknown Anthropic event type "+event.Type, nil)
		}
	}
	return nil
}

func (s *messageSource) Close() error {
	if s.reader == nil {
		return nil
	}
	return s.reader.Close()
}

func streamProtocol(code, message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "anthropic", Operation: "stream_next", Code: code, Message: message, Cause: cause}
}

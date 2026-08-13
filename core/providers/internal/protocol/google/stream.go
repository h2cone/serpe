package google

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

type googleSource struct {
	reader             *sse.Reader
	requestID          string
	fallbackModel      string
	stateLimit         int64
	queue              shared.EventQueue
	candidates         map[int]*googleCandidateState
	started            bool
	finished           bool
	responseID         string
	model              string
	usage              *models.Usage
	promptBlock        string
	promptBlockMessage string
	toolGuard          *shared.ToolCallGuard
}

type googleCandidateState struct {
	parts      map[int]*googlePartState
	nextPart   int
	activePart int
	lastPart   int
	finish     models.FinishReason
	rawFinish  string
	role       string
}

type googlePartState struct {
	kind models.ContentKind
	open bool
	wire partWire
	text strings.Builder
}

// NewSSEStreamSource builds a Gemini event source from a raw SSE response
// obtained through an official SDK request.
func NewSSEStreamSource(reader *sse.Reader, requestID, fallbackModel string, stateLimit int64, toolLimits ...shared.ToolCallLimits) models.EventSource {
	limits := shared.DefaultToolCallLimits()
	if len(toolLimits) > 0 {
		limits = toolLimits[0]
	}
	return &googleSource{reader: reader, requestID: requestID, fallbackModel: fallbackModel, stateLimit: stateLimit, candidates: make(map[int]*googleCandidateState), toolGuard: shared.NewToolCallGuard(limits)}
}

func (s *googleSource) Next() (models.Event, error) {
	for {
		if event, ok := s.queue.Shift(); ok {
			return event, nil
		}
		if s.finished {
			return models.Event{}, io.EOF
		}
		wireEvent, readErr := s.reader.Next()
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if !s.hasTerminalEvidence() {
					return models.Event{}, io.EOF
				}
				if completeErr := s.complete(); completeErr != nil {
					return models.Event{}, completeErr
				}
				s.finished = true
				continue
			}
			return models.Event{}, streamProtocol("sse_read_error", "failed to parse Gemini SSE event", readErr)
		}
		data := wireEvent.Data
		eventID := wireEvent.ID
		if len(data) == 0 {
			continue
		}
		if err := shared.ValidateStreamJSON(data); err != nil {
			return models.Event{}, streamProtocol("invalid_json", "Gemini stream event is not strict JSON", err)
		}
		var response responseWire
		if err := json.Unmarshal(data, &response); err != nil {
			return models.Event{}, streamProtocol("invalid_json", "Gemini stream event is not valid JSON", err)
		}
		if response.Error != nil {
			return models.Event{}, normalizeWireError(response.Error, "stream_next", s.requestID)
		}
		if err := s.consume(response, eventID); err != nil {
			return models.Event{}, err
		}
	}
}

func (s *googleSource) consume(response responseWire, eventID string) error {
	if response.ResponseID != "" {
		s.responseID = response.ResponseID
	}
	if response.ModelVersion != "" {
		s.model = response.ModelVersion
	}
	if !s.started {
		modelName := s.model
		if modelName == "" {
			modelName = s.fallbackModel
		}
		s.queue.Push(models.Event{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "google", ID: s.responseID, Model: modelName, RequestID: s.requestID}, ProviderEventID: eventID})
		s.started = true
	}
	for position, wireCandidate := range response.Candidates {
		index := position
		if wireCandidate.Index != nil {
			index = *wireCandidate.Index
		}
		candidate := s.candidate(index)
		if wireCandidate.Content.Role != "" {
			candidate.role = wireCandidate.Content.Role
		}
		for chunkPartIndex, part := range wireCandidate.Content.Parts {
			if err := s.consumePart(index, part, chunkPartIndex == 0, eventID); err != nil {
				return err
			}
		}
		if wireCandidate.FinishReason != "" {
			candidate.rawFinish = wireCandidate.FinishReason
			candidate.finish = mapFinish(wireCandidate.FinishReason)
			if candidate.finish == models.FinishStop && candidateHasTool(candidate) {
				candidate.finish = models.FinishToolCall
			}
		}
	}
	if response.PromptFeedback != nil && response.PromptFeedback.BlockReason != "" {
		s.promptBlock = response.PromptFeedback.BlockReason
		s.promptBlockMessage = response.PromptFeedback.BlockReasonMessage
	}
	if response.UsageMetadata != nil {
		usage := decodeUsage(response.UsageMetadata)
		s.usage = &usage
	}
	return nil
}

func (s *googleSource) candidate(index int) *googleCandidateState {
	state := s.candidates[index]
	if state == nil {
		state = &googleCandidateState{parts: make(map[int]*googlePartState), activePart: -1, lastPart: -1, finish: models.FinishUnknown, role: "model"}
		s.candidates[index] = state
	}
	return state
}

func (s *googleSource) consumePart(candidateIndex int, part partWire, mayContinue bool, eventID string) error {
	candidate := s.candidate(candidateIndex)
	switch {
	case part.Text != nil:
		kind := models.ContentText
		content := models.Text("")
		deltaKind := models.DeltaText
		if part.Thought {
			kind = models.ContentReasoningSummary
			content = models.ReasoningSummary("")
			deltaKind = models.DeltaReasoningSummary
		}
		partIndex := candidate.activePart
		state := candidate.parts[partIndex]
		if !mayContinue || state == nil || !state.open || state.kind != kind {
			s.closeActivePart(candidateIndex, eventID)
			partIndex = candidate.nextPart
			candidate.nextPart++
			state = &googlePartState{kind: kind, open: true, wire: part}
			candidate.parts[partIndex] = state
			candidate.activePart = partIndex
			candidate.lastPart = partIndex
			s.queue.Push(models.Event{Kind: models.EventPartStart, CandidateIndex: candidateIndex, PartIndex: partIndex, Part: content, ProviderEventID: eventID})
		}
		if *part.Text != "" {
			state.text.WriteString(*part.Text)
			text := state.text.String()
			state.wire.Text = &text
			s.queue.Push(models.Event{Kind: models.EventPartDelta, CandidateIndex: candidateIndex, PartIndex: partIndex, Delta: models.Delta{Kind: deltaKind, Text: *part.Text}, ProviderEventID: eventID})
		}
		if part.ThoughtSignature != "" {
			state.wire.ThoughtSignature = part.ThoughtSignature
		}
	case part.FunctionCall != nil:
		if part.FunctionCall.Name == "" || !shared.JSONObject(part.FunctionCall.Args) {
			return streamProtocol("invalid_tool_call", "Gemini functionCall has invalid name or args", nil)
		}
		toolKey := fmt.Sprintf("%d/%d", candidateIndex, candidate.nextPart)
		if err := s.toolGuard.Start(toolKey, part.FunctionCall.ID, part.FunctionCall.Name); err != nil {
			return streamProtocol("response_limit", "Gemini tool-call response exceeds configured limit", err)
		}
		if err := s.toolGuard.AddArguments(toolKey, len(part.FunctionCall.Args)); err != nil {
			return streamProtocol("response_limit", "Gemini tool-call response exceeds configured limit", err)
		}
		s.closeActivePart(candidateIndex, eventID)
		partIndex := candidate.nextPart
		candidate.nextPart++
		state := &googlePartState{kind: models.ContentToolCall, open: false, wire: part}
		candidate.parts[partIndex] = state
		candidate.lastPart = partIndex
		s.queue.Push(
			models.Event{Kind: models.EventPartStart, CandidateIndex: candidateIndex, PartIndex: partIndex, Part: models.ToolCallContent(part.FunctionCall.ID, part.FunctionCall.Name, nil), CallID: part.FunctionCall.ID, ProviderEventID: eventID},
			models.Event{Kind: models.EventPartDelta, CandidateIndex: candidateIndex, PartIndex: partIndex, Delta: models.Delta{Kind: models.DeltaToolArguments, Text: string(part.FunctionCall.Args)}, CallID: part.FunctionCall.ID, ProviderEventID: eventID},
			models.Event{Kind: models.EventPartEnd, CandidateIndex: candidateIndex, PartIndex: partIndex, CallID: part.FunctionCall.ID, ProviderEventID: eventID},
		)
	default:
		if part.ThoughtSignature != "" {
			partIndex := candidate.activePart
			if partIndex < 0 {
				partIndex = candidate.lastPart
			}
			if state := candidate.parts[partIndex]; state != nil {
				state.wire.ThoughtSignature = part.ThoughtSignature
			} else {
				partIndex = candidate.nextPart
				candidate.nextPart++
				candidate.parts[partIndex] = &googlePartState{wire: part}
				candidate.lastPart = partIndex
			}
		}
	}
	return nil
}

func (s *googleSource) closeActivePart(candidateIndex int, eventID string) {
	candidate := s.candidate(candidateIndex)
	partIndex := candidate.activePart
	if partIndex < 0 {
		return
	}
	if state := candidate.parts[partIndex]; state != nil && state.open {
		state.open = false
		s.queue.Push(models.Event{Kind: models.EventPartEnd, CandidateIndex: candidateIndex, PartIndex: partIndex, ProviderEventID: eventID})
	}
	candidate.activePart = -1
}

func candidateHasTool(candidate *googleCandidateState) bool {
	for _, part := range candidate.parts {
		if part.kind == models.ContentToolCall {
			return true
		}
	}
	return false
}

func (s *googleSource) hasTerminalEvidence() bool {
	if !s.started {
		return false
	}
	if s.promptBlock != "" && len(s.candidates) == 0 {
		return true
	}
	if len(s.candidates) == 0 {
		// A decoded GenerateContentResponse with no candidates is still a
		// complete empty response. This mirrors unary decoding; a truncated
		// candidate remains distinguishable because it is present without a
		// finish reason.
		return true
	}
	for _, candidate := range s.candidates {
		if candidate.rawFinish == "" {
			return false
		}
	}
	return true
}

func (s *googleSource) complete() error {
	indexes := slices.Sorted(maps.Keys(s.candidates))
	finishes := make([]models.CandidateFinish, 0, len(indexes))
	status := models.ResponseStatusCompleted
	var metadata map[string]string
	for _, index := range indexes {
		candidate := s.candidates[index]
		partIndexes := slices.Sorted(maps.Keys(candidate.parts))
		hasState := false
		content := contentWire{Role: candidate.role}
		for _, partIndex := range partIndexes {
			part := candidate.parts[partIndex]
			if part.open {
				part.open = false
				s.queue.Push(models.Event{Kind: models.EventPartEnd, CandidateIndex: index, PartIndex: partIndex})
			}
			content.Parts = append(content.Parts, part.wire)
			if part.wire.ThoughtSignature != "" {
				hasState = true
			}
		}
		var providerState *models.ProviderState
		if hasState {
			data, _ := json.Marshal(content)
			if int64(len(data)) > s.stateLimit {
				return streamProtocol("provider_state_too_large", "Gemini provider state exceeds configured limit", nil)
			}
			providerState = &models.ProviderState{Provider: protocol, Data: data}
		}
		finishes = append(finishes, models.CandidateFinish{CandidateIndex: index, Reason: candidate.finish, RawReason: candidate.rawFinish, ProviderState: providerState})
		if candidate.finish == models.FinishContentFilter || candidate.finish == models.FinishIncomplete {
			status = models.ResponseStatusIncomplete
		}
	}
	if s.promptBlock != "" && len(finishes) == 0 {
		finishes = []models.CandidateFinish{{CandidateIndex: 0, Reason: models.FinishContentFilter, RawReason: s.promptBlock}}
		status = models.ResponseStatusIncomplete
		metadata = map[string]string{"prompt_block_reason": s.promptBlock}
		if s.promptBlockMessage != "" {
			metadata["prompt_block_message"] = s.promptBlockMessage
		}
	}
	modelName := s.model
	if modelName == "" {
		modelName = s.fallbackModel
	}
	s.queue.Push(models.Event{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "google", ID: s.responseID, Model: modelName, Status: status, RequestID: s.requestID, Metadata: metadata}, Finishes: finishes, Usage: s.usage})
	return nil
}

func (s *googleSource) Close() error {
	if s.reader == nil {
		return nil
	}
	return s.reader.Close()
}

func streamProtocol(code, message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "google", Operation: "stream_next", Code: code, Message: message, Cause: cause}
}

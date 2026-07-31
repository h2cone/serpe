package generatecontent

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
)

type geminiSource struct {
	reader             *sse.Reader
	requestID          string
	fallbackModel      string
	stateLimit         int64
	queue              shared.EventQueue
	candidates         map[int]*geminiCandidateState
	started            bool
	finished           bool
	responseID         string
	model              string
	usage              *models.Usage
	promptBlock        string
	promptBlockMessage string
	closeOnce          sync.Once
	closeErr           error
}

type geminiCandidateState struct {
	parts      map[int]*geminiPartState
	nextPart   int
	activePart int
	lastPart   int
	finish     models.FinishReason
	rawFinish  string
	role       string
}

type geminiPartState struct {
	kind models.ContentKind
	open bool
	wire partWire
	text strings.Builder
}

func newGeminiSource(reader *sse.Reader, requestID, fallbackModel string, stateLimit int64) *geminiSource {
	return &geminiSource{reader: reader, requestID: requestID, fallbackModel: fallbackModel, stateLimit: stateLimit, candidates: make(map[int]*geminiCandidateState)}
}

func (s *geminiSource) Next() (models.Event, error) {
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
				if !s.hasTerminalEvidence() {
					return models.Event{}, io.EOF
				}
				if err := s.complete(); err != nil {
					return models.Event{}, err
				}
				s.finished = true
				continue
			}
			return models.Event{}, streamProtocol("sse_read_error", "failed to parse Gemini SSE event", err)
		}
		if len(wireEvent.Data) == 0 {
			continue
		}
		var response responseWire
		if err := json.Unmarshal(wireEvent.Data, &response); err != nil {
			return models.Event{}, streamProtocol("invalid_json", "Gemini stream event is not valid JSON", err)
		}
		if response.Error != nil {
			return models.Event{}, normalizeWireError(response.Error, "stream_next", s.requestID)
		}
		if err := s.consume(response, wireEvent.ID); err != nil {
			return models.Event{}, err
		}
	}
}

func (s *geminiSource) consume(response responseWire, eventID string) error {
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
		s.queue.Push(models.Event{Kind: models.EventResponseStart, Response: &models.ResponseInfo{Provider: "gemini", ID: s.responseID, Model: modelName, RequestID: s.requestID}, ProviderEventID: eventID})
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

func (s *geminiSource) candidate(index int) *geminiCandidateState {
	state := s.candidates[index]
	if state == nil {
		state = &geminiCandidateState{parts: make(map[int]*geminiPartState), activePart: -1, lastPart: -1, finish: models.FinishUnknown, role: "model"}
		s.candidates[index] = state
	}
	return state
}

func (s *geminiSource) consumePart(candidateIndex int, part partWire, mayContinue bool, eventID string) error {
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
			state = &geminiPartState{kind: kind, open: true, wire: part}
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
		s.closeActivePart(candidateIndex, eventID)
		partIndex := candidate.nextPart
		candidate.nextPart++
		state := &geminiPartState{kind: models.ContentToolCall, open: false, wire: part}
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
				candidate.parts[partIndex] = &geminiPartState{wire: part}
				candidate.lastPart = partIndex
			}
		}
	}
	return nil
}

func (s *geminiSource) closeActivePart(candidateIndex int, eventID string) {
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

func candidateHasTool(candidate *geminiCandidateState) bool {
	for _, part := range candidate.parts {
		if part.kind == models.ContentToolCall {
			return true
		}
	}
	return false
}

func (s *geminiSource) hasTerminalEvidence() bool {
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

func (s *geminiSource) complete() error {
	indexes := make([]int, 0, len(s.candidates))
	for index := range s.candidates {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	finishes := make([]models.CandidateFinish, 0, len(indexes))
	status := models.ResponseStatusCompleted
	var metadata map[string]string
	for _, index := range indexes {
		candidate := s.candidates[index]
		partIndexes := make([]int, 0, len(candidate.parts))
		for partIndex := range candidate.parts {
			partIndexes = append(partIndexes, partIndex)
		}
		sort.Ints(partIndexes)
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
	s.queue.Push(models.Event{Kind: models.EventResponseEnd, Response: &models.ResponseInfo{Provider: "gemini", ID: s.responseID, Model: modelName, Status: status, RequestID: s.requestID, Metadata: metadata}, Finishes: finishes, Usage: s.usage})
	return nil
}

func (s *geminiSource) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.reader.Close() })
	return s.closeErr
}

func streamProtocol(code, message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "gemini", Operation: "stream_next", Code: code, Message: message, Cause: cause}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

package anthropic

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

func decodeResponse(wire messageWire, requestID string, stateLimit int64) (*models.Response, error) {
	if wire.Error != nil {
		return nil, normalizeWireError(wire.Error, "generate", requestID)
	}
	response := &models.Response{Provider: "anthropic", ID: wire.ID, Model: wire.Model, Status: models.ResponseStatusCompleted, RequestID: requestID}
	candidate := models.Candidate{Index: 0, FinishReason: models.FinishUnknown}
	hasState := false
	for _, raw := range wire.Content {
		var block contentWire
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, protocolError("Anthropic content block has an invalid shape", err)
		}
		switch block.Type {
		case "text":
			candidate.Content = append(candidate.Content, models.Text(block.Text))
		case "tool_use":
			input, ok := normalizeToolInput(block.Input)
			if !ok {
				return nil, protocolError("Anthropic tool input is not a JSON object", nil)
			}
			candidate.Content = append(candidate.Content, models.ToolCallContent(block.ID, block.Name, input))
		case "thinking":
			if block.Thinking != "" {
				candidate.Content = append(candidate.Content, models.ReasoningSummary(block.Thinking))
			}
			hasState = true
		case "redacted_thinking":
			hasState = true
		}
	}
	if wire.StopReason != nil {
		candidate.RawFinishReason = *wire.StopReason
		candidate.FinishReason = mapFinish(*wire.StopReason)
	}
	if candidate.FinishReason == models.FinishIncomplete {
		response.Status = models.ResponseStatusIncomplete
	}
	if hasState {
		data, _ := json.Marshal(wire.Content)
		if int64(len(data)) > stateLimit {
			return nil, &models.Error{Kind: models.ErrorProtocol, Provider: "anthropic", Operation: "generate", Code: "provider_state_too_large", Message: "Anthropic provider state exceeds configured limit"}
		}
		candidate.ProviderState = &models.ProviderState{Provider: protocol, Data: data}
	}
	response.Candidates = []models.Candidate{candidate}
	if wire.Usage != nil {
		response.Usage = decodeUsage(wire.Usage)
	}
	return response, nil
}

func decodeUsage(usage *usageWire) models.Usage {
	var result models.Usage
	if usage == nil {
		return result
	}
	inputTokens := int64(0)
	hasInputTokens := false
	for _, tokens := range []*int64{usage.InputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens} {
		if tokens != nil {
			inputTokens += *tokens
			hasInputTokens = true
		}
	}
	if hasInputTokens {
		result.InputTokens = models.Some(inputTokens)
	}
	if usage.OutputTokens != nil {
		result.OutputTokens = models.Some(*usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != nil {
		result.CachedInputTokens = models.Some(*usage.CacheReadInputTokens)
	}
	if hasInputTokens && usage.OutputTokens != nil {
		result.TotalTokens = models.Some(inputTokens + *usage.OutputTokens)
	}
	result.Raw, _ = json.Marshal(usage)
	return result
}

func mapFinish(raw string) models.FinishReason {
	switch raw {
	case "end_turn", "stop_sequence":
		return models.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		return models.FinishLength
	case "tool_use":
		return models.FinishToolCall
	case "refusal":
		return models.FinishContentFilter
	case "pause_turn":
		return models.FinishIncomplete
	case "cancelled", "canceled":
		return models.FinishCancelled
	default:
		return models.FinishUnknown
	}
}

func normalizeToolInput(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}"), true
	}
	if !shared.JSONObject(trimmed) {
		return nil, false
	}
	return append(json.RawMessage(nil), raw...), true
}

func protocolError(message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "anthropic", Operation: "generate", Code: "invalid_response", Message: message, Cause: cause}
}

func normalizeWireError(wire *errorWire, operation, requestID string) *models.Error {
	lower := strings.ToLower(wire.Type)
	kind := models.ErrorUnknown
	retryable := false
	switch {
	case strings.Contains(lower, "rate_limit"):
		kind, retryable = models.ErrorRateLimited, true
	case strings.Contains(lower, "authentication"):
		kind = models.ErrorAuthentication
	case strings.Contains(lower, "permission"):
		kind = models.ErrorPermission
	case strings.Contains(lower, "not_found"):
		kind = models.ErrorNotFound
	case strings.Contains(lower, "invalid_request"):
		kind = models.ErrorInvalidRequest
	case strings.Contains(lower, "timeout"):
		kind, retryable = models.ErrorTimeout, true
	case strings.Contains(lower, "overloaded") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "api_error"):
		kind, retryable = models.ErrorUnavailable, true
	}
	return &models.Error{Kind: kind, Provider: "anthropic", Operation: operation, Code: wire.Type, Message: wire.Message, RequestID: requestID, Retryable: retryable}
}

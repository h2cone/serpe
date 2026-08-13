package responses

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/h2cone/serpe/core/models"
	openaicommon "github.com/h2cone/serpe/core/providers/internal/protocol/openai/internal/common"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

func decodeResponse(wire responseWire, requestID string, stateLimit int64, guard *shared.ToolCallGuard) (*models.Response, error) {
	if wire.Error != nil {
		return nil, normalizeWireError(wire.Error, "generate", requestID)
	}
	response := &models.Response{Provider: "openai", ID: wire.ID, Model: wire.Model, Status: mapStatus(wire.Status), RequestID: requestID}
	if wire.CreatedAt != 0 {
		response.CreatedAt = time.Unix(wire.CreatedAt, 0).UTC()
	}
	candidate := models.Candidate{Index: 0}
	hasState := false
	hasToolCall := false
	for outputIndex, raw := range wire.Output {
		var header itemHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, protocolError("response output item has an invalid shape", err)
		}
		switch header.Type {
		case "message":
			var item messageItemWire
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, protocolError("response message item has an invalid shape", err)
			}
			for _, partRaw := range item.Content {
				var part contentPartWire
				if err := json.Unmarshal(partRaw, &part); err != nil {
					return nil, protocolError("response content part has an invalid shape", err)
				}
				switch part.Type {
				case "output_text":
					candidate.Content = append(candidate.Content, models.Text(part.Text))
				case "refusal":
					candidate.Content = append(candidate.Content, models.Refusal(part.Refusal))
				}
			}
		case "function_call":
			var item functionCallWire
			if err := json.Unmarshal(raw, &item); err != nil || !shared.JSONObject([]byte(item.Arguments)) {
				return nil, protocolError("response function call has invalid arguments", err)
			}
			key := strconv.Itoa(outputIndex)
			if err := guard.Start(key, item.CallID, item.Name); err != nil {
				return nil, responseDecodeLimit(err)
			}
			if err := guard.AddArguments(key, len(item.Arguments)); err != nil {
				return nil, responseDecodeLimit(err)
			}
			candidate.Content = append(candidate.Content, models.ToolCallContent(item.CallID, item.Name, json.RawMessage(item.Arguments)))
			hasToolCall = true
		case "reasoning":
			var item reasoningItemWire
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, protocolError("response reasoning item has an invalid shape", err)
			}
			for _, summary := range item.Summary {
				if summary.Type == "summary_text" || summary.Type == "text" {
					candidate.Content = append(candidate.Content, models.ReasoningSummary(summary.Text))
				}
			}
			for _, part := range item.Content {
				if part.Type == "reasoning_text" || part.Type == "text" {
					candidate.Content = append(candidate.Content, models.ReasoningSummary(part.Text))
				}
			}
			hasState = true
		}
	}
	rawFinish := wire.Status
	if wire.IncompleteDetails != nil && wire.IncompleteDetails.Reason != "" {
		rawFinish = wire.IncompleteDetails.Reason
	}
	candidate.RawFinishReason = rawFinish
	candidate.FinishReason = responsesFinish(wire.Status, rawFinish, hasToolCall)
	if hasState {
		stateData, _ := json.Marshal(wire.Output)
		if int64(len(stateData)) > stateLimit {
			return nil, &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "generate", Code: "provider_state_too_large", Message: "Responses provider state exceeds configured limit"}
		}
		candidate.ProviderState = &models.ProviderState{Provider: protocol, Data: stateData}
	}
	if len(candidate.Content) > 0 || len(wire.Output) > 0 {
		response.Candidates = []models.Candidate{candidate}
	}
	if wire.Usage != nil {
		response.Usage = decodeUsage(wire.Usage)
	}
	return response, nil
}

func responseDecodeLimit(cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "generate", Code: "response_limit", Message: "Responses tool-call response exceeds configured limit", Cause: cause}
}

func decodeUsage(usage *usageWire) models.Usage {
	if usage == nil {
		return models.Usage{}
	}
	result := models.Usage{
		InputTokens:       shared.OptionalValue(usage.InputTokens),
		OutputTokens:      shared.OptionalValue(usage.OutputTokens),
		TotalTokens:       shared.OptionalValue(usage.TotalTokens),
		CachedInputTokens: shared.OptionalValue(usage.InputTokensDetails.CachedTokens),
		ReasoningTokens:   shared.OptionalValue(usage.OutputTokensDetails.ReasoningTokens),
	}
	result.Raw, _ = json.Marshal(usage)
	return result
}

func mapStatus(status string) models.ResponseStatus {
	switch status {
	case "completed":
		return models.ResponseStatusCompleted
	case "incomplete", "in_progress", "queued":
		return models.ResponseStatusIncomplete
	case "failed":
		return models.ResponseStatusFailed
	case "cancelled", "canceled":
		return models.ResponseStatusCancelled
	default:
		return models.ResponseStatusIncomplete
	}
}

func responsesFinish(status, raw string, hasTool bool) models.FinishReason {
	if hasTool && status == "completed" {
		return models.FinishToolCall
	}
	switch raw {
	case "completed", "stop":
		return models.FinishStop
	case "max_output_tokens", "length":
		return models.FinishLength
	case "content_filter":
		return models.FinishContentFilter
	case "cancelled", "canceled":
		return models.FinishCancelled
	case "failed":
		return models.FinishError
	case "incomplete":
		return models.FinishIncomplete
	default:
		if status == "incomplete" {
			return models.FinishIncomplete
		}
		return models.FinishUnknown
	}
}

func protocolError(message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "generate", Code: "invalid_response", Message: message, Cause: cause}
}

func normalizeWireError(wire *errorWire, operation, requestID string) *models.Error {
	return openaicommon.NormalizeError(wire.Code, wire.Type, wire.Message, operation, requestID)
}

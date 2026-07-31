package generatecontent

import (
	"encoding/json"
	"strconv"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

func decodeResponse(wire responseWire, requestID, fallbackModel string, stateLimit int64) (*models.Response, error) {
	if wire.Error != nil {
		return nil, normalizeWireError(wire.Error, "generate", requestID)
	}
	modelName := wire.ModelVersion
	if modelName == "" {
		modelName = fallbackModel
	}
	response := &models.Response{Provider: "gemini", ID: wire.ResponseID, Model: modelName, Status: models.ResponseStatusCompleted, RequestID: requestID}
	for position, wireCandidate := range wire.Candidates {
		index := position
		if wireCandidate.Index != nil {
			index = *wireCandidate.Index
		}
		candidate := models.Candidate{Index: index, RawFinishReason: wireCandidate.FinishReason, FinishReason: mapFinish(wireCandidate.FinishReason)}
		hasTool := false
		hasState := false
		for _, part := range wireCandidate.Content.Parts {
			switch {
			case part.Text != nil && part.Thought:
				if *part.Text != "" {
					candidate.Content = append(candidate.Content, models.ReasoningSummary(*part.Text))
				}
			case part.Text != nil:
				if *part.Text != "" {
					candidate.Content = append(candidate.Content, models.Text(*part.Text))
				}
			case part.FunctionCall != nil:
				if !shared.JSONObject(part.FunctionCall.Args) {
					return nil, protocolError("Gemini functionCall args are not a JSON object", nil)
				}
				candidate.Content = append(candidate.Content, models.ToolCallContent(part.FunctionCall.ID, part.FunctionCall.Name, part.FunctionCall.Args))
				hasTool = true
			}
			if part.ThoughtSignature != "" {
				hasState = true
			}
		}
		if hasTool && candidate.FinishReason == models.FinishStop {
			candidate.FinishReason = models.FinishToolCall
		}
		if hasState {
			stateData, _ := json.Marshal(wireCandidate.Content)
			if int64(len(stateData)) > stateLimit {
				return nil, &models.Error{Kind: models.ErrorProtocol, Provider: "gemini", Operation: "generate", Code: "provider_state_too_large", Message: "Gemini provider state exceeds configured limit"}
			}
			candidate.ProviderState = &models.ProviderState{Provider: protocol, Data: stateData}
		}
		response.Candidates = append(response.Candidates, candidate)
	}
	if len(response.Candidates) == 0 && wire.PromptFeedback != nil && wire.PromptFeedback.BlockReason != "" {
		response.Status = models.ResponseStatusIncomplete
		response.Candidates = []models.Candidate{{Index: 0, FinishReason: models.FinishContentFilter, RawFinishReason: wire.PromptFeedback.BlockReason}}
		response.Metadata = map[string]string{"prompt_block_reason": wire.PromptFeedback.BlockReason}
		if wire.PromptFeedback.BlockReasonMessage != "" {
			response.Metadata["prompt_block_message"] = wire.PromptFeedback.BlockReasonMessage
		}
	}
	if wire.UsageMetadata != nil {
		response.Usage = decodeUsage(wire.UsageMetadata)
	}
	return response, nil
}

func decodeUsage(usage *usageMetadataWire) models.Usage {
	var result models.Usage
	if usage == nil {
		return result
	}
	if usage.PromptTokenCount != nil {
		result.InputTokens = models.Some(*usage.PromptTokenCount)
	}
	if usage.CandidatesTokenCount != nil {
		result.OutputTokens = models.Some(*usage.CandidatesTokenCount)
	}
	if usage.TotalTokenCount != nil {
		result.TotalTokens = models.Some(*usage.TotalTokenCount)
	}
	if usage.CachedContentTokenCount != nil {
		result.CachedInputTokens = models.Some(*usage.CachedContentTokenCount)
	}
	if usage.ThoughtsTokenCount != nil {
		result.ReasoningTokens = models.Some(*usage.ThoughtsTokenCount)
	}
	if usage.ToolUsePromptTokenCount != nil {
		result.ToolUseTokens = models.Some(*usage.ToolUsePromptTokenCount)
	}
	result.Raw, _ = json.Marshal(usage)
	return result
}

func mapFinish(raw string) models.FinishReason {
	switch raw {
	case "STOP":
		return models.FinishStop
	case "MAX_TOKENS":
		return models.FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return models.FinishContentFilter
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return models.FinishError
	case "CANCELLED", "CANCELED":
		return models.FinishCancelled
	case "":
		return models.FinishUnknown
	default:
		return models.FinishUnknown
	}
}

func protocolError(message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "gemini", Operation: "generate", Code: "invalid_response", Message: message, Cause: cause}
}

func errorCode(code int) string {
	if code == 0 {
		return ""
	}
	return strconv.Itoa(code)
}

func normalizeWireError(wire *errorWire, operation, requestID string) *models.Error {
	kind := models.ErrorUnknown
	retryable := false
	switch wire.Code {
	case 400, 422:
		kind = models.ErrorInvalidRequest
	case 401:
		kind = models.ErrorAuthentication
	case 403:
		kind = models.ErrorPermission
	case 404:
		kind = models.ErrorNotFound
	case 409:
		kind = models.ErrorConflict
	case 429:
		kind, retryable = models.ErrorRateLimited, true
	case 408, 504:
		kind, retryable = models.ErrorTimeout, true
	case 500, 502, 503:
		kind, retryable = models.ErrorUnavailable, true
	}
	return &models.Error{Kind: kind, Provider: "gemini", Operation: operation, HTTPStatus: wire.Code, Code: firstNonempty(wire.Status, errorCode(wire.Code)), Message: wire.Message, RequestID: requestID, Retryable: retryable}
}

package chatcompletions

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/h2cone/ouro/core/models"
	openaicommon "github.com/h2cone/ouro/core/providers/internal/openai/common"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

func decodeResponse(wire chatResponse, requestID string) (*models.Response, error) {
	if wire.Error != nil {
		return nil, normalizeWireError(wire.Error, "generate", requestID)
	}
	response := &models.Response{Provider: "openai", ID: wire.ID, Model: wire.Model, Status: models.ResponseStatusCompleted, RequestID: requestID}
	if wire.Created != 0 {
		response.CreatedAt = time.Unix(wire.Created, 0).UTC()
	}
	for _, choice := range wire.Choices {
		candidate := models.Candidate{Index: choice.Index, FinishReason: models.FinishUnknown}
		if choice.FinishReason != nil {
			candidate.RawFinishReason = *choice.FinishReason
			candidate.FinishReason = mapFinish(*choice.FinishReason)
		}
		content, err := decodeMessage(choice.Message)
		if err != nil {
			return nil, err
		}
		candidate.Content = content
		response.Candidates = append(response.Candidates, candidate)
	}
	if wire.Usage != nil {
		response.Usage = decodeUsage(wire.Usage)
	}
	return response, nil
}

func decodeMessage(message chatResponseMessage) ([]models.Content, error) {
	content, err := decodeTextContent(message.Content)
	if err != nil {
		return nil, err
	}
	if message.Refusal != nil && *message.Refusal != "" {
		content = append(content, models.Refusal(*message.Refusal))
	}
	for _, call := range message.ToolCalls {
		arguments := json.RawMessage(call.Function.Arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage("{}")
		}
		if !shared.JSONObject(arguments) {
			return nil, protocolError("tool call arguments are not a JSON object", nil)
		}
		content = append(content, models.ToolCallContent(call.ID, call.Function.Name, arguments))
	}
	if message.FunctionCall != nil {
		arguments := json.RawMessage(message.FunctionCall.Arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage("{}")
		}
		if !shared.JSONObject(arguments) {
			return nil, protocolError("deprecated function_call arguments are not a JSON object", nil)
		}
		content = append(content, models.ToolCallContent("", message.FunctionCall.Name, arguments))
	}
	return content, nil
}

func decodeTextContent(raw json.RawMessage) ([]models.Content, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []models.Content{models.Text(text)}, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, protocolError("assistant content has an unknown shape", err)
	}
	output := make([]models.Content, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" || part.Type == "output_text" {
			output = append(output, models.Text(part.Text))
		}
	}
	return output, nil
}

func decodeUsage(usage *chatUsage) models.Usage {
	var normalized models.Usage
	if usage == nil {
		return normalized
	}
	if usage.PromptTokens != nil {
		normalized.InputTokens = models.Some(*usage.PromptTokens)
	}
	if usage.CompletionTokens != nil {
		normalized.OutputTokens = models.Some(*usage.CompletionTokens)
	}
	if usage.TotalTokens != nil {
		normalized.TotalTokens = models.Some(*usage.TotalTokens)
	}
	if usage.PromptTokensDetails.CachedTokens != nil {
		normalized.CachedInputTokens = models.Some(*usage.PromptTokensDetails.CachedTokens)
	}
	if usage.CompletionTokensDetails.ReasoningTokens != nil {
		normalized.ReasoningTokens = models.Some(*usage.CompletionTokensDetails.ReasoningTokens)
	}
	normalized.Raw, _ = json.Marshal(usage)
	return normalized
}

func mapFinish(raw string) models.FinishReason {
	switch raw {
	case "stop":
		return models.FinishStop
	case "length":
		return models.FinishLength
	case "tool_calls", "function_call":
		return models.FinishToolCall
	case "content_filter":
		return models.FinishContentFilter
	case "cancelled", "canceled":
		return models.FinishCancelled
	default:
		return models.FinishUnknown
	}
}

func protocolError(message string, cause error) error {
	return &models.Error{Kind: models.ErrorProtocol, Provider: "openai", Operation: "generate", Code: "invalid_response", Message: message, Cause: cause}
}

func normalizeWireError(wire *wireError, operation, requestID string) *models.Error {
	return openaicommon.NormalizeError(wire.Code, wire.Type, wire.Message, operation, requestID)
}

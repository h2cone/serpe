package google

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

// EncodeRequest encodes a canonical request for Gemini GenerateContent.
func EncodeRequest(req *models.Request, lenient bool, stateLimit int64) ([]byte, error) {
	wire := requestWire{}
	if len(req.Instructions) > 0 {
		merged, requiresLenient := shared.MergeInstructions(req.Instructions, lenient)
		if requiresLenient {
			return nil, unsupported("Gemini has no independent developer instruction layer; enable lenient mapping for deterministic merging")
		}
		wire.SystemInstruction = &contentWire{Role: "system", Parts: []partWire{{Text: &merged}}}
	}
	for _, message := range req.Messages {
		content, err := encodeMessage(message, lenient, stateLimit)
		if err != nil {
			return nil, err
		}
		wire.Contents = append(wire.Contents, content)
	}
	if len(req.Tools) > 0 {
		container := toolContainer{}
		for _, tool := range req.Tools {
			if tool.Strict.Set && !tool.Strict.Value {
				return nil, unsupported("Gemini does not map non-strict function schemas")
			}
			container.FunctionDeclarations = append(container.FunctionDeclarations, functionDeclaration{Name: tool.Name, Description: tool.Description, Parameters: append(json.RawMessage(nil), tool.Parameters...)})
		}
		wire.Tools = []toolContainer{container}
	}
	if req.ToolChoice.Kind != "" {
		config := &toolConfigWire{}
		switch req.ToolChoice.Kind {
		case models.ToolChoiceAuto:
			config.FunctionCallingConfig.Mode = "AUTO"
		case models.ToolChoiceNone:
			config.FunctionCallingConfig.Mode = "NONE"
		case models.ToolChoiceRequired:
			config.FunctionCallingConfig.Mode = "ANY"
		case models.ToolChoiceFunction:
			config.FunctionCallingConfig.Mode = "ANY"
			config.FunctionCallingConfig.AllowedFunctionNames = []string{req.ToolChoice.Name}
		default:
			return nil, unsupported("unknown tool choice")
		}
		wire.ToolConfig = config
	}
	generation := &generationConfig{
		MaxOutputTokens: req.Generation.MaxOutputTokens.Pointer(),
		Temperature:     req.Generation.Temperature.Pointer(),
		TopP:            req.Generation.TopP.Pointer(),
		StopSequences:   req.Generation.Stop,
		Seed:            req.Generation.Seed.Pointer(),
		CandidateCount:  req.Generation.CandidateCount.Pointer(),
	}
	hasGeneration := req.Generation.MaxOutputTokens.Set || req.Generation.Temperature.Set ||
		req.Generation.TopP.Set || len(req.Generation.Stop) > 0 || req.Generation.Seed.Set || req.Generation.CandidateCount.Set
	switch req.ResponseFormat.Kind {
	case "", models.ResponseFormatText:
	case models.ResponseFormatJSONObject:
		generation.ResponseMIMEType = "application/json"
		hasGeneration = true
	case models.ResponseFormatJSONSchema:
		generation.ResponseMIMEType = "application/json"
		generation.ResponseSchema = append(json.RawMessage(nil), req.ResponseFormat.Schema...)
		hasGeneration = true
	default:
		return nil, unsupported("unknown response format")
	}
	if hasGeneration {
		wire.GenerationConfig = generation
	}
	encoded, err := shared.EncodeJSON(wire)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "google", Operation: "encode", Message: "failed to encode Gemini request", Cause: err}
	}
	return shared.MergeExtension(encoded, req.Extensions, protocol,
		"systemInstruction", "contents", "tools", "toolConfig", "generationConfig")
}

func encodeMessage(message models.Message, lenient bool, stateLimit int64) (contentWire, error) {
	accepted, err := shared.ValidateProviderState(message.ProviderState, protocol, stateLimit, lenient)
	if err != nil {
		return contentWire{}, err
	}
	if accepted {
		var state contentWire
		if err := shared.DecodeJSON(message.ProviderState.Data, &state); err != nil || state.Role != "model" || len(state.Parts) == 0 {
			return contentWire{}, invalidState("Gemini provider state must be a model Content object", err)
		}
		projected, decodeErr := decodeResponse(responseWire{Candidates: []candidateWire{{Content: state, FinishReason: "STOP"}}}, "", "", stateLimit, shared.NewToolCallGuard(shared.DefaultToolCallLimits()))
		if decodeErr != nil || len(projected.Candidates) != 1 || !shared.EquivalentContent(projected.Candidates[0].Content, message.Content) {
			return contentWire{}, invalidState("Gemini provider state does not match assistant content", decodeErr)
		}
		return state, nil
	}
	role := "user"
	if message.Role == models.RoleAssistant {
		role = "model"
	}
	result := contentWire{Role: role}
	for _, block := range message.Content {
		switch block.Kind {
		case models.ContentText:
			if block.Text.Text == "" && lenient {
				continue
			}
			text := block.Text.Text
			result.Parts = append(result.Parts, partWire{Text: &text})
		case models.ContentImage:
			if message.Role != models.RoleUser {
				return contentWire{}, unsupported("Gemini image input is only valid in user messages")
			}
			if block.Image.URI != "" {
				result.Parts = append(result.Parts, partWire{FileData: &fileDataWire{FileURI: block.Image.URI}})
			} else {
				result.Parts = append(result.Parts, partWire{InlineData: &inlineDataWire{MIMEType: block.Image.MIMEType, Data: base64.StdEncoding.EncodeToString(block.Image.Data)}})
			}
		case models.ContentToolCall:
			if message.Role != models.RoleAssistant {
				return contentWire{}, unsupported("tool calls are only valid in assistant messages")
			}
			result.Parts = append(result.Parts, partWire{FunctionCall: &functionCallWire{ID: block.ToolCall.ID, Name: block.ToolCall.Name, Args: append(json.RawMessage(nil), block.ToolCall.Arguments...)}})
		case models.ContentToolResult:
			if message.Role != models.RoleUser {
				return contentWire{}, unsupported("Gemini tool results must be in user messages")
			}
			var text strings.Builder
			var media []functionResponsePartWire
			for _, child := range block.ToolResult.Content {
				switch child.Kind {
				case models.ContentText:
					text.WriteString(child.Text.Text)
				case models.ContentImage:
					if child.Image.URI != "" {
						return contentWire{}, unsupported("Gemini tool-result images must contain inline bytes")
					}
					if child.Image.Detail != "" {
						return contentWire{}, unsupported("Gemini tool-result images do not support detail hints")
					}
					media = append(media, functionResponsePartWire{InlineData: &functionResponseBlobWire{
						MIMEType: child.Image.MIMEType,
						Data:     base64.StdEncoding.EncodeToString(child.Image.Data),
					}})
				default:
					return contentWire{}, unsupported("Gemini tool results support text and inline image content only")
				}
			}
			response := json.RawMessage(text.String())
			if !shared.JSONObject(response) {
				response, _ = shared.EncodeJSON(struct {
					Result  string `json:"result"`
					IsError bool   `json:"isError,omitzero"`
				}{Result: text.String(), IsError: block.ToolResult.IsError})
			}
			result.Parts = append(result.Parts, partWire{FunctionResponse: &functionResponseWire{ID: block.ToolResult.CallID, Name: block.ToolResult.Name, Response: response, Parts: media}})
		case models.ContentReasoningSummary:
			return contentWire{}, unsupported("Gemini reasoning summaries require same-protocol provider state")
		case models.ContentRefusal:
			text := block.Refusal.Text
			result.Parts = append(result.Parts, partWire{Text: &text})
		default:
			return contentWire{}, unsupported(fmt.Sprintf("content kind %q cannot be encoded by Gemini", block.Kind))
		}
	}
	if len(result.Parts) == 0 {
		return contentWire{}, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "google", Operation: "encode", Message: "Gemini content became empty after mapping"}
	}
	return result, nil
}

func unsupported(message string) error {
	return &models.Error{Kind: models.ErrorUnsupportedFeature, Provider: "google", Operation: "encode", Message: message}
}

func invalidState(message string, cause error) error {
	return &models.Error{Kind: models.ErrorInvalidRequest, Provider: "google", Operation: "encode", Code: "invalid_provider_state", Message: message, Cause: cause}
}

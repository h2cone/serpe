package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

// EncodeRequest encodes a canonical request for Anthropic Messages.
func EncodeRequest(modelID string, req *models.Request, stream, lenient bool, stateLimit int64) ([]byte, error) {
	if !req.Generation.MaxOutputTokens.Set {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "encode", Code: "max_tokens_required", Message: "Anthropic Messages requires max output tokens"}
	}
	wire := requestWire{
		Model: modelID, MaxTokens: req.Generation.MaxOutputTokens.Value, Stream: stream,
		Temperature:   shared.OptionalPointer(req.Generation.Temperature),
		TopP:          shared.OptionalPointer(req.Generation.TopP),
		StopSequences: req.Generation.Stop,
	}
	if len(req.Instructions) > 0 {
		merged, requiresLenient := shared.MergeInstructions(req.Instructions, lenient)
		if requiresLenient {
			return nil, unsupported("Anthropic has no independent developer instruction layer; enable lenient mapping for deterministic merging")
		}
		wire.System = []contentWire{{Type: "text", Text: merged}}
	}
	for _, message := range req.Messages {
		encoded, err := encodeMessage(message, lenient, stateLimit)
		if err != nil {
			return nil, err
		}
		wire.Messages = append(wire.Messages, requestMessage{Role: string(message.Role), Content: encoded})
	}
	for _, tool := range req.Tools {
		if tool.Strict.Set && !tool.Strict.Value {
			return nil, unsupported("Anthropic does not map non-strict function schemas")
		}
		wire.Tools = append(wire.Tools, toolWire{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.Parameters...)})
	}
	choice, err := encodeToolChoice(req.ToolChoice)
	if err != nil {
		return nil, err
	}
	wire.ToolChoice = choice
	if req.ResponseFormat.Kind != "" {
		if req.ResponseFormat.Kind != models.ResponseFormatJSONSchema {
			return nil, unsupported("Anthropic structured output requires JSON Schema")
		}
		config := &outputConfig{}
		config.Format.Type = "json_schema"
		config.Format.Schema = append(json.RawMessage(nil), req.ResponseFormat.Schema...)
		wire.OutputConfig = config
	}
	if req.Generation.Seed.Set || req.Generation.CandidateCount.Set {
		return nil, unsupported("Anthropic does not safely map seed or candidate count")
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "encode", Message: "failed to encode Anthropic request", Cause: err}
	}
	return shared.MergeExtension(encoded, req.Extensions, protocol,
		"model", "max_tokens", "system", "messages", "tools", "tool_choice", "output_config", "temperature", "top_p", "stop_sequences", "stream")
}

func encodeMessage(message models.Message, lenient bool, stateLimit int64) ([]json.RawMessage, error) {
	var output []json.RawMessage
	accepted, err := shared.ValidateProviderState(message.ProviderState, protocol, stateLimit, lenient)
	if err != nil {
		return nil, err
	}
	if accepted {
		var rawBlocks []json.RawMessage
		if err := json.Unmarshal(message.ProviderState.Data, &rawBlocks); err != nil {
			return nil, invalidState("Anthropic provider state must be an array of thinking blocks", err)
		}
		for _, raw := range rawBlocks {
			var header struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &header); err != nil || (header.Type != "thinking" && header.Type != "redacted_thinking" && header.Type != "text" && header.Type != "tool_use") {
				return nil, invalidState("Anthropic provider state contains an unsupported content block", err)
			}
			output = append(output, append(json.RawMessage(nil), raw...))
		}
		stop := "end_turn"
		projected, decodeErr := decodeResponse(messageWire{Content: rawBlocks, StopReason: &stop}, "", stateLimit)
		if decodeErr != nil || len(projected.Candidates) != 1 || !shared.EquivalentContent(projected.Candidates[0].Content, message.Content) {
			return nil, invalidState("Anthropic provider state does not match assistant content", decodeErr)
		}
		return output, nil
	}
	for _, block := range message.Content {
		var mapped contentWire
		switch block.Kind {
		case models.ContentText:
			if block.Text.Text == "" && lenient {
				continue
			}
			mapped = contentWire{Type: "text", Text: block.Text.Text}
		case models.ContentImage:
			if message.Role != models.RoleUser {
				return nil, unsupported("Anthropic image input is only valid in user messages")
			}
			source := &imageSource{}
			if block.Image.URI != "" {
				source.Type = "url"
				source.URL = block.Image.URI
			} else {
				source.Type = "base64"
				source.MediaType = block.Image.MIMEType
				source.Data = base64.StdEncoding.EncodeToString(block.Image.Data)
			}
			mapped = contentWire{Type: "image", Source: source}
		case models.ContentToolCall:
			if message.Role != models.RoleAssistant {
				return nil, unsupported("tool calls are only valid in assistant messages")
			}
			mapped = contentWire{Type: "tool_use", ID: block.ToolCall.ID, Name: block.ToolCall.Name, Input: append(json.RawMessage(nil), block.ToolCall.Arguments...)}
		case models.ContentToolResult:
			if message.Role != models.RoleUser {
				return nil, unsupported("Anthropic tool results must be in user messages")
			}
			children := make([]contentWire, 0, len(block.ToolResult.Content))
			for _, child := range block.ToolResult.Content {
				switch child.Kind {
				case models.ContentText:
					children = append(children, contentWire{Type: "text", Text: child.Text.Text})
				case models.ContentImage:
					if child.Image.URI != "" {
						children = append(children, contentWire{Type: "image", Source: &imageSource{Type: "url", URL: child.Image.URI}})
					} else {
						children = append(children, contentWire{Type: "image", Source: &imageSource{Type: "base64", MediaType: child.Image.MIMEType, Data: base64.StdEncoding.EncodeToString(child.Image.Data)}})
					}
				}
			}
			mapped = contentWire{Type: "tool_result", ToolUseID: block.ToolResult.CallID, Content: children, IsError: block.ToolResult.IsError}
		case models.ContentReasoningSummary:
			return nil, unsupported("reasoning summaries require same-protocol Anthropic provider state")
		case models.ContentRefusal:
			mapped = contentWire{Type: "text", Text: block.Refusal.Text}
		default:
			return nil, unsupported(fmt.Sprintf("content kind %q cannot be encoded by Anthropic", block.Kind))
		}
		raw, err := json.Marshal(mapped)
		if err != nil {
			return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "encode", Message: "failed to encode Anthropic message content", Cause: err}
		}
		output = append(output, raw)
	}
	if len(output) == 0 {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "encode", Message: "Anthropic message became empty after mapping"}
	}
	return output, nil
}

func encodeToolChoice(choice models.ToolChoice) (json.RawMessage, error) {
	switch choice.Kind {
	case "":
		return nil, nil
	case models.ToolChoiceAuto:
		return json.Marshal(struct {
			Type string `json:"type"`
		}{Type: "auto"})
	case models.ToolChoiceNone:
		return json.Marshal(struct {
			Type string `json:"type"`
		}{Type: "none"})
	case models.ToolChoiceRequired:
		return json.Marshal(struct {
			Type string `json:"type"`
		}{Type: "any"})
	case models.ToolChoiceFunction:
		return json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "tool", Name: choice.Name})
	default:
		return nil, unsupported("unknown tool choice")
	}
}

func unsupported(message string) error {
	return &models.Error{Kind: models.ErrorUnsupportedFeature, Provider: "anthropic", Operation: "encode", Message: message}
}

func invalidState(message string, cause error) error {
	return &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "encode", Code: "invalid_provider_state", Message: message, Cause: cause}
}

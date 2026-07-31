package responses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

func encodeRequest(modelID string, req *models.Request, stream, lenient bool, stateLimit int64) ([]byte, error) {
	wire := requestWire{Model: modelID, Stream: stream}
	for _, instruction := range req.Instructions {
		content, err := marshalJSON(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "input_text", Text: instruction.Text})
		if err != nil {
			return nil, err
		}
		item, err := marshalJSON(messageItemWire{Type: "message", Role: string(instruction.Role), Content: []json.RawMessage{content}})
		if err != nil {
			return nil, err
		}
		wire.Input = append(wire.Input, item)
	}
	for _, message := range req.Messages {
		items, err := encodeMessage(message, lenient, stateLimit)
		if err != nil {
			return nil, err
		}
		wire.Input = append(wire.Input, items...)
	}
	for _, tool := range req.Tools {
		encoded := toolWire{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: append(json.RawMessage(nil), tool.Parameters...)}
		if tool.Strict.Set {
			strict := tool.Strict.Value
			encoded.Strict = &strict
		}
		wire.Tools = append(wire.Tools, encoded)
	}
	choice, err := encodeToolChoice(req.ToolChoice)
	if err != nil {
		return nil, err
	}
	wire.ToolChoice = choice
	format, err := encodeTextFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	if format != nil {
		wire.Text = &textConfigWire{Format: format}
	}
	if req.Generation.MaxOutputTokens.Set {
		value := req.Generation.MaxOutputTokens.Value
		wire.MaxOutputTokens = &value
	}
	if req.Generation.Temperature.Set {
		value := req.Generation.Temperature.Value
		wire.Temperature = &value
	}
	if req.Generation.TopP.Set {
		value := req.Generation.TopP.Value
		wire.TopP = &value
	}
	if len(req.Generation.Stop) > 0 || req.Generation.Seed.Set || req.Generation.CandidateCount.Set {
		return nil, unsupported("Responses does not safely map stop, seed, or candidate count")
	}
	encoded, err := marshalJSON(wire)
	if err != nil {
		return nil, err
	}
	return shared.MergeExtension(encoded, req.Extensions, protocol,
		"model", "input", "tools", "tool_choice", "text", "max_output_tokens", "temperature", "top_p", "stream")
}

func encodeMessage(message models.Message, lenient bool, stateLimit int64) ([]json.RawMessage, error) {
	var output []json.RawMessage
	if message.ProviderState != nil {
		accepted, err := shared.ValidateProviderState(message.ProviderState, protocol, stateLimit, lenient)
		if err != nil {
			return nil, err
		}
		if accepted {
			var items []json.RawMessage
			if err := json.Unmarshal(message.ProviderState.Data, &items); err != nil {
				return nil, invalidState("Responses provider state must be an array of output items", err)
			}
			for _, item := range items {
				var header itemHeader
				if json.Unmarshal(item, &header) != nil || (header.Type != "reasoning" && header.Type != "message" && header.Type != "function_call") {
					return nil, invalidState("Responses provider state contains an unsupported output item", nil)
				}
			}
			projected, decodeErr := decodeResponse(responseWire{Status: "completed", Output: items}, "", stateLimit)
			if decodeErr != nil || len(projected.Candidates) != 1 || !shared.EquivalentContent(projected.Candidates[0].Content, message.Content) {
				return nil, invalidState("Responses provider state does not match assistant content", decodeErr)
			}
			output = make([]json.RawMessage, len(items))
			for index := range items {
				output[index] = append(json.RawMessage(nil), items[index]...)
			}
			return output, nil
		}
	}
	var content []json.RawMessage
	appendContent := func(value any) error {
		encoded, err := marshalJSON(value)
		if err != nil {
			return err
		}
		content = append(content, encoded)
		return nil
	}
	appendOutput := func(value any) error {
		encoded, err := marshalJSON(value)
		if err != nil {
			return err
		}
		output = append(output, encoded)
		return nil
	}
	flush := func() error {
		if len(content) == 0 {
			return nil
		}
		if err := appendOutput(messageItemWire{Type: "message", Role: string(message.Role), Content: content}); err != nil {
			return err
		}
		content = nil
		return nil
	}
	for _, block := range message.Content {
		switch block.Kind {
		case models.ContentText:
			typeName := "input_text"
			if message.Role == models.RoleAssistant {
				typeName = "output_text"
			}
			if err := appendContent(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: typeName, Text: block.Text.Text}); err != nil {
				return nil, err
			}
		case models.ContentImage:
			if message.Role != models.RoleUser {
				return nil, unsupported("Responses image input is only valid in user messages")
			}
			uri := block.Image.URI
			if uri == "" {
				uri = "data:" + block.Image.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(block.Image.Data)
			}
			if err := appendContent(struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
				Detail   string `json:"detail,omitempty"`
			}{Type: "input_image", ImageURL: uri, Detail: string(block.Image.Detail)}); err != nil {
				return nil, err
			}
		case models.ContentRefusal:
			if message.Role != models.RoleAssistant {
				return nil, unsupported("refusals are only valid in assistant messages")
			}
			if err := appendContent(struct {
				Type    string `json:"type"`
				Refusal string `json:"refusal"`
			}{Type: "refusal", Refusal: block.Refusal.Text}); err != nil {
				return nil, err
			}
		case models.ContentToolCall:
			if message.Role != models.RoleAssistant {
				return nil, unsupported("tool calls are only valid in assistant messages")
			}
			if err := flush(); err != nil {
				return nil, err
			}
			if err := appendOutput(functionCallWire{Type: "function_call", CallID: block.ToolCall.ID, Name: block.ToolCall.Name, Arguments: string(block.ToolCall.Arguments)}); err != nil {
				return nil, err
			}
		case models.ContentToolResult:
			if err := flush(); err != nil {
				return nil, err
			}
			var text strings.Builder
			for _, child := range block.ToolResult.Content {
				if child.Kind != models.ContentText {
					return nil, unsupported("Responses tool results support text only")
				}
				text.WriteString(child.Text.Text)
			}
			status := "completed"
			if block.ToolResult.IsError {
				status = "incomplete"
			}
			if err := appendOutput(struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
				Status string `json:"status,omitempty"`
			}{Type: "function_call_output", CallID: block.ToolResult.CallID, Output: text.String(), Status: status}); err != nil {
				return nil, err
			}
		case models.ContentReasoningSummary:
			return nil, unsupported("reasoning summaries require same-protocol Responses provider state")
		default:
			return nil, unsupported(fmt.Sprintf("content kind %q cannot be encoded by Responses", block.Kind))
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return output, nil
}

func encodeToolChoice(choice models.ToolChoice) (json.RawMessage, error) {
	switch choice.Kind {
	case "":
		return nil, nil
	case models.ToolChoiceAuto, models.ToolChoiceNone, models.ToolChoiceRequired:
		return marshalJSON(string(choice.Kind))
	case models.ToolChoiceFunction:
		return marshalJSON(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "function", Name: choice.Name})
	default:
		return nil, unsupported("unknown tool choice")
	}
}

func encodeTextFormat(format models.ResponseFormat) (json.RawMessage, error) {
	switch format.Kind {
	case "":
		return nil, nil
	case models.ResponseFormatText, models.ResponseFormatJSONObject:
		return marshalJSON(struct {
			Type string `json:"type"`
		}{Type: string(format.Kind)})
	case models.ResponseFormatJSONSchema:
		var strict *bool
		if format.Strict.Set {
			value := format.Strict.Value
			strict = &value
		}
		return marshalJSON(struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict,omitempty"`
		}{Type: "json_schema", Name: format.Name, Description: format.Description, Schema: format.Schema, Strict: strict})
	default:
		return nil, unsupported("unknown response format")
	}
}

func marshalJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "encode", Code: "encode_json", Message: "failed to encode Responses request", Cause: err}
	}
	return encoded, nil
}

func unsupported(message string) error {
	return &models.Error{Kind: models.ErrorUnsupportedFeature, Provider: "openai", Operation: "encode", Message: message}
}

func invalidState(message string, cause error) error {
	return &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "encode", Code: "invalid_provider_state", Message: message, Cause: cause}
}

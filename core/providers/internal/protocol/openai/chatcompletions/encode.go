package chatcompletions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

// EncodeRequest encodes a canonical request for Chat Completions.
func EncodeRequest(modelID string, req *models.Request, stream bool) ([]byte, error) {
	wire := chatRequest{
		Model: modelID, Stream: stream,
		MaxCompletionTokens: shared.OptionalPointer(req.Generation.MaxOutputTokens),
		Temperature:         shared.OptionalPointer(req.Generation.Temperature),
		TopP:                shared.OptionalPointer(req.Generation.TopP),
		Stop:                req.Generation.Stop,
		Seed:                shared.OptionalPointer(req.Generation.Seed),
		N:                   shared.OptionalPointer(req.Generation.CandidateCount),
	}
	if stream {
		wire.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	for _, instruction := range req.Instructions {
		content, err := marshalJSON(instruction.Text)
		if err != nil {
			return nil, err
		}
		wire.Messages = append(wire.Messages, chatMessage{Role: string(instruction.Role), Content: content})
	}
	for _, message := range req.Messages {
		encoded, err := encodeMessage(message)
		if err != nil {
			return nil, err
		}
		wire.Messages = append(wire.Messages, encoded...)
	}
	for _, tool := range req.Tools {
		function := chatFunction{Name: tool.Name, Description: tool.Description, Parameters: append(json.RawMessage(nil), tool.Parameters...), Strict: shared.OptionalPointer(tool.Strict)}
		wire.Tools = append(wire.Tools, chatTool{Type: "function", Function: function})
	}
	choice, err := encodeToolChoice(req.ToolChoice)
	if err != nil {
		return nil, err
	}
	wire.ToolChoice = choice
	format, err := encodeResponseFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	wire.ResponseFormat = format
	encoded, err := marshalJSON(wire)
	if err != nil {
		return nil, err
	}
	return shared.MergeExtension(encoded, req.Extensions, protocol,
		"model", "messages", "tools", "tool_choice", "response_format", "max_completion_tokens", "temperature", "top_p", "stop", "seed", "n", "stream", "stream_options")
}

func encodeMessage(message models.Message) ([]chatMessage, error) {
	var output []chatMessage
	var ordinary []models.Content
	flush := func() error {
		if len(ordinary) == 0 {
			return nil
		}
		encoded, calls, err := encodeOrdinaryContent(message.Role, ordinary)
		if err != nil {
			return err
		}
		output = append(output, chatMessage{Role: string(message.Role), Content: encoded, ToolCalls: calls})
		ordinary = nil
		return nil
	}
	for _, content := range message.Content {
		if content.Kind != models.ContentToolResult {
			ordinary = append(ordinary, content)
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, child := range content.ToolResult.Content {
			if child.Kind != models.ContentText {
				return nil, unsupported("Chat Completions tool results support text only")
			}
			text.WriteString(child.Text.Text)
		}
		raw, err := marshalJSON(text.String())
		if err != nil {
			return nil, err
		}
		output = append(output, chatMessage{Role: "tool", ToolCallID: content.ToolResult.CallID, Content: raw})
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return output, nil
}

func encodeOrdinaryContent(role models.Role, contents []models.Content) (json.RawMessage, []chatToolCall, error) {
	var parts []inputContentPart
	var calls []chatToolCall
	var assistantText strings.Builder
	for _, content := range contents {
		switch content.Kind {
		case models.ContentText:
			if role == models.RoleAssistant {
				assistantText.WriteString(content.Text.Text)
			} else {
				parts = append(parts, inputContentPart{Type: "text", Text: content.Text.Text})
			}
		case models.ContentImage:
			if role != models.RoleUser {
				return nil, nil, unsupported("Chat Completions image input is only valid in user messages")
			}
			uri := content.Image.URI
			if uri == "" {
				uri = "data:" + content.Image.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(content.Image.Data)
			}
			parts = append(parts, inputContentPart{Type: "image_url", ImageURL: &inputImageURL{URL: uri, Detail: string(content.Image.Detail)}})
		case models.ContentToolCall:
			if role != models.RoleAssistant {
				return nil, nil, unsupported("tool calls are only valid in assistant messages")
			}
			calls = append(calls, chatToolCall{ID: content.ToolCall.ID, Type: "function", Function: chatCallFunction{Name: content.ToolCall.Name, Arguments: string(content.ToolCall.Arguments)}})
		case models.ContentRefusal:
			if role != models.RoleAssistant {
				return nil, nil, unsupported("refusals are only valid in assistant messages")
			}
			assistantText.WriteString(content.Refusal.Text)
		case models.ContentReasoningSummary:
			return nil, nil, unsupported("Chat Completions cannot safely encode reasoning summaries")
		default:
			return nil, nil, unsupported(fmt.Sprintf("content kind %q cannot be encoded by Chat Completions", content.Kind))
		}
	}
	if role == models.RoleAssistant {
		if assistantText.Len() == 0 {
			if len(calls) == 0 {
				raw, err := marshalJSON("")
				return raw, calls, err
			}
			return json.RawMessage("null"), calls, nil
		}
		raw, err := marshalJSON(assistantText.String())
		return raw, calls, err
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		raw, err := marshalJSON(parts[0].Text)
		return raw, nil, err
	}
	raw, err := marshalJSON(parts)
	return raw, nil, err
}

func encodeToolChoice(choice models.ToolChoice) (json.RawMessage, error) {
	switch choice.Kind {
	case "":
		return nil, nil
	case models.ToolChoiceAuto, models.ToolChoiceNone, models.ToolChoiceRequired:
		return marshalJSON(string(choice.Kind))
	case models.ToolChoiceFunction:
		return marshalJSON(struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}{Type: "function", Function: struct {
			Name string `json:"name"`
		}{Name: choice.Name}})
	default:
		return nil, unsupported("unknown tool choice")
	}
}

func encodeResponseFormat(format models.ResponseFormat) (json.RawMessage, error) {
	switch format.Kind {
	case "":
		return nil, nil
	case models.ResponseFormatText, models.ResponseFormatJSONObject:
		return marshalJSON(struct {
			Type string `json:"type"`
		}{Type: string(format.Kind)})
	case models.ResponseFormatJSONSchema:
		type schema struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict,omitempty"`
		}
		value := schema{Name: format.Name, Description: format.Description, Schema: format.Schema, Strict: shared.OptionalPointer(format.Strict)}
		return marshalJSON(struct {
			Type       string `json:"type"`
			JSONSchema schema `json:"json_schema"`
		}{Type: "json_schema", JSONSchema: value})
	default:
		return nil, unsupported("unknown response format")
	}
}

func marshalJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "encode", Code: "encode_json", Message: "failed to encode Chat Completions request", Cause: err}
	}
	return encoded, nil
}

func unsupported(message string) error {
	return &models.Error{Kind: models.ErrorUnsupportedFeature, Provider: "openai", Operation: "encode", Message: message}
}

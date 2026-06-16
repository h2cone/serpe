package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ResponseTransport is the minimal transport an Agent needs.
type ResponseTransport interface {
	CreateResponse(context.Context, map[string]any) (map[string]any, error)
}

// Config contains the dependencies and request settings for an Agent.
type Config struct {
	Transport    ResponseTransport
	Tools        ToolExecutor
	Model        string
	Instructions string
}

// Agent runs a Responses API loop with local tool calls.
type Agent struct {
	transport    ResponseTransport
	tools        ToolExecutor
	model        string
	instructions string
}

// New creates an Agent from config.
func New(config Config) *Agent {
	return &Agent{
		transport:    config.Transport,
		tools:        config.Tools,
		model:        config.Model,
		instructions: config.Instructions,
	}
}

// Run sends userMessage to the model, executes requested tools, and returns
// the final text response.
func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	input := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": userMessage},
			},
		},
	}

	var previousResponseID string
	var lastStep *stepFingerprint

	for {
		request := a.buildRequest(previousResponseID, input)
		response, err := a.transport.CreateResponse(ctx, request)
		if err != nil {
			return "", err
		}

		toolCalls, err := extractToolCalls(response)
		if err != nil {
			return "", err
		}
		if len(toolCalls) == 0 {
			text := collectOutputText(response)
			if text == "" {
				return "", fmt.Errorf("response did not contain tool calls or message text")
			}
			return text, nil
		}

		previousResponseID, err = responseID(response)
		if err != nil {
			return "", err
		}

		outputs := make([]any, 0, len(toolCalls))
		currentStep := stepFingerprint{outcomes: make([]toolCallOutcome, 0, len(toolCalls))}
		for _, call := range toolCalls {
			output, err := a.tools.execute(ctx, call.name, call.arguments)
			if err != nil {
				output = "Error: " + err.Error()
			}

			currentStep.push(call, output)
			outputs = append(outputs, map[string]any{
				"type":    "function_call_output",
				"call_id": call.callID,
				"output":  output,
			})
		}

		if lastStep != nil && reflect.DeepEqual(*lastStep, currentStep) {
			return "", fmt.Errorf("semantic stop condition triggered: repeated identical tool calls produced identical outputs")
		}

		lastStep = &currentStep
		input = outputs
	}
}

func (a *Agent) buildRequest(previousResponseID string, input []any) map[string]any {
	request := map[string]any{
		"model":        a.model,
		"instructions": a.instructions,
		"input":        input,
		"tools":        toolDefinitions(),
	}
	if previousResponseID != "" {
		request["previous_response_id"] = previousResponseID
	}
	return request
}

type toolCall struct {
	name      string
	callID    string
	arguments map[string]any
}

type toolCallOutcome struct {
	name      string
	arguments map[string]any
	output    string
}

type stepFingerprint struct {
	outcomes []toolCallOutcome
}

func (f *stepFingerprint) push(call toolCall, output string) {
	f.outcomes = append(f.outcomes, toolCallOutcome{
		name:      call.name,
		arguments: call.arguments,
		output:    output,
	})
}

func extractToolCalls(response map[string]any) ([]toolCall, error) {
	output, ok := response["output"].([]any)
	if !ok {
		return nil, fmt.Errorf("response.output was not an array")
	}

	var calls []toolCall
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "function_call" {
			continue
		}

		name, err := stringField(item, "name")
		if err != nil {
			return nil, err
		}
		callID, err := stringField(item, "call_id")
		if err != nil {
			return nil, err
		}
		argumentsText, err := stringField(item, "arguments")
		if err != nil {
			return nil, err
		}

		var arguments map[string]any
		if err := json.Unmarshal([]byte(argumentsText), &arguments); err != nil {
			return nil, fmt.Errorf("invalid JSON arguments for tool %q: %w", name, err)
		}

		calls = append(calls, toolCall{
			name:      name,
			callID:    callID,
			arguments: arguments,
		})
	}
	return calls, nil
}

func collectOutputText(response map[string]any) string {
	output, ok := response["output"].([]any)
	if !ok {
		return ""
	}

	var chunks []string
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		switch item["type"] {
		case "message":
			content, _ := item["content"].([]any)
			for _, rawPart := range content {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				if text, ok := part["text"].(string); ok {
					chunks = append(chunks, text)
				}
			}
		case "output_text":
			if text, ok := item["text"].(string); ok {
				chunks = append(chunks, text)
			}
		}
	}
	return strings.Join(chunks, "\n")
}

func responseID(response map[string]any) (string, error) {
	id, ok := response["id"].(string)
	if !ok {
		return "", fmt.Errorf("response missing id")
	}
	return id, nil
}

func toolDefinitions() []any {
	return []any{
		map[string]any{
			"type":        "function",
			"name":        "execute_shell",
			"description": "Execute a shell command on the local machine and return combined output.",
			"strict":      true,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to execute.",
					},
				},
				"required":             []any{"command"},
				"additionalProperties": false,
			},
		},
		map[string]any{
			"type":        "function",
			"name":        "read_file",
			"description": "Read a UTF-8 text file from disk.",
			"strict":      true,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative or absolute file path.",
					},
				},
				"required":             []any{"path"},
				"additionalProperties": false,
			},
		},
		map[string]any{
			"type":        "function",
			"name":        "write_file",
			"description": "Write UTF-8 text content to disk, creating parent directories when needed.",
			"strict":      true,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative or absolute file path.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The text content to write.",
					},
				},
				"required":             []any{"path", "content"},
				"additionalProperties": false,
			},
		},
	}
}

func stringField(item map[string]any, field string) (string, error) {
	value, ok := item[field].(string)
	if !ok {
		return "", fmt.Errorf("function call missing %s", field)
	}
	return value, nil
}

package models

import (
	"encoding/json"
	"fmt"

	"github.com/h2cone/ouro/internal/jsonvalue"
)

// Tool defines a client-side function available to a model.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      Optional[bool]
}

// NewTool creates a client function definition and copies its JSON Schema.
func NewTool(name, description string, parameters json.RawMessage) Tool {
	return Tool{Name: name, Description: description, Parameters: append(json.RawMessage(nil), parameters...)}
}

// Clone returns a deep copy safe for the caller to retain and modify.
func (t Tool) Clone() Tool {
	out := t
	out.Parameters = append(json.RawMessage(nil), t.Parameters...)
	return out
}

// Validate checks a tool definition.
func (t Tool) Validate() error {
	if t.Name == "" {
		return fmt.Errorf("tool: name is required")
	}
	if !jsonvalue.IsObject(t.Parameters) {
		return fmt.Errorf("tool %q: parameters must be a JSON Schema object", t.Name)
	}
	return nil
}

// ToolChoiceKind controls whether and which tool may be called.
type ToolChoiceKind string

const (
	// ToolChoiceAuto lets the model choose whether to call a tool.
	ToolChoiceAuto ToolChoiceKind = "auto"
	// ToolChoiceNone prevents tool calls.
	ToolChoiceNone ToolChoiceKind = "none"
	// ToolChoiceRequired requires at least one tool call.
	ToolChoiceRequired ToolChoiceKind = "required"
	// ToolChoiceFunction requires a named client function.
	ToolChoiceFunction ToolChoiceKind = "function"
)

// ToolChoice is an optional tool-selection policy. Its zero value is unset.
type ToolChoice struct {
	Kind ToolChoiceKind
	Name string
}

// SpecificTool selects a named function.
func SpecificTool(name string) ToolChoice {
	return ToolChoice{Kind: ToolChoiceFunction, Name: name}
}

// ResponseFormatKind identifies a requested output representation.
type ResponseFormatKind string

const (
	// ResponseFormatText requests ordinary text.
	ResponseFormatText ResponseFormatKind = "text"
	// ResponseFormatJSONObject requests a JSON object.
	ResponseFormatJSONObject ResponseFormatKind = "json_object"
	// ResponseFormatJSONSchema requests output conforming to a JSON Schema.
	ResponseFormatJSONSchema ResponseFormatKind = "json_schema"
)

// ResponseFormat is an optional structured-output request. Its zero value is
// unset.
type ResponseFormat struct {
	Kind        ResponseFormatKind
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      Optional[bool]
}

// JSONSchemaFormat creates a JSON Schema response format and copies schema.
func JSONSchemaFormat(name, description string, schema json.RawMessage) ResponseFormat {
	return ResponseFormat{Kind: ResponseFormatJSONSchema, Name: name, Description: description, Schema: append(json.RawMessage(nil), schema...)}
}

func (f ResponseFormat) validate() error {
	switch f.Kind {
	case "", ResponseFormatText, ResponseFormatJSONObject:
		return nil
	case ResponseFormatJSONSchema:
		if f.Name == "" || !jsonvalue.IsObject(f.Schema) {
			return fmt.Errorf("response format: JSON Schema name and object schema are required")
		}
		return nil
	default:
		return fmt.Errorf("response format: unknown kind %q", f.Kind)
	}
}

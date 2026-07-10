package canon

import "encoding/json"

// Tool describes one capability advertised to the model. Parameters carries a
// JSON Schema verbatim; schema validation is the tool-execution side's concern.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Extra       map[string]any  `json:"-"` // tool-level protocol metadata, e.g. Anthropic cache_control
}

// ToolChoiceMode selects how the model should pick tools.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceSpecific ToolChoiceMode = "specific"
)

// ToolChoice constrains tool selection. Name is used only when Mode == specific.
type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode"`
	Name string         `json:"name,omitempty"`
}

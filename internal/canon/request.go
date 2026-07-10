package canon

// Request is the protocol-agnostic request carried through the codec layer.
type Request struct {
	Model        string         `json:"model"`
	Conversation Conversation   `json:"conversation"`
	Tools        []Tool         `json:"tools,omitempty"`
	ToolChoice   ToolChoice     `json:"tool_choice,omitempty"`
	Temperature  *float64       `json:"temperature,omitempty"`
	TopP         *float64       `json:"top_p,omitempty"`
	MaxTokens    int            `json:"max_tokens,omitempty"` // Anthropic requires this
	Stop         []string       `json:"stop,omitempty"`
	Stream       bool           `json:"stream,omitempty"`
	Extra        map[string]any `json:"-"` // protocol-specific top-level passthrough; codec may read it
}

package responses

import "encoding/json"

type requestWire struct {
	Model           string            `json:"model"`
	Input           []json.RawMessage `json:"input"`
	Tools           []toolWire        `json:"tools,omitempty"`
	ToolChoice      json.RawMessage   `json:"tool_choice,omitempty"`
	Text            *textConfigWire   `json:"text,omitempty"`
	MaxOutputTokens *int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	TopP            *float64          `json:"top_p,omitempty"`
	Stream          bool              `json:"stream"`
}

type toolWire struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
}

type textConfigWire struct {
	Format json.RawMessage `json:"format"`
}

type responseWire struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	CreatedAt         int64             `json:"created_at"`
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             *usageWire        `json:"usage"`
	Error             *errorWire        `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type usageWire struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	TotalTokens        *int64 `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type errorWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

type itemHeader struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type messageItemWire struct {
	Type    string            `json:"type"`
	ID      string            `json:"id,omitempty"`
	Role    string            `json:"role"`
	Status  string            `json:"status,omitempty"`
	Content []json.RawMessage `json:"content"`
}

type contentPartWire struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type functionCallWire struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Status    string `json:"status,omitempty"`
}

type reasoningItemWire struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	// Content holds optional reasoning_text parts (e.g. GPT-OSS models).
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type streamEventWire struct {
	Type         string          `json:"type"`
	Sequence     int64           `json:"sequence_number"`
	Response     json.RawMessage `json:"response"`
	Item         json.RawMessage `json:"item"`
	Part         json.RawMessage `json:"part"`
	OutputIndex  int             `json:"output_index"`
	ContentIndex int             `json:"content_index"`
	ItemID       string          `json:"item_id"`
	Delta        string          `json:"delta"`
	Text         *string         `json:"text"`
	Refusal      *string         `json:"refusal"`
	Arguments    string          `json:"arguments"`
	Name         string          `json:"name"`
	Error        *errorWire      `json:"error"`
}

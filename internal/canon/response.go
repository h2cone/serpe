package canon

// FinishReason is the protocol-normalized reason the model stopped.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"           // normal end / end_turn
	FinishLength        FinishReason = "length"         // truncated by max_tokens
	FinishToolCalls     FinishReason = "tool_calls"     // stopped to call tools
	FinishContentFilter FinishReason = "content_filter" // safety filter
	FinishStopSequence  FinishReason = "stop_sequence"  // stop sequence hit (only when the protocol exposes it)
	FinishPaused        FinishReason = "paused"         // Anthropic pause_turn
)

// Usage reports token accounting, including cache fields where the protocol
// provides them.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheRead    int `json:"cache_read,omitempty"`
	CacheWrite   int `json:"cache_write,omitempty"`
}

// Response is a completed assistant turn. Content reuses []ContentBlock -- one
// assistant turn is text blocks plus tool-use blocks plus optional thinking.
type Response struct {
	ID           string         `json:"id"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	FinishReason FinishReason   `json:"finish_reason"`
	Usage        Usage          `json:"usage"`
	Provider     string         `json:"provider,omitempty"` // codec name that produced this response
	Extra        map[string]any `json:"-"`                  // response-side protocol passthrough; codec may read/write
}

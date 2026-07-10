package canon

// Event is one canonical streaming event. The taxonomy is taken from Anthropic
// (the finest-grained, most orthogonal of the three); OpenAI's two stream
// flavors map down onto it. See json.go for discriminant (un)marshaling.
type Event interface{ event() }

// MessageStartEvent begins a stream, carrying a partial Response (at least ID,
// Model, role=assistant).
type MessageStartEvent struct {
	Response *Response `json:"response"`
}

// ContentBlockStartEvent signals a new content block is starting (text / tool_use / thinking).
type ContentBlockStartEvent struct {
	Index int          `json:"index"`
	Block ContentBlock `json:"block"`
}

// ContentBlockDeltaEvent is an incremental update to the current block.
type ContentBlockDeltaEvent struct {
	Index int   `json:"index"`
	Delta Delta `json:"delta"`
}

// ContentBlockStopEvent signals the block at Index has ended.
type ContentBlockStopEvent struct {
	Index int `json:"index"`
}

// MessageDeltaEvent is a message-level update: final finish_reason and cumulative usage.
type MessageDeltaEvent struct {
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	Usage        *Usage       `json:"usage,omitempty"`
}

// MessageStopEvent signals the stream has ended.
type MessageStopEvent struct{}

// ErrorEvent carries an in-stream error. Err is not serialized.
type ErrorEvent struct {
	Err error `json:"-"`
}

// Delta is an incremental update to the current content block.
type Delta struct {
	Type      string `json:"type"`                // text|input_json|thinking|signature
	Text      string `json:"text,omitempty"`      // text/thinking increment
	Partial   string `json:"partial,omitempty"`   // partial JSON arguments for input_json
	Signature string `json:"signature,omitempty"` // thinking signature increment
}

func (MessageStartEvent) event()      {}
func (ContentBlockStartEvent) event() {}
func (ContentBlockDeltaEvent) event() {}
func (ContentBlockStopEvent) event()  {}
func (MessageDeltaEvent) event()      {}
func (MessageStopEvent) event()       {}
func (ErrorEvent) event()             {}

// Canonical streaming event discriminators.
const (
	EventMessageStart      = "message_start"
	EventContentBlockStart = "content_block_start"
	EventContentBlockDelta = "content_block_delta"
	EventContentBlockStop  = "content_block_stop"
	EventMessageDelta      = "message_delta"
	EventMessageStop       = "message_stop"
	EventError             = "error"
)

// Delta type discriminators.
const (
	DeltaText      = "text"
	DeltaInputJSON = "input_json"
	DeltaThinking  = "thinking"
	DeltaSignature = "signature"
)

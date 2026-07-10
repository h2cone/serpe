package canon

// Role is the turn-level role of a message. system is not a turn role: it lives
// in Conversation.System, mirroring Anthropic's stricter constraint.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in a transcript. Its Content is a slice of ContentBlock.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
	Extra   map[string]any `json:"-"` // message-level protocol metadata
}

// Conversation is the full protocol-agnostic transcript. A single Run carries
// it turn by turn; no protocol-side state (such as previous_response_id) is used.
type Conversation struct {
	System   string         `json:"system,omitempty"` // system / developer instructions
	Messages []Message      `json:"messages"`
	Extra    map[string]any `json:"-"` // transcript-level metadata, e.g. Anthropic system blocks
}

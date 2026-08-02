package providers

// Protocol selects one concrete provider wire protocol.
type Protocol string

const (
	// OpenAIChatCompletions selects POST /v1/chat/completions.
	OpenAIChatCompletions Protocol = "openai.chat_completions"
	// OpenAIResponses selects POST /v1/responses.
	OpenAIResponses Protocol = "openai.responses"
	// AnthropicMessages selects POST /v1/messages.
	AnthropicMessages Protocol = "anthropic.messages"
	// GeminiGenerateContent selects Gemini generateContent endpoints.
	GeminiGenerateContent Protocol = "gemini.generate_content"
)

var protocolDetails = map[Protocol]struct{ provider, baseURL string }{
	OpenAIChatCompletions: {"openai", "https://api.openai.com"},
	OpenAIResponses:       {"openai", "https://api.openai.com"},
	AnthropicMessages:     {"anthropic", "https://api.anthropic.com"},
	GeminiGenerateContent: {"gemini", "https://generativelanguage.googleapis.com"},
}

func (p Protocol) providerName() string   { return protocolDetails[p].provider }
func (p Protocol) defaultBaseURL() string { return protocolDetails[p].baseURL }

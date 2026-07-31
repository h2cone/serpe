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

func (p Protocol) providerName() string {
	switch p {
	case OpenAIChatCompletions, OpenAIResponses:
		return "openai"
	case AnthropicMessages:
		return "anthropic"
	case GeminiGenerateContent:
		return "gemini"
	default:
		return ""
	}
}

func (p Protocol) defaultBaseURL() string {
	switch p {
	case OpenAIChatCompletions, OpenAIResponses:
		return "https://api.openai.com"
	case AnthropicMessages:
		return "https://api.anthropic.com"
	case GeminiGenerateContent:
		return "https://generativelanguage.googleapis.com"
	default:
		return ""
	}
}

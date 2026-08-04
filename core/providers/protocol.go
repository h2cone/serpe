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

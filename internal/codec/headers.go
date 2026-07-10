package codec

import "net/http"

// OpenAIAuth builds the Authorization header used by both OpenAI protocols.
func OpenAIAuth(apiKey string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+apiKey)
	return h
}

// AnthropicAuth builds the x-api-key and anthropic-version headers required by
// the Anthropic Messages API.
func AnthropicAuth(apiKey string) http.Header {
	h := http.Header{}
	h.Set("x-api-key", apiKey)
	h.Set("anthropic-version", "2023-06-01")
	return h
}

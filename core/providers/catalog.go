package providers

import (
	"github.com/h2cone/serpe/core/providers/internal/driver/builtin"
	officialanthropic "github.com/h2cone/serpe/core/providers/internal/driver/official/anthropic"
	officialgoogle "github.com/h2cone/serpe/core/providers/internal/driver/official/google"
	officialopenai "github.com/h2cone/serpe/core/providers/internal/driver/official/openai"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

type providerFactory func(shared.Config) (Provider, error)

// protocolSpec is the sole public-facade catalog entry for a wire protocol.
// It is static code rather than a mutable registration mechanism.
type protocolSpec struct {
	provider       string
	defaultBaseURL string
	apiKeyHeader   string
	apiKeyPrefix   string
	builtin        providerFactory
	official       providerFactory
}

func lookupProtocol(protocol Protocol) (protocolSpec, bool) {
	switch protocol {
	case OpenAIChatCompletions:
		return protocolSpec{
			provider:       "openai",
			defaultBaseURL: "https://api.openai.com",
			apiKeyHeader:   "Authorization",
			apiKeyPrefix:   "Bearer ",
			builtin: func(config shared.Config) (Provider, error) {
				return builtin.NewOpenAIChatCompletions(config), nil
			},
			official: func(config shared.Config) (Provider, error) {
				return officialopenai.NewChatCompletions(config)
			},
		}, true
	case OpenAIResponses:
		return protocolSpec{
			provider:       "openai",
			defaultBaseURL: "https://api.openai.com",
			apiKeyHeader:   "Authorization",
			apiKeyPrefix:   "Bearer ",
			builtin: func(config shared.Config) (Provider, error) {
				return builtin.NewOpenAIResponses(config), nil
			},
			official: func(config shared.Config) (Provider, error) {
				return officialopenai.NewResponses(config)
			},
		}, true
	case AnthropicMessages:
		return protocolSpec{
			provider:       "anthropic",
			defaultBaseURL: "https://api.anthropic.com",
			apiKeyHeader:   "X-API-Key",
			builtin: func(config shared.Config) (Provider, error) {
				return builtin.NewAnthropicMessages(config), nil
			},
			official: func(config shared.Config) (Provider, error) {
				return officialanthropic.New(config)
			},
		}, true
	case GeminiGenerateContent:
		return protocolSpec{
			provider:       "google",
			defaultBaseURL: "https://generativelanguage.googleapis.com",
			apiKeyHeader:   "X-Goog-API-Key",
			builtin: func(config shared.Config) (Provider, error) {
				return builtin.NewGeminiGenerateContent(config), nil
			},
			official: func(config shared.Config) (Provider, error) {
				return officialgoogle.New(config)
			},
		}, true
	default:
		return protocolSpec{}, false
	}
}

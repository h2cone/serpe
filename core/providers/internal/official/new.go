// Package official constructs provider adapters backed by vendor official SDKs.
package official

import (
	"fmt"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/official/anthropic"
	"github.com/h2cone/ouro/core/providers/internal/official/google"
	"github.com/h2cone/ouro/core/providers/internal/official/openai"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

// Provider matches providers.Provider without importing the public package
// (which would create an import cycle).
type Provider interface {
	Model(upstreamModelID string) (models.Model, error)
}

// Protocol is the typed boundary between the public provider selector and the
// official SDK adapters.
type Protocol string

const (
	OpenAIChatCompletions Protocol = "openai.chat_completions"
	OpenAIResponses       Protocol = "openai.responses"
	AnthropicMessages     Protocol = "anthropic.messages"
	GeminiGenerateContent Protocol = "gemini.generate_content"
)

// New selects an official SDK adapter for the given protocol. It performs no
// network operation. Construction failures from the vendor client are returned
// immediately; there is no fallback to the default Driver.
func New(protocol Protocol, config shared.Config) (Provider, error) {
	switch protocol {
	case OpenAIChatCompletions:
		return openai.NewChatCompletions(config)
	case OpenAIResponses:
		return openai.NewResponses(config)
	case AnthropicMessages:
		return anthropic.New(config)
	case GeminiGenerateContent:
		return google.New(config)
	default:
		return nil, fmt.Errorf("providers: official SDK does not support protocol %q", protocol)
	}
}

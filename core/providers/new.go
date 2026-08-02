package providers

import (
	"fmt"

	"github.com/h2cone/ouro/core/providers/internal/official"
	"github.com/h2cone/ouro/core/providers/internal/protocol/anthropic"
	"github.com/h2cone/ouro/core/providers/internal/protocol/gemini"
	"github.com/h2cone/ouro/core/providers/internal/protocol/openai/chatcompletions"
	"github.com/h2cone/ouro/core/providers/internal/protocol/openai/responses"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

// New validates and freezes configuration, selects one concrete Driver and
// protocol adapter, and returns a Provider. It performs no network operation.
// Driver selection is fixed for the lifetime of the returned Provider; bound
// models use that Driver for both Complete and Stream.
func New(config Config) (Provider, error) {
	internal, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	driver, err := normalizeDriver(config.Driver)
	if err != nil {
		return nil, err
	}
	switch driver {
	case DriverDefault:
		return newDefaultProvider(config.Protocol, internal)
	case DriverOfficialSDK:
		return official.New(official.Protocol(config.Protocol), internal)
	default:
		return nil, fmt.Errorf("providers: unknown driver %q", driver)
	}
}

func newDefaultProvider(protocol Protocol, internal shared.Config) (Provider, error) {
	switch protocol {
	case OpenAIChatCompletions:
		return chatcompletions.New(internal), nil
	case OpenAIResponses:
		return responses.New(internal), nil
	case AnthropicMessages:
		return anthropic.New(internal), nil
	case GeminiGenerateContent:
		return gemini.New(internal), nil
	default:
		return nil, fmt.Errorf("providers: unknown protocol %q", protocol)
	}
}

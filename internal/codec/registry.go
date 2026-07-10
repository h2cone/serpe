package codec

import (
	"sort"

	"github.com/tw8ap/ouro/internal/codec/anthropic"
	"github.com/tw8ap/ouro/internal/codec/openai"
)

// Built-in protocols. openai-responses and openai-chat share OpenAIAuth;
// anthropic-messages uses AnthropicAuth.
var (
	OpenAIResponses = Protocol{
		Name:           "openai-responses",
		DefaultBaseURL: "https://api.openai.com/v1",
		Endpoint:       "/responses",
		Auth:           OpenAIAuth,
		Codec:          openai.ResponsesCodec{},
	}
	OpenAIChat = Protocol{
		Name:           "openai-chat",
		DefaultBaseURL: "https://api.openai.com/v1",
		Endpoint:       "/chat/completions",
		Auth:           OpenAIAuth,
		Codec:          openai.ChatCodec{},
	}
	AnthropicMessages = Protocol{
		Name:           "anthropic-messages",
		DefaultBaseURL: "https://api.anthropic.com/v1",
		Endpoint:       "/messages",
		Auth:           AnthropicAuth,
		Codec:          anthropic.MessagesCodec{},
	}
)

var protocols = map[string]Protocol{
	OpenAIResponses.Name:   OpenAIResponses,
	OpenAIChat.Name:        OpenAIChat,
	AnthropicMessages.Name: AnthropicMessages,
}

// Lookup returns the named protocol and whether it exists.
func Lookup(name string) (Protocol, bool) {
	p, ok := protocols[name]
	return p, ok
}

// Protocols returns all registered protocols in a stable order.
func Protocols() []Protocol {
	out := make([]Protocol, 0, len(protocols))
	for _, p := range protocols {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

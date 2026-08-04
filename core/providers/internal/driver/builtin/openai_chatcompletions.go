package builtin

import (
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/protocol/openai/chatcompletions"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

// NewOpenAIChatCompletions constructs the built-in Chat Completions Driver.
func NewOpenAIChatCompletions(config shared.Config) *Provider {
	return NewProvider(config, Adapter{
		Provider: "openai", Capabilities: chatcompletions.ProtocolCapabilities(),
		CompleteEndpoint: "/v1/chat/completions", StreamEndpoint: "/v1/chat/completions", ClientRequestID: true,
		Encode: func(modelID string, req *models.Request, stream bool, config shared.Config) ([]byte, error) {
			if err := chatcompletions.RejectProviderState(req, config.Limits.MaxProviderStateBytes, config.Policy.LenientMapping); err != nil {
				return nil, err
			}
			return chatcompletions.EncodeRequest(modelID, req, stream)
		},
		Decode: func(raw []byte, requestID, _ string, _ shared.Config) (*models.Response, error) {
			return chatcompletions.DecodeResponseJSON(raw, requestID)
		},
		NewSource: func(reader *sse.Reader, requestID, modelID string, _ shared.Config) models.EventSource {
			return chatcompletions.NewSSEStreamSource(reader, requestID, modelID)
		},
	})
}

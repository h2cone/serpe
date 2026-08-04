package builtin

import (
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/protocol/openai/responses"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

// NewOpenAIResponses constructs the built-in Responses Driver.
func NewOpenAIResponses(config shared.Config) *Provider {
	return NewProvider(config, Adapter{
		Provider: "openai", Capabilities: responses.ProtocolCapabilities(),
		CompleteEndpoint: "/v1/responses", StreamEndpoint: "/v1/responses", ClientRequestID: true,
		Encode: func(modelID string, req *models.Request, stream bool, config shared.Config) ([]byte, error) {
			return responses.EncodeRequest(modelID, req, stream, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
		},
		Decode: func(raw []byte, requestID, _ string, config shared.Config) (*models.Response, error) {
			return responses.DecodeResponseJSON(raw, requestID, config.Limits.MaxProviderStateBytes)
		},
		NewSource: func(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
			return responses.NewSSEStreamSource(reader, requestID, modelID, config.Policy.IgnoreUnknownEvent, config.Limits.MaxProviderStateBytes)
		},
	})
}

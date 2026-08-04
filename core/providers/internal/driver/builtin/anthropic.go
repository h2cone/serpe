package builtin

import (
	"net/http"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/protocol/anthropic"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

// NewAnthropicMessages constructs the built-in Anthropic Messages Driver.
func NewAnthropicMessages(config shared.Config) *Provider {
	return NewProvider(config, Adapter{
		Provider: "anthropic", Capabilities: anthropic.ProtocolCapabilities(),
		CompleteEndpoint: "/v1/messages", StreamEndpoint: "/v1/messages",
		Headers: http.Header{"Anthropic-Version": {"2023-06-01"}},
		Encode: func(modelID string, req *models.Request, stream bool, config shared.Config) ([]byte, error) {
			return anthropic.EncodeRequest(modelID, req, stream, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
		},
		Decode: func(raw []byte, requestID, _ string, config shared.Config) (*models.Response, error) {
			return anthropic.DecodeResponseJSON(raw, requestID, config.Limits.MaxProviderStateBytes)
		},
		NewSource: func(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
			return anthropic.NewSSEStreamSource(reader, requestID, modelID, config.Policy.IgnoreUnknownEvent, config.Limits.MaxProviderStateBytes)
		},
	})
}

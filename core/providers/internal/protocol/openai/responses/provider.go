// Package responses implements the OpenAI Responses protocol.
package responses

import (
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/httpx"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

const protocol = "openai.responses"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONOutput,
	models.CapabilityJSONSchema,
	models.CapabilityReasoningSummary,
	models.CapabilityProviderState,
)

// Provider is the internal immutable Responses provider.
type Provider = httpx.Provider

// New constructs a Responses provider without network access.
func New(config shared.Config) *Provider {
	return httpx.NewProvider(config, httpx.Adapter{
		Provider: "openai", Capabilities: capabilities,
		CompleteEndpoint: "/v1/responses", StreamEndpoint: "/v1/responses", ClientRequestID: true,
		Encode: func(modelID string, req *models.Request, stream bool, config shared.Config) ([]byte, error) {
			return EncodeRequest(modelID, req, stream, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
		},
		Decode: httpx.JSONDecoder("openai", func(wire responseWire, requestID, _ string, config shared.Config) (*models.Response, error) {
			return decodeResponse(wire, requestID, config.Limits.MaxProviderStateBytes)
		}),
		NewSource: func(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
			return NewSSEStreamSource(reader, requestID, modelID, config.Policy.IgnoreUnknownEvent, config.Limits.MaxProviderStateBytes)
		},
	})
}

// Package anthropic implements the Anthropic Messages protocol.
package anthropic

import (
	"net/http"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/httpx"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

const protocol = "anthropic.messages"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONSchema,
	models.CapabilityReasoningSummary,
	models.CapabilityProviderState,
)

// Provider is the internal immutable Anthropic Messages provider.
type Provider = httpx.Provider

// New constructs an Anthropic Messages provider without network access.
func New(config shared.Config) *Provider {
	return httpx.NewProvider(config, httpx.Adapter{
		Provider: "anthropic", Capabilities: capabilities,
		CompleteEndpoint: "/v1/messages", StreamEndpoint: "/v1/messages",
		Headers: http.Header{"Anthropic-Version": {"2023-06-01"}},
		Encode: func(modelID string, req *models.Request, stream bool, config shared.Config) ([]byte, error) {
			return EncodeRequest(modelID, req, stream, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
		},
		Decode: httpx.JSONDecoder("anthropic", func(wire messageWire, requestID, _ string, config shared.Config) (*models.Response, error) {
			return decodeResponse(wire, requestID, config.Limits.MaxProviderStateBytes)
		}),
		NewSource: func(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
			return NewSSEStreamSource(reader, requestID, modelID, config.Policy.IgnoreUnknownEvent, config.Limits.MaxProviderStateBytes)
		},
	})
}

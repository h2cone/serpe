// Package google implements the Google Gemini GenerateContent protocol.
package google

import (
	"net/url"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/httpx"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

const protocol = "gemini.generate_content"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONOutput,
	models.CapabilityJSONSchema,
	models.CapabilityReasoningSummary,
	models.CapabilityProviderState,
	models.CapabilityMultipleCandidates,
)

// Provider is the internal immutable Gemini provider.
type Provider = httpx.Provider

// New constructs a Gemini provider without network access.
func New(config shared.Config) *Provider {
	return httpx.NewProvider(config, httpx.Adapter{
		Provider: "google", Capabilities: capabilities, BindModel: ValidateModelID,
		Route: func(modelID string, stream bool) (string, url.Values) {
			if stream {
				return "/v1beta/models/" + modelID + ":streamGenerateContent", url.Values{"alt": {"sse"}}
			}
			return "/v1beta/models/" + modelID + ":generateContent", nil
		},
		Encode: func(_ string, req *models.Request, _ bool, config shared.Config) ([]byte, error) {
			return EncodeRequest(req, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
		},
		Decode: httpx.JSONDecoder("google", func(wire responseWire, requestID, modelID string, config shared.Config) (*models.Response, error) {
			return decodeResponse(wire, requestID, modelID, config.Limits.MaxProviderStateBytes)
		}),
		NewSource: func(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
			return NewSSEStreamSource(reader, requestID, modelID, config.Limits.MaxProviderStateBytes)
		},
	})
}

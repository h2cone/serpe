// Package chatcompletions implements the OpenAI Chat Completions protocol.
package chatcompletions

import (
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/httpx"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

const protocol = "openai.chat_completions"

var capabilities = models.Capabilities(
	models.CapabilityText,
	models.CapabilityImageInput,
	models.CapabilityTools,
	models.CapabilityParallelTools,
	models.CapabilityJSONOutput,
	models.CapabilityJSONSchema,
	models.CapabilityMultipleCandidates,
)

// Provider is the internal immutable Chat Completions provider.
type Provider = httpx.Provider

// New constructs a Chat Completions provider without network access.
func New(config shared.Config) *Provider {
	return httpx.NewProvider(config, httpx.Adapter{
		Provider: "openai", Capabilities: capabilities,
		CompleteEndpoint: "/v1/chat/completions", StreamEndpoint: "/v1/chat/completions", ClientRequestID: true,
		Encode: func(modelID string, req *models.Request, stream bool, config shared.Config) ([]byte, error) {
			if err := RejectProviderState(req, config.Limits.MaxProviderStateBytes, config.Policy.LenientMapping); err != nil {
				return nil, err
			}
			return EncodeRequest(modelID, req, stream)
		},
		Decode: httpx.JSONDecoder("openai", func(wire chatResponse, requestID, _ string, _ shared.Config) (*models.Response, error) {
			return decodeResponse(wire, requestID)
		}),
		NewSource: func(reader *sse.Reader, requestID, modelID string, _ shared.Config) models.EventSource {
			return NewSSEStreamSource(reader, requestID, modelID)
		},
	})
}

package builtin

import (
	"net/url"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/protocol/google"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

// NewGeminiGenerateContent constructs the built-in Gemini Driver.
func NewGeminiGenerateContent(config shared.Config) *Provider {
	return NewProvider(config, Adapter{
		Provider: "google", Capabilities: google.ProtocolCapabilities(), BindModel: google.ValidateModelID,
		Route: func(modelID string, stream bool) (string, url.Values) {
			if stream {
				return "/v1beta/models/" + modelID + ":streamGenerateContent", url.Values{"alt": {"sse"}}
			}
			return "/v1beta/models/" + modelID + ":generateContent", nil
		},
		Encode: func(_ string, req *models.Request, _ bool, config shared.Config) ([]byte, error) {
			return google.EncodeRequest(req, config.Policy.LenientMapping, config.Limits.MaxProviderStateBytes)
		},
		Decode: func(raw []byte, requestID, modelID string, config shared.Config) (*models.Response, error) {
			return google.DecodeResponseJSON(raw, requestID, modelID, config.Limits.MaxProviderStateBytes)
		},
		NewSource: func(reader *sse.Reader, requestID, modelID string, config shared.Config) models.EventSource {
			return google.NewSSEStreamSource(reader, requestID, modelID, config.Limits.MaxProviderStateBytes)
		},
	})
}

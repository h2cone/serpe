// Package responses implements the OpenAI Responses protocol.
package responses

import (
	"context"
	"net/http"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/httpx"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
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
type Provider struct {
	config shared.Config
	http   *httpx.Client
}

// New constructs a Responses provider without network access.
func New(config shared.Config) *Provider {
	return &Provider{config: config, http: httpx.New(httpx.Config{
		BaseURL: config.BaseURL, Doer: config.HTTPClient, Authenticate: config.Authenticate,
		Headers: config.Headers, Provider: config.Provider,
		MaxErrorResponseBytes: config.Limits.MaxErrorResponseBytes,
		RequireContentType:    !config.Policy.IgnoreContentType, Redact: config.Redact,
	})}
}

// Model validates and binds a physical model identifier.
func (p *Provider) Model(modelID string) (models.Model, error) {
	if err := shared.ValidateModelID(modelID, "openai"); err != nil {
		return nil, err
	}
	return &model{provider: p, modelID: modelID}, nil
}

type model struct {
	provider *Provider
	modelID  string
}

func (m *model) Capabilities() models.CapabilitySet { return capabilities }

func (m *model) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	payload, err := m.encode(req, false)
	if err != nil {
		return nil, err
	}
	extra := make(http.Header)
	if req.RequestID != "" {
		extra.Set("X-Client-Request-ID", req.RequestID)
	}
	response, err := m.provider.http.Do(ctx, "generate", "/v1/responses", nil, payload, false, req.RequestID, extra)
	if err != nil {
		return nil, err
	}
	var wire responseWire
	if err := httpx.ReadJSON(response, m.provider.config.Limits.MaxResponseBytes, "openai", "generate", &wire); err != nil {
		return nil, err
	}
	return decodeResponse(wire, httpx.RequestID(response.Header), m.provider.config.Limits.MaxProviderStateBytes)
}

func (m *model) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	payload, err := m.encode(req, true)
	if err != nil {
		return nil, err
	}
	extra := make(http.Header)
	if req.RequestID != "" {
		extra.Set("X-Client-Request-ID", req.RequestID)
	}
	response, err := m.provider.http.Do(ctx, "stream", "/v1/responses", nil, payload, true, req.RequestID, extra)
	if err != nil {
		return nil, err
	}
	source := newResponseSource(sse.NewReader(response.Body, m.provider.config.Limits.MaxSSEEventBytes), httpx.RequestID(response.Header), m.modelID, m.provider.config.Policy.IgnoreUnknownEvent, m.provider.config.Limits.MaxProviderStateBytes)
	return models.NewStream(ctx, source, models.WithStreamProvider("openai")), nil
}

func (m *model) encode(req *models.Request, stream bool) ([]byte, error) {
	if err := models.ValidateCapabilities(req, capabilities, "openai"); err != nil {
		return nil, err
	}
	return encodeRequest(m.modelID, req, stream, m.provider.config.Policy.LenientMapping, m.provider.config.Limits.MaxProviderStateBytes)
}

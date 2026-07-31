// Package generatecontent implements Google Gemini GenerateContent.
package generatecontent

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/httpx"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
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
type Provider struct {
	config shared.Config
	http   *httpx.Client
}

// New constructs a Gemini provider without network access.
func New(config shared.Config) *Provider {
	return &Provider{config: config, http: httpx.New(httpx.Config{
		BaseURL: config.BaseURL, Doer: config.HTTPClient, Authenticate: config.Authenticate,
		Headers: config.Headers, Provider: config.Provider,
		MaxErrorResponseBytes: config.Limits.MaxErrorResponseBytes,
		RequireContentType:    !config.Policy.IgnoreContentType, Redact: config.Redact,
	})}
}

// Model validates a bare model ID and binds it without network access.
func (p *Provider) Model(modelID string) (models.Model, error) {
	if err := shared.ValidateModelID(modelID, "gemini"); err != nil {
		return nil, err
	}
	if modelID == "." || modelID == ".." || strings.ContainsAny(modelID, "/\\?#:%") || strings.IndexFunc(modelID, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.')
	}) >= 0 {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "gemini", Operation: "bind_model", Code: "invalid_model", Message: "Gemini model ID must be a safe bare model name"}
	}
	return &model{provider: p, modelID: modelID, escapedModelID: url.PathEscape(modelID)}, nil
}

type model struct {
	provider       *Provider
	modelID        string
	escapedModelID string
}

func (m *model) Capabilities() models.CapabilitySet { return capabilities }

func (m *model) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	payload, err := m.encode(req)
	if err != nil {
		return nil, err
	}
	endpoint := "/v1beta/models/" + m.escapedModelID + ":generateContent"
	response, err := m.provider.http.Do(ctx, "generate", endpoint, nil, payload, false, req.RequestID, nil)
	if err != nil {
		return nil, err
	}
	var wire responseWire
	if err := httpx.ReadJSON(response, m.provider.config.Limits.MaxResponseBytes, "gemini", "generate", &wire); err != nil {
		return nil, err
	}
	return decodeResponse(wire, httpx.RequestID(response.Header), m.modelID, m.provider.config.Limits.MaxProviderStateBytes)
}

func (m *model) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	payload, err := m.encode(req)
	if err != nil {
		return nil, err
	}
	endpoint := "/v1beta/models/" + m.escapedModelID + ":streamGenerateContent"
	query := url.Values{"alt": []string{"sse"}}
	response, err := m.provider.http.Do(ctx, "stream", endpoint, query, payload, true, req.RequestID, nil)
	if err != nil {
		return nil, err
	}
	source := newGeminiSource(sse.NewReader(response.Body, m.provider.config.Limits.MaxSSEEventBytes), httpx.RequestID(response.Header), m.modelID, m.provider.config.Limits.MaxProviderStateBytes)
	return models.NewStream(ctx, source, models.WithStreamProvider("gemini")), nil
}

func (m *model) encode(req *models.Request) ([]byte, error) {
	if err := models.ValidateCapabilities(req, capabilities, "gemini"); err != nil {
		return nil, err
	}
	encoded, err := encodeRequest(req, m.provider.config.Policy.LenientMapping, m.provider.config.Limits.MaxProviderStateBytes)
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("gemini: empty encoded request")
	}
	return encoded, nil
}

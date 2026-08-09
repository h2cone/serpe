// Package builtin implements the common lifecycle for direct HTTP protocol
// Drivers. Wire semantics remain in internal/protocol and HTTP I/O remains in
// internal/transport/httpx.
package builtin

import (
	"context"
	"net/http"
	"net/url"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/httpx"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

// Adapter contains the protocol-specific operations used by the built-in HTTP
// Driver. Provider owns the common immutable model, request, and stream flow.
type Adapter struct {
	Provider         string
	Capabilities     models.CapabilitySet
	CompleteEndpoint string
	StreamEndpoint   string
	StreamQuery      url.Values
	Headers          http.Header
	ClientRequestID  bool
	BindModel        func(string) (string, error)
	Route            func(string, bool) (string, url.Values)
	Encode           func(string, *models.Request, bool, shared.Config) ([]byte, error)
	Decode           func([]byte, string, string, shared.Config) (*models.Response, error)
	NewSource        func(*sse.Reader, string, string, shared.Config) models.EventSource
}

// Provider implements the lifecycle shared by built-in HTTP protocol adapters.
type Provider struct {
	config  shared.Config
	client  *httpx.Client
	adapter Adapter
}

// NewProvider constructs a built-in provider without network access.
func NewProvider(config shared.Config, adapter Adapter) *Provider {
	adapter.Headers = adapter.Headers.Clone()
	return &Provider{config: config, adapter: adapter, client: httpx.New(httpx.Config{
		BaseURL: config.BaseURL, Doer: config.HTTPClient, Authenticate: config.Authenticate,
		Headers: config.Headers, Provider: config.Provider,
		MaxErrorResponseBytes: config.Limits.MaxErrorResponseBytes,
		RequireContentType:    !config.Policy.IgnoreContentType, Redact: config.Redact,
	})}
}

// Model validates and binds an upstream model identifier.
func (p *Provider) Model(upstreamModelID string) (models.Model, error) {
	routeID, err := upstreamModelID, error(nil)
	if p.adapter.BindModel != nil {
		routeID, err = p.adapter.BindModel(upstreamModelID)
	} else {
		err = shared.ValidateModelID(upstreamModelID, p.adapter.Provider)
	}
	if err != nil {
		return nil, err
	}
	return &upstreamModel{provider: p, modelID: upstreamModelID, routeID: routeID}, nil
}

type upstreamModel struct {
	provider *Provider
	modelID  string
	routeID  string
}

func (m *upstreamModel) Capabilities() models.CapabilitySet {
	return m.provider.adapter.Capabilities
}

func (m *upstreamModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	response, err := m.send(ctx, req, false)
	if err != nil {
		return nil, err
	}
	config := m.provider.config
	raw, err := httpx.ReadJSON(response, config.Limits.MaxResponseBytes, m.provider.adapter.Provider, "generate")
	if err != nil {
		return nil, err
	}
	return m.provider.adapter.Decode(raw, httpx.RequestID(response.Header), m.modelID, config)
}

func (m *upstreamModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	response, err := m.send(ctx, req, true)
	if err != nil {
		return nil, err
	}
	config := m.provider.config
	source := m.provider.adapter.NewSource(
		sse.NewReader(response.Body, config.Limits.MaxSSEEventBytes),
		httpx.RequestID(response.Header), m.modelID, config,
	)
	return models.NewStream(ctx, source, models.WithStreamProvider(m.provider.adapter.Provider)), nil
}

func (m *upstreamModel) send(ctx context.Context, req *models.Request, stream bool) (*http.Response, error) {
	adapter, config := m.provider.adapter, m.provider.config
	if err := models.ValidateCapabilities(req, adapter.Capabilities, adapter.Provider); err != nil {
		return nil, err
	}
	payload, err := adapter.Encode(m.modelID, req, stream, config)
	if err != nil {
		return nil, err
	}
	endpoint, query := adapter.CompleteEndpoint, url.Values(nil)
	if stream {
		endpoint, query = adapter.StreamEndpoint, adapter.StreamQuery
	}
	if adapter.Route != nil {
		endpoint, query = adapter.Route(m.routeID, stream)
	}
	headers := adapter.Headers.Clone()
	if adapter.ClientRequestID && req.RequestID != "" {
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("X-Client-Request-ID", req.RequestID)
	}
	operation := "generate"
	if stream {
		operation = "stream"
	}
	return m.provider.client.Do(ctx, operation, endpoint, query, payload, stream, req.RequestID, headers)
}

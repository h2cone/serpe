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
	NewSource        func(*sse.Reader, string, string, shared.Config, models.StreamLimits) models.EventSource
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
		MaxErrorResponseBytes:  config.Limits.MaxErrorResponseBytes,
		MaxResponseBytes:       config.Limits.MaxResponseBytes,
		MaxStreamResponseBytes: config.Limits.MaxStreamResponseBytes,
		RequireContentType:     !config.Policy.IgnoreContentType, Redact: config.Redact,
	})}
}

// ResolveModel validates and binds an upstream model identifier.
func (p *Provider) ResolveModel(upstreamModelID string) (models.Model, error) {
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

func (m *upstreamModel) ToolResultPolicy() (models.ToolResultPolicy, bool) {
	return shared.ToolResultPolicy(m.provider.adapter.Provider, m.modelID, m.Capabilities())
}

func (m *upstreamModel) AllowsToolHistoryGroupDeletion() bool { return true }

func (m *upstreamModel) ValidateToolDefinitions(defs []models.Tool) error {
	for i := range defs {
		if err := defs[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (m *upstreamModel) MaxEncodedRequestBytes() int64 {
	return m.provider.config.Limits.MaxRequestBytes
}

func (m *upstreamModel) EncodedRequestSizeUpperBound(ctx context.Context, req *models.Request, stream bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	payload, err := m.provider.adapter.Encode(m.modelID, req, stream, m.provider.config)
	if err != nil {
		return 0, err
	}
	return int64(len(payload)), nil
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
	decoded, err := m.provider.adapter.Decode(raw, httpx.RequestID(response.Header), m.modelID, config)
	if err != nil {
		return nil, err
	}
	return models.ApplyStreamLimitsToResponse(ctx, decoded, models.StreamLimits{})
}

func (m *upstreamModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	return m.StreamWithLimits(ctx, req, models.StreamLimits{})
}

func (m *upstreamModel) StreamWithLimits(ctx context.Context, req *models.Request, limits models.StreamLimits) (models.Stream, error) {
	response, err := m.send(ctx, req, true)
	if err != nil {
		return nil, err
	}
	config := m.provider.config
	source := m.provider.adapter.NewSource(
		sse.NewReader(response.Body, config.Limits.MaxSSEEventBytes),
		httpx.RequestID(response.Header), m.modelID, config, limits,
	)
	return models.NewStream(ctx, source,
		models.WithStreamProvider(m.provider.adapter.Provider),
		models.WithStreamLimits(limits)), nil
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
	if int64(len(payload)) > config.Limits.MaxRequestBytes {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: adapter.Provider, Operation: "encode", Code: "request_too_large", Message: "encoded request exceeds provider limit"}
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

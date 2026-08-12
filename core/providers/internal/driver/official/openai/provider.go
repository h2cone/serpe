// Package openai implements official OpenAI Go SDK adapters for Chat Completions
// and Responses.
package openai

import (
	"context"
	"errors"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sdkhttp"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

// Provider is an immutable official-SDK OpenAI provider bound to one protocol.
type Provider struct {
	config  shared.Config
	adapter protocolAdapter
}

type protocolAdapter interface {
	capabilities() models.CapabilitySet
	encode(modelID string, request *models.Request, stream bool, config shared.Config) ([]byte, error)
	complete(context.Context, []byte, []option.RequestOption) ([]byte, error)
	startStream(context.Context, []byte, []option.RequestOption) (func() error, error)
	decode([]byte, string, shared.Config) (*models.Response, error)
	newSource(*sse.Reader, string, string, shared.Config) models.EventSource
}

// NewChatCompletions constructs an official Chat Completions provider without
// network access.
func NewChatCompletions(config shared.Config) (*Provider, error) {
	opts := requestOptions(config)
	return &Provider{config: config, adapter: newChatAdapter(opts)}, nil
}

// NewResponses constructs an official Responses provider without network
// access.
func NewResponses(config shared.Config) (*Provider, error) {
	opts := requestOptions(config)
	return &Provider{config: config, adapter: newResponsesAdapter(opts)}, nil
}

func requestOptions(config shared.Config) []option.RequestOption {
	bridge := sdkhttp.NewConfigBridge(config, "openai")
	endpoint := sdkhttp.OpenAIEndpoint(config.BaseURL)
	opts := []option.RequestOption{
		option.WithBaseURL(endpoint.BaseURL),
		option.WithHTTPClient(bridge),
		option.WithMaxRetries(0),
		// Explicit empty key so ambient OPENAI_API_KEY cannot win; the bridge
		// strips Authorization and applies Config authentication.
		option.WithAPIKey(""),
		option.WithHeaderDel("Authorization"),
	}
	// Service constructors use only the supplied options, unlike
	// openai.NewClient, which prepends OPENAI_* environment defaults.
	return opts
}

// ResolveModel validates and binds an upstream model identifier.
func (p *Provider) ResolveModel(upstreamModelID string) (models.Model, error) {
	if err := shared.ValidateModelID(upstreamModelID, "openai"); err != nil {
		return nil, err
	}
	return &upstreamModel{provider: p, modelID: upstreamModelID}, nil
}

type upstreamModel struct {
	provider *Provider
	modelID  string
}

func (m *upstreamModel) Capabilities() models.CapabilitySet {
	return m.provider.adapter.capabilities()
}

func (m *upstreamModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "openai", "generate"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req, false)
	if err != nil {
		return nil, err
	}
	ctx, capture := sdkhttp.PrepareCall(ctx, "generate", nil)

	var callOpts []option.RequestOption
	if req.RequestID != "" {
		callOpts = append(callOpts,
			option.WithHeader("X-Client-Request-ID", req.RequestID),
			option.WithHeader("X-Request-ID", req.RequestID),
		)
	}

	raw, callErr := m.provider.adapter.complete(ctx, payload, callOpts)
	if callErr != nil {
		return nil, normalizeError(callErr, "generate", capture, m.provider.config.Redact)
	}
	return m.provider.adapter.decode(raw, capture.RequestID(), m.provider.config)
}

func (m *upstreamModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "openai", "stream"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req, true)
	if err != nil {
		return nil, err
	}
	streamCtx, capture, cancel := sdkhttp.PrepareStreamCall(ctx, "stream", nil)

	var callOpts []option.RequestOption
	if req.RequestID != "" {
		callOpts = append(callOpts,
			option.WithHeader("X-Client-Request-ID", req.RequestID),
			option.WithHeader("X-Request-ID", req.RequestID),
		)
	}

	closeSDK, callErr := m.provider.adapter.startStream(streamCtx, payload, callOpts)
	if callErr != nil {
		cancel()
		return nil, normalizeError(callErr, "stream", capture, m.provider.config.Redact)
	}
	body := capture.StreamBody()
	if body == nil {
		cancel()
		if closeSDK != nil {
			_ = closeSDK()
		}
		return nil, &models.Error{
			Kind: models.ErrorProtocol, Provider: "openai", Operation: "stream",
			Code: "missing_response", Message: "official OpenAI SDK returned no streaming response body",
		}
	}
	reader := sse.NewReaderWithClose(body, m.provider.config.Limits.MaxSSEEventBytes, func() error {
		cancel()
		if closeSDK != nil {
			return closeSDK()
		}
		return nil
	})
	source := m.provider.adapter.newSource(reader, capture.RequestID(), m.modelID, m.provider.config)
	return models.NewStream(ctx, source, models.WithStreamProvider("openai")), nil
}

func (m *upstreamModel) encode(req *models.Request, stream bool) ([]byte, error) {
	if err := models.ValidateCapabilities(req, m.Capabilities(), "openai"); err != nil {
		return nil, err
	}
	return m.provider.adapter.encode(m.modelID, req, stream, m.provider.config)
}

func normalizeError(err error, operation string, capture *sdkhttp.Capture, redact []string) error {
	return sdkhttp.NormalizeError(err, "openai", operation, capture, redact, "official OpenAI SDK call failed", parseError)
}

func parseError(err error) (sdkhttp.ErrorInfo, bool) {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return sdkhttp.ErrorInfo{}, false
	}
	info := sdkhttp.ErrorInfo{Status: apiErr.StatusCode, Code: shared.FirstNonempty(apiErr.Code, apiErr.Type), Message: apiErr.Message}
	if apiErr.Response != nil {
		info.Header = apiErr.Response.Header
	}
	return info, true
}

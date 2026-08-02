// Package openai implements official OpenAI Go SDK adapters for Chat Completions
// and Responses.
package openai

import (
	"context"
	"errors"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/httpx"
	"github.com/h2cone/ouro/core/providers/internal/sdkhttp"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
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
	bridge := sdkhttp.NewBridge(sdkhttp.BridgeConfig{
		Doer:               config.HTTPClient,
		Authenticate:       config.Authenticate,
		Headers:            config.Headers,
		Provider:           "openai",
		Limits:             config.Limits,
		RequireContentType: !config.Policy.IgnoreContentType,
	})
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

func (m *model) Capabilities() models.CapabilitySet {
	return m.provider.adapter.capabilities()
}

func (m *model) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	if ctx == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "generate", Message: "context is nil"}
	}
	if req == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "generate", Message: "request is nil"}
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

func (m *model) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	if ctx == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "stream", Message: "context is nil"}
	}
	if req == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "openai", Operation: "stream", Message: "request is nil"}
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

func (m *model) encode(req *models.Request, stream bool) ([]byte, error) {
	if err := models.ValidateCapabilities(req, m.Capabilities(), "openai"); err != nil {
		return nil, err
	}
	return m.provider.adapter.encode(m.modelID, req, stream, m.provider.config)
}

func normalizeError(err error, operation string, capture *sdkhttp.Capture, redact []string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return models.ContextError("openai", operation, err)
	}
	var modelErr *models.Error
	if errors.As(err, &modelErr) {
		return modelErr
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		requestID := ""
		if capture != nil {
			requestID = capture.RequestID()
		}
		if requestID == "" && apiErr.Response != nil {
			requestID = httpx.RequestID(apiErr.Response.Header)
		}
		message := apiErr.Message
		code := apiErr.Code
		if code == "" {
			code = apiErr.Type
		}
		message = httpx.Redact(message, redact)
		code = httpx.Redact(code, redact)
		kind, retryable := httpx.StatusKind(apiErr.StatusCode)
		var retryAfter time.Duration
		if apiErr.Response != nil {
			retryAfter = httpx.RetryAfter(apiErr.Response.Header.Get("Retry-After"))
		}
		return &models.Error{
			Kind: kind, Provider: "openai", Operation: operation,
			HTTPStatus: apiErr.StatusCode, Code: code, Message: message,
			RequestID: requestID, RetryAfter: retryAfter, Retryable: retryable,
		}
	}
	return &models.Error{
		Kind: models.ErrorUnavailable, Provider: "openai", Operation: operation,
		Message: "official OpenAI SDK call failed", Retryable: true,
	}
}

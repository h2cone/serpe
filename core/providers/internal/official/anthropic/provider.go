// Package anthropic implements the official Anthropic Go SDK Messages adapter.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/h2cone/ouro/core/models"
	defaultanthropic "github.com/h2cone/ouro/core/providers/internal/anthropic"
	"github.com/h2cone/ouro/core/providers/internal/httpx"
	"github.com/h2cone/ouro/core/providers/internal/sdkhttp"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/sse"
)

// Provider is an immutable official-SDK Anthropic Messages provider.
type Provider struct {
	config shared.Config
	client anthropic.Client
}

// New constructs an official Anthropic provider without network access.
func New(config shared.Config) (*Provider, error) {
	bridge := sdkhttp.NewBridge(sdkhttp.BridgeConfig{
		Doer:               config.HTTPClient,
		Authenticate:       config.Authenticate,
		Headers:            config.Headers,
		Provider:           "anthropic",
		Limits:             config.Limits,
		RequireContentType: !config.Policy.IgnoreContentType,
	})
	endpoint := sdkhttp.AnthropicEndpoint(config.BaseURL)
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(endpoint.BaseURL),
		option.WithHTTPClient(bridge),
		option.WithMaxRetries(0),
		option.WithAPIKey(""),
		option.WithHeaderDel("X-Api-Key"),
		option.WithHeaderDel("Authorization"),
		// Non-streaming Messages.New always attaches a request timeout. Pass a
		// large explicit timeout so CalculateNonStreamingTimeout does not
		// require streaming for large max_tokens; the sdkhttp bridge restores
		// the caller's context so the SDK timeout never terminates early.
		option.WithRequestTimeout(24 * time.Hour),
	}
	client := anthropic.NewClient(opts...)
	return &Provider{config: config, client: client}, nil
}

// Model validates and binds a physical model identifier.
func (p *Provider) Model(modelID string) (models.Model, error) {
	if err := shared.ValidateModelID(modelID, "anthropic"); err != nil {
		return nil, err
	}
	return &model{provider: p, modelID: modelID}, nil
}

type model struct {
	provider *Provider
	modelID  string
}

func (m *model) Capabilities() models.CapabilitySet {
	return defaultanthropic.ProtocolCapabilities()
}

func (m *model) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	if ctx == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "generate", Message: "context is nil"}
	}
	if req == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "generate", Message: "request is nil"}
	}
	payload, err := m.encode(req, false)
	if err != nil {
		return nil, err
	}
	ctx, capture := sdkhttp.PrepareCall(ctx, "generate", nil)

	var params anthropic.MessageNewParams
	param.SetJSON(payload, &params)
	var callOpts []option.RequestOption
	if req.RequestID != "" {
		callOpts = append(callOpts, option.WithHeader("X-Request-ID", req.RequestID))
	}
	message, callErr := m.provider.client.Messages.New(ctx, params, callOpts...)
	if callErr != nil {
		return nil, normalizeError(callErr, "generate", capture, m.provider.config.Redact)
	}
	requestID := capture.RequestID()
	return defaultanthropic.DecodeResponseJSON([]byte(message.RawJSON()), requestID, m.provider.config.Limits.MaxProviderStateBytes)
}

func (m *model) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	if ctx == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "stream", Message: "context is nil"}
	}
	if req == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: "stream", Message: "request is nil"}
	}
	payload, err := m.encode(req, true)
	if err != nil {
		return nil, err
	}
	streamCtx, capture, cancel := sdkhttp.PrepareStreamCall(ctx, "stream", nil)

	var params anthropic.MessageNewParams
	param.SetJSON(payload, &params)
	var callOpts []option.RequestOption
	if req.RequestID != "" {
		callOpts = append(callOpts, option.WithHeader("X-Request-ID", req.RequestID))
	}
	stream := m.provider.client.Messages.NewStreaming(streamCtx, params, callOpts...)
	if err := stream.Err(); err != nil {
		cancel()
		return nil, normalizeError(err, "stream", capture, m.provider.config.Redact)
	}
	body := capture.StreamBody()
	if body == nil {
		cancel()
		_ = stream.Close()
		return nil, &models.Error{
			Kind: models.ErrorProtocol, Provider: "anthropic", Operation: "stream",
			Code: "missing_response", Message: "official Anthropic SDK returned no streaming response body",
		}
	}
	requestID := capture.RequestID()
	source := defaultanthropic.NewSSEStreamSource(
		sse.NewReaderWithClose(body, m.provider.config.Limits.MaxSSEEventBytes, func() error {
			cancel()
			return stream.Close()
		}), requestID, m.modelID, m.provider.config.Policy.IgnoreUnknownEvent, m.provider.config.Limits.MaxProviderStateBytes)
	return models.NewStream(ctx, source, models.WithStreamProvider("anthropic")), nil
}

func (m *model) encode(req *models.Request, stream bool) ([]byte, error) {
	if err := models.ValidateCapabilities(req, m.Capabilities(), "anthropic"); err != nil {
		return nil, err
	}
	return defaultanthropic.EncodeRequest(m.modelID, req, stream, m.provider.config.Policy.LenientMapping, m.provider.config.Limits.MaxProviderStateBytes)
}

func normalizeError(err error, operation string, capture *sdkhttp.Capture, redact []string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return models.ContextError("anthropic", operation, err)
	}
	var modelErr *models.Error
	if errors.As(err, &modelErr) {
		return modelErr
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		requestID := apiErr.RequestID
		if requestID == "" && capture != nil {
			requestID = capture.RequestID()
		}
		if requestID == "" && apiErr.Response != nil {
			requestID = httpx.RequestID(apiErr.Response.Header)
		}
		message, code := parseAnthropicErrorBody(apiErr.RawJSON())
		if message == "" {
			message = http.StatusText(apiErr.StatusCode)
		}
		if code == "" {
			code = string(apiErr.Type())
		}
		message = httpx.Redact(message, redact)
		code = httpx.Redact(code, redact)
		kind, retryable := httpx.StatusKind(apiErr.StatusCode)
		var retryAfter time.Duration
		if apiErr.Response != nil {
			retryAfter = httpx.RetryAfter(apiErr.Response.Header.Get("Retry-After"))
		}
		return &models.Error{
			Kind: kind, Provider: "anthropic", Operation: operation,
			HTTPStatus: apiErr.StatusCode, Code: code, Message: message,
			RequestID: requestID, RetryAfter: retryAfter, Retryable: retryable,
		}
	}
	return &models.Error{
		Kind: models.ErrorUnavailable, Provider: "anthropic", Operation: operation,
		Message: "official Anthropic SDK call failed", Retryable: true,
	}
}

func parseAnthropicErrorBody(raw string) (message, code string) {
	if raw == "" {
		return "", ""
	}
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return "", ""
	}
	message = envelope.Error.Message
	if message == "" {
		message = envelope.Message
	}
	code = envelope.Error.Type
	if code == "" {
		code = envelope.Type
	}
	return message, code
}

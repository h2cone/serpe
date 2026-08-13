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

	"github.com/h2cone/serpe/core/models"
	defaultanthropic "github.com/h2cone/serpe/core/providers/internal/protocol/anthropic"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sdkhttp"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

// Provider is an immutable official-SDK Anthropic Messages provider.
type Provider struct {
	config shared.Config
	client anthropic.Client
}

// New constructs an official Anthropic provider without network access.
func New(config shared.Config) (*Provider, error) {
	bridge := sdkhttp.NewConfigBridge(config, "anthropic")
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

// ResolveModel validates and binds an upstream model identifier.
func (p *Provider) ResolveModel(upstreamModelID string) (models.Model, error) {
	if err := shared.ValidateModelID(upstreamModelID, "anthropic"); err != nil {
		return nil, err
	}
	return &upstreamModel{provider: p, modelID: upstreamModelID}, nil
}

type upstreamModel struct {
	provider *Provider
	modelID  string
}

func (m *upstreamModel) Capabilities() models.CapabilitySet {
	return defaultanthropic.ProtocolCapabilities()
}

func (m *upstreamModel) ToolResultPolicy() (models.ToolResultPolicy, bool) {
	return shared.ToolResultPolicy("anthropic", m.modelID, m.Capabilities())
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
	if configured := m.provider.config.Limits.MaxRequestBytes; configured < 30<<20 {
		return configured
	}
	return 30 << 20
}

func (m *upstreamModel) EncodedRequestSizeUpperBound(ctx context.Context, req *models.Request, stream bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	payload, err := m.encode(req, stream)
	if err != nil {
		return 0, err
	}
	return int64(len(payload)), nil
}

func (m *upstreamModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "anthropic", "generate"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req, false)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > m.MaxEncodedRequestBytes() {
		return nil, anthropicRequestTooLarge("generate")
	}
	ctx, capture := sdkhttp.PrepareCall(ctx, "generate", nil)

	var params anthropic.MessageNewParams
	param.SetJSON(payload, &params)
	var callOpts []option.RequestOption
	if req.RequestID != "" {
		callOpts = append(callOpts, option.WithHeader("X-Request-ID", req.RequestID))
	}
	raw, callErr := sdkhttp.RawJSON(m.provider.client.Messages.New(ctx, params, callOpts...))
	if callErr != nil {
		return nil, normalizeError(callErr, "generate", capture, m.provider.config.Redact)
	}
	decoded, err := defaultanthropic.DecodeResponseJSONWithLimits(raw, capture.RequestID(), m.provider.config.Limits.MaxProviderStateBytes, shared.EffectiveToolCallLimits(m.provider.config.Limits, models.StreamLimits{}))
	if err != nil {
		return nil, err
	}
	return models.ApplyStreamLimitsToResponse(ctx, decoded, models.StreamLimits{})
}

func (m *upstreamModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	return m.StreamWithLimits(ctx, req, models.StreamLimits{})
}

func (m *upstreamModel) StreamWithLimits(ctx context.Context, req *models.Request, limits models.StreamLimits) (models.Stream, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "anthropic", "stream"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req, true)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > m.MaxEncodedRequestBytes() {
		return nil, anthropicRequestTooLarge("stream")
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
		}), requestID, m.modelID, m.provider.config.Policy.IgnoreUnknownEvent, m.provider.config.Limits.MaxProviderStateBytes,
		shared.EffectiveToolCallLimits(m.provider.config.Limits, limits))
	return models.NewStream(ctx, source,
		models.WithStreamProvider("anthropic"),
		models.WithStreamLimits(limits)), nil
}

func anthropicRequestTooLarge(operation string) error {
	return &models.Error{Kind: models.ErrorInvalidRequest, Provider: "anthropic", Operation: operation, Code: "request_too_large", Message: "encoded request exceeds provider limit"}
}

func (m *upstreamModel) encode(req *models.Request, stream bool) ([]byte, error) {
	if err := models.ValidateCapabilities(req, m.Capabilities(), "anthropic"); err != nil {
		return nil, err
	}
	return defaultanthropic.EncodeRequest(m.modelID, req, stream, m.provider.config.Policy.LenientMapping, m.provider.config.Limits.MaxProviderStateBytes)
}

func normalizeError(err error, operation string, capture *sdkhttp.Capture, redact []string) error {
	return sdkhttp.NormalizeError(err, "anthropic", operation, capture, redact, "official Anthropic SDK call failed", parseError)
}

func parseError(err error) (sdkhttp.ErrorInfo, bool) {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return sdkhttp.ErrorInfo{}, false
	}
	message, code := parseAnthropicErrorBody(apiErr.RawJSON())
	message = shared.FirstNonempty(message, http.StatusText(apiErr.StatusCode))
	code = shared.FirstNonempty(code, string(apiErr.Type()))
	info := sdkhttp.ErrorInfo{Status: apiErr.StatusCode, Code: code, Message: message, RequestID: apiErr.RequestID}
	if apiErr.Response != nil {
		info.Header = apiErr.Response.Header
	}
	return info, true
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
	return shared.FirstNonempty(envelope.Error.Message, envelope.Message), shared.FirstNonempty(envelope.Error.Type, envelope.Type)
}

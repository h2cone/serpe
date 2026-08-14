// Package google implements the official Google Gen AI SDK GenerateContent adapter.
package google

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"strconv"

	"google.golang.org/genai"

	"github.com/h2cone/serpe/core/models"
	defaultgoogle "github.com/h2cone/serpe/core/providers/internal/protocol/google"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/sdkhttp"
	"github.com/h2cone/serpe/core/providers/internal/transport/sse"
)

// Placeholder API key used when Config has no APIKey (Authenticator-only or
// unauthenticated custom endpoint). The sdkhttp bridge strips this value before
// the request leaves the process.
const placeholderAPIKey = "serpe-gemini-placeholder-not-a-secret"

// Provider is an immutable official-SDK Gemini GenerateContent provider.
type Provider struct {
	config shared.Config
	client *genai.Client
}

// New constructs an official Gemini provider without network access.
func New(config shared.Config) (*Provider, error) {
	endpoint := sdkhttp.GeminiEndpoint(config.BaseURL)
	bridge := sdkhttp.NewConfigBridge(config, "google", placeholderAPIKey)
	// Explicit Backend + APIKey + BaseURL/APIVersion so ambient env vars cannot
	// select Vertex, inject credentials, or override the endpoint. The bridge
	// still strips the SDK auth header and applies Config authentication.
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend:    genai.BackendGeminiAPI,
		APIKey:     placeholderAPIKey,
		HTTPClient: bridge.HTTPClient(),
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    endpoint.BaseURL,
			APIVersion: endpoint.APIVersion,
		},
	})
	if err != nil {
		return nil, &models.Error{
			Kind: models.ErrorInvalidRequest, Provider: "google", Operation: "construct",
			Message: "failed to construct official Gemini client: " + err.Error(),
		}
	}
	return &Provider{config: config, client: client}, nil
}

// ResolveModel validates and binds an upstream model identifier.
func (p *Provider) ResolveModel(upstreamModelID string) (models.Model, error) {
	if _, err := defaultgoogle.ValidateModelID(upstreamModelID); err != nil {
		return nil, err
	}
	return &upstreamModel{provider: p, modelID: upstreamModelID}, nil
}

type upstreamModel struct {
	provider *Provider
	modelID  string
}

func (m *upstreamModel) Capabilities() models.CapabilitySet {
	return defaultgoogle.ProtocolCapabilities()
}

func (m *upstreamModel) ToolResultPolicy() (models.ToolResultPolicy, bool) {
	return shared.ToolResultPolicy("google", m.modelID, m.Capabilities())
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
	payload, err := m.encode(req)
	if err != nil {
		return 0, err
	}
	return int64(len(payload)), nil
}

func (m *upstreamModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	return m.CompleteWithLimits(ctx, req, models.StreamLimits{})
}

func (m *upstreamModel) CompleteWithLimits(ctx context.Context, req *models.Request, limits models.StreamLimits) (*models.Response, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "google", "generate"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > m.MaxEncodedRequestBytes() {
		return nil, googleRequestTooLarge("generate")
	}
	ctx, capture := sdkhttp.PrepareCall(ctx, "generate", payload)

	config := &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{}}
	if req.RequestID != "" {
		config.HTTPOptions.Headers = http.Header{
			"X-Client-Request-ID": []string{req.RequestID},
			"X-Request-ID":        []string{req.RequestID},
		}
	}

	_, callErr := m.provider.client.Models.GenerateContent(ctx, m.modelID, nil, config)
	if callErr != nil {
		return nil, normalizeError(callErr, "generate", capture, m.provider.config.Redact)
	}
	requestID := capture.RequestID()
	raw := capture.Body
	if len(raw) == 0 {
		return nil, &models.Error{
			Kind: models.ErrorProtocol, Provider: "google", Operation: "generate",
			Code: "missing_response_body", Message: "official Gemini SDK returned no response body",
		}
	}
	decoded, err := defaultgoogle.DecodeResponseJSONWithLimits(raw, requestID, m.modelID, m.provider.config.Limits.MaxProviderStateBytes, shared.ToolCallLimitsFromStream(limits))
	if err != nil {
		return nil, err
	}
	return models.ApplyStreamLimitsToResponse(ctx, decoded, limits)
}

func (m *upstreamModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	return m.StreamWithLimits(ctx, req, models.StreamLimits{})
}

func (m *upstreamModel) StreamWithLimits(ctx context.Context, req *models.Request, limits models.StreamLimits) (models.Stream, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "google", "stream"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > m.MaxEncodedRequestBytes() {
		return nil, googleRequestTooLarge("stream")
	}
	streamCtx, capture, cancel := sdkhttp.PrepareStreamCall(ctx, "stream", payload)

	config := &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{}}
	if req.RequestID != "" {
		config.HTTPOptions.Headers = http.Header{
			"X-Client-Request-ID": []string{req.RequestID},
			"X-Request-ID":        []string{req.RequestID},
		}
	}

	seq := m.provider.client.Models.GenerateContentStream(streamCtx, m.modelID, nil, config)
	requestID := capture.RequestID()
	body := capture.StreamBody()
	if body == nil {
		// Setup failures are represented as a one-item iterator by the SDK.
		next, stop := iter.Pull2(seq)
		_, setupErr, _ := next()
		stop()
		cancel()
		if setupErr != nil {
			return nil, normalizeError(setupErr, "stream", capture, m.provider.config.Redact)
		}
		return nil, &models.Error{
			Kind: models.ErrorProtocol, Provider: "google", Operation: "stream",
			Code: "missing_response", Message: "official Gemini SDK returned no streaming response body",
		}
	}
	source := defaultgoogle.NewSSEStreamSource(
		sse.NewReaderWithClose(body, m.provider.config.Limits.MaxSSEEventBytes, func() error {
			cancel()
			return nil
		}), requestID, m.modelID, m.provider.config.Limits.MaxProviderStateBytes,
		shared.ToolCallLimitsFromStream(limits))
	return models.NewStream(ctx, source,
		models.WithStreamProvider("google"),
		models.WithStreamLimits(limits)), nil
}

func googleRequestTooLarge(operation string) error {
	return &models.Error{Kind: models.ErrorInvalidRequest, Provider: "google", Operation: operation, Code: "request_too_large", Message: "encoded request exceeds provider limit"}
}

func (m *upstreamModel) encode(req *models.Request) ([]byte, error) {
	if err := models.ValidateCapabilities(req, m.Capabilities(), "google"); err != nil {
		return nil, err
	}
	return defaultgoogle.EncodeRequest(req, m.provider.config.Policy.LenientMapping, m.provider.config.Limits.MaxProviderStateBytes)
}

func normalizeError(err error, operation string, capture *sdkhttp.Capture, redact []string) error {
	secrets := append(append([]string(nil), redact...), placeholderAPIKey)
	return sdkhttp.NormalizeError(err, "google", operation, capture, secrets, "official Gemini SDK call failed", parseError)
}

func parseError(err error) (sdkhttp.ErrorInfo, bool) {
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		message := apiErr.Message
		if message == "" {
			message = apiErr.Error()
		}
		code := apiErr.Status
		if code == "" {
			code = strconv.Itoa(apiErr.Code)
		}
		return sdkhttp.ErrorInfo{Status: apiErr.Code, Code: code, Message: message, CaptureHeader: true}, true
	}
	if errors.Is(err, io.EOF) {
		return sdkhttp.ErrorInfo{Kind: models.ErrorProtocol, Code: "unexpected_eof", Message: "official Gemini stream ended unexpectedly", OmitRequestID: true}, true
	}
	return sdkhttp.ErrorInfo{}, false
}

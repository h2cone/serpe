// Package gemini implements the official Google Gen AI SDK GenerateContent adapter.
package gemini

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"strconv"

	"google.golang.org/genai"

	"github.com/h2cone/ouro/core/models"
	defaultgemini "github.com/h2cone/ouro/core/providers/internal/protocol/gemini"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/sdkhttp"
	"github.com/h2cone/ouro/core/providers/internal/transport/sse"
)

// Placeholder API key used when Config has no APIKey (Authenticator-only or
// unauthenticated custom endpoint). The sdkhttp bridge strips this value before
// the request leaves the process.
const placeholderAPIKey = "ouro-gemini-placeholder-not-a-secret"

// Provider is an immutable official-SDK Gemini GenerateContent provider.
type Provider struct {
	config shared.Config
	client *genai.Client
}

// New constructs an official Gemini provider without network access.
func New(config shared.Config) (*Provider, error) {
	endpoint := sdkhttp.GeminiEndpoint(config.BaseURL)
	bridge := sdkhttp.NewConfigBridge(config, "gemini", placeholderAPIKey)
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
			Kind: models.ErrorInvalidRequest, Provider: "gemini", Operation: "construct",
			Message: "failed to construct official Gemini client: " + err.Error(),
		}
	}
	return &Provider{config: config, client: client}, nil
}

// Model validates and binds a physical model identifier.
func (p *Provider) Model(modelID string) (models.Model, error) {
	if _, err := defaultgemini.ValidateModelID(modelID); err != nil {
		return nil, err
	}
	return &model{provider: p, modelID: modelID}, nil
}

type model struct {
	provider *Provider
	modelID  string
}

func (m *model) Capabilities() models.CapabilitySet {
	return defaultgemini.ProtocolCapabilities()
}

func (m *model) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "gemini", "generate"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req)
	if err != nil {
		return nil, err
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
			Kind: models.ErrorProtocol, Provider: "gemini", Operation: "generate",
			Code: "missing_response_body", Message: "official Gemini SDK returned no response body",
		}
	}
	return defaultgemini.DecodeResponseJSON(raw, requestID, m.modelID, m.provider.config.Limits.MaxProviderStateBytes)
}

func (m *model) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	if err := sdkhttp.ValidateCall(ctx, req, "gemini", "stream"); err != nil {
		return nil, err
	}
	payload, err := m.encode(req)
	if err != nil {
		return nil, err
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
			Kind: models.ErrorProtocol, Provider: "gemini", Operation: "stream",
			Code: "missing_response", Message: "official Gemini SDK returned no streaming response body",
		}
	}
	source := defaultgemini.NewSSEStreamSource(
		sse.NewReaderWithClose(body, m.provider.config.Limits.MaxSSEEventBytes, func() error {
			cancel()
			return nil
		}), requestID, m.modelID, m.provider.config.Limits.MaxProviderStateBytes)
	return models.NewStream(ctx, source, models.WithStreamProvider("gemini")), nil
}

func (m *model) encode(req *models.Request) ([]byte, error) {
	if err := models.ValidateCapabilities(req, m.Capabilities(), "gemini"); err != nil {
		return nil, err
	}
	return defaultgemini.EncodeRequest(req, m.provider.config.Policy.LenientMapping, m.provider.config.Limits.MaxProviderStateBytes)
}

func normalizeError(err error, operation string, capture *sdkhttp.Capture, redact []string) error {
	secrets := append(append([]string(nil), redact...), placeholderAPIKey)
	return sdkhttp.NormalizeError(err, "gemini", operation, capture, secrets, "official Gemini SDK call failed", parseError)
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

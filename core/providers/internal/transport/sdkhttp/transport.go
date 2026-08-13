package sdkhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/httpx"
)

// AuthHeader names that may be set by an SDK from ambient or placeholder
// credentials and must be replaced by Config authentication.
var authHeaderNames = []string{
	"Authorization",
	"X-Api-Key",
	"X-API-Key",
	"x-api-key",
	"X-Goog-Api-Key",
	"X-Goog-API-Key",
	"x-goog-api-key",
	"Proxy-Authorization",
}

// BridgeConfig configures the HTTP bridge used by official SDKs.
type BridgeConfig struct {
	Doer         shared.Doer
	Authenticate shared.AuthenticateFunc
	Headers      http.Header
	Provider     string
	// BasePath is the caller-configured URL path. It lets the bridge remove a
	// vendor SDK's nested default version without changing the caller's version.
	BasePath           string
	Limits             shared.Limits
	RequireContentType bool
	// PlaceholderAuth is deleted from outgoing requests before Authenticate so
	// Gemini (or similar) placeholder keys never leave the wrapper.
	PlaceholderAuth []string
}

// Bridge is a Doer suitable for option.WithHTTPClient and *http.Client.Transport
// wrappers. It is safe for concurrent use.
type Bridge struct {
	cfg BridgeConfig
}

// NewBridge constructs an immutable SDK HTTP bridge.
func NewBridge(cfg BridgeConfig) *Bridge {
	if cfg.Headers != nil {
		cfg.Headers = cfg.Headers.Clone()
	} else {
		cfg.Headers = make(http.Header)
	}
	cfg.PlaceholderAuth = append([]string(nil), cfg.PlaceholderAuth...)
	if cfg.Limits.MaxRequestBytes == 0 {
		cfg.Limits.MaxRequestBytes = 32 << 20
	}
	if cfg.Limits.MaxResponseBytes == 0 {
		cfg.Limits.MaxResponseBytes = 32 << 20
	}
	if cfg.Limits.MaxStreamResponseBytes == 0 {
		cfg.Limits.MaxStreamResponseBytes = 32 << 20
	}
	if cfg.Limits.MaxErrorResponseBytes == 0 {
		cfg.Limits.MaxErrorResponseBytes = 64 << 10
	}
	return &Bridge{cfg: cfg}
}

// NewConfigBridge maps the normalized provider configuration into an SDK bridge.
func NewConfigBridge(config shared.Config, provider string, placeholderAuth ...string) *Bridge {
	basePath := ""
	if config.BaseURL != nil {
		basePath = config.BaseURL.Path
	}
	return NewBridge(BridgeConfig{
		Doer: config.HTTPClient, Authenticate: config.Authenticate, Headers: config.Headers,
		Provider: provider, BasePath: basePath, Limits: config.Limits,
		RequireContentType: !config.Policy.IgnoreContentType, PlaceholderAuth: placeholderAuth,
	})
}

// HTTPClient returns an *http.Client whose Transport delegates to Bridge.Do.
// SDKs that require *http.Client can use this without losing Doer semantics.
func (b *Bridge) HTTPClient() *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return b.Do(req)
	})}
}

// Do implements shared.Doer / option.HTTPClient.
func (b *Bridge) Do(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: b.cfg.Provider, Operation: "transport", Message: "HTTP request is nil"}
	}
	ctx := req.Context()
	// Capture values before optionally rebinding to the caller's context. Vendor
	// SDKs may wrap the context with their own timeout; we neutralize that by
	// restoring the caller context, but must keep per-call metadata.
	metadata := callFrom(ctx)
	var capture *Capture
	var operation string
	var requestBody []byte
	var replaceRequestBody bool
	if metadata != nil {
		capture, operation = metadata.capture, metadata.operation
		requestBody, replaceRequestBody = metadata.requestBody, metadata.replaceBody
		// Restore the caller context while retaining the single metadata snapshot
		// for SDK wrappers and any nested transport invocation.
		ctx = context.WithValue(metadata.caller, callKey{}, metadata)
		req = req.WithContext(ctx)
	}
	if operation == "" {
		if strings.Contains(strings.ToLower(req.Header.Get("Accept")), "text/event-stream") {
			operation = "stream"
		} else {
			operation = "generate"
		}
	}

	// Clone so we never mutate the SDK's request header map in place across retries.
	out := req.Clone(ctx)
	if out.URL != nil {
		out.URL.Path = httpx.JoinEndpointPath(b.cfg.BasePath, out.URL.Path)
		out.URL.RawPath = ""
	}
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	if replaceRequestBody {
		payload := append([]byte(nil), requestBody...)
		out.Body = io.NopCloser(bytes.NewReader(payload))
		out.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
		out.ContentLength = int64(len(payload))
		out.TransferEncoding = nil
		out.Header.Set("Content-Type", "application/json")
		out.Header.Del("Content-Encoding")
	}
	if err := boundRequestBody(out, b.cfg.Limits.MaxRequestBytes, b.cfg.Provider, operation); err != nil {
		return nil, err
	}

	// Drop SDK/ambient/placeholder credentials before applying Config auth.
	for _, name := range authHeaderNames {
		out.Header.Del(name)
	}
	stripSecrets(out.Header, b.cfg.PlaceholderAuth)

	// Merge Config headers without clobbering SDK-required non-auth headers
	// that the caller did not set (Content-Type/Accept are reserved for the SDK
	// or already validated at Config construction).
	for name, values := range b.cfg.Headers {
		if httpx.ReservedHeader(name) {
			continue
		}
		out.Header.Del(name)
		for _, value := range values {
			out.Header.Add(name, value)
		}
	}

	if b.cfg.Authenticate != nil {
		if err := b.cfg.Authenticate(ctx, out); err != nil {
			return nil, &models.Error{
				Kind: models.ErrorAuthentication, Provider: b.cfg.Provider, Operation: operation,
				Message: "request authentication failed", Cause: err,
			}
		}
	}

	// Ensure placeholder values never survive authentication either.
	stripSecrets(out.Header, b.cfg.PlaceholderAuth)

	response, err := b.cfg.Doer.Do(out)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, models.ContextError(b.cfg.Provider, operation, ctxErr)
		}
		return nil, &models.Error{
			Kind: models.ErrorUnavailable, Provider: b.cfg.Provider, Operation: operation,
			Message: "HTTP transport failed", Cause: err, Retryable: true,
		}
	}
	if response == nil {
		return nil, &models.Error{
			Kind: models.ErrorProtocol, Provider: b.cfg.Provider, Operation: operation,
			Code: "missing_response", Message: "HTTP transport returned no response",
		}
	}

	if capture != nil {
		capture.Header = response.Header.Clone()
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if err := b.prepareErrorResponse(response, operation); err != nil {
			return nil, err
		}
		return response, nil
	}
	if response.Body == nil {
		return nil, &models.Error{
			Kind: models.ErrorProtocol, Provider: b.cfg.Provider, Operation: operation,
			Code: "missing_body", HTTPStatus: response.StatusCode, RequestID: httpx.RequestID(response.Header),
			Message: "successful response has no body",
		}
	}
	responseLimit := b.cfg.Limits.MaxResponseBytes
	if strings.Contains(strings.ToLower(out.Header.Get("Accept")), "text/event-stream") {
		responseLimit = b.cfg.Limits.MaxStreamResponseBytes
	}
	if err := httpx.PrepareResponseBody(response, responseLimit, b.cfg.Provider, operation); err != nil {
		httpx.DrainAndClose(response.Body, 0)
		return nil, err
	}

	stream := strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") ||
		strings.Contains(strings.ToLower(out.Header.Get("Accept")), "text/event-stream")
	if b.cfg.RequireContentType {
		expected := "application/json"
		if stream {
			expected = "text/event-stream"
		}
		if !httpx.HasMediaType(response.Header.Get("Content-Type"), expected) {
			httpx.DrainAndClose(response.Body, 4096)
			return nil, &models.Error{
				Kind: models.ErrorProtocol, Provider: b.cfg.Provider, Operation: operation,
				Code: "unexpected_content_type", HTTPStatus: response.StatusCode,
				RequestID: httpx.RequestID(response.Header),
				Message:   fmt.Sprintf("expected %s response", expected),
			}
		}
	}
	if stream {
		if capture != nil {
			capture.setStreamBody(response.Body)
		}
	} else {
		data, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		if err := shared.ValidateUnaryJSON(data); err != nil {
			return nil, &models.Error{
				Kind: models.ErrorProtocol, Provider: b.cfg.Provider, Operation: operation,
				Code: "invalid_json", HTTPStatus: response.StatusCode, RequestID: httpx.RequestID(response.Header),
				Message: "successful response is not strict JSON", Cause: err,
			}
		}
		if capture != nil {
			capture.Body = append([]byte(nil), data...)
		}
		response.Body = io.NopCloser(bytes.NewReader(data))
		response.ContentLength = int64(len(data))
	}
	return response, nil
}

func (b *Bridge) prepareErrorResponse(response *http.Response, operation string) error {
	if response.Body == nil {
		kind, retryable := httpx.StatusKind(response.StatusCode)
		return &models.Error{Kind: kind, Provider: b.cfg.Provider, Operation: operation, HTTPStatus: response.StatusCode,
			Message: http.StatusText(response.StatusCode), RequestID: httpx.RequestID(response.Header), Retryable: retryable}
	}
	if err := httpx.PrepareResponseBody(response, b.cfg.Limits.MaxErrorResponseBytes, b.cfg.Provider, operation); err != nil {
		httpx.DrainAndClose(response.Body, 0)
		return err
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(data)
	looksJSON := httpx.HasMediaType(response.Header.Get("Content-Type"), "application/json") ||
		strings.HasSuffix(strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])), "+json") ||
		len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	if looksJSON {
		if err := shared.ValidateErrorJSON(data); err != nil {
			return &models.Error{Kind: models.ErrorProtocol, Provider: b.cfg.Provider, Operation: operation,
				Code: "invalid_json", HTTPStatus: response.StatusCode, RequestID: httpx.RequestID(response.Header),
				Message: "provider returned an invalid JSON error response", Cause: err}
		}
		response.Body = io.NopCloser(bytes.NewReader(data))
		response.ContentLength = int64(len(data))
		return nil
	}
	kind, retryable := httpx.StatusKind(response.StatusCode)
	message := http.StatusText(response.StatusCode)
	if len(trimmed) > 0 {
		message = "provider returned a non-JSON error response"
	}
	return &models.Error{Kind: kind, Provider: b.cfg.Provider, Operation: operation, HTTPStatus: response.StatusCode,
		Message: message, RequestID: httpx.RequestID(response.Header), RetryAfter: httpx.RetryAfter(response.Header.Get("Retry-After")), Retryable: retryable}
}

func boundRequestBody(request *http.Request, limit int64, provider, operation string) error {
	if request.Body == nil {
		return nil
	}
	if request.ContentLength > limit {
		_ = request.Body.Close()
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: operation, Code: "request_too_large", Message: "encoded request exceeds provider limit"}
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	closeErr := request.Body.Close()
	if err != nil || closeErr != nil {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: operation, Code: "request_read_error", Message: "failed to inspect encoded request", Cause: errors.Join(err, closeErr)}
	}
	if int64(len(payload)) > limit {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: operation, Code: "request_too_large", Message: "encoded request exceeds provider limit"}
	}
	if err := shared.ValidateUnaryJSON(payload); err != nil {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: operation, Code: "invalid_request_json", Message: "SDK encoded non-strict JSON", Cause: err}
	}
	request.Body = io.NopCloser(bytes.NewReader(payload))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }
	request.ContentLength = int64(len(payload))
	request.TransferEncoding = nil
	return nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func stripSecrets(header http.Header, secrets []string) {
	for name, values := range header {
		for _, value := range values {
			for _, secret := range secrets {
				if secret != "" && strings.Contains(value, secret) {
					header.Del(name)
					break
				}
			}
		}
	}
}

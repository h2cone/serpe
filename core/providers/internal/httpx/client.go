// Package httpx implements bounded, credential-safe provider HTTP plumbing.
package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

// Config configures an immutable HTTP client helper.
type Config struct {
	BaseURL               *url.URL
	Doer                  shared.Doer
	Authenticate          shared.AuthenticateFunc
	Headers               http.Header
	Provider              string
	MaxErrorResponseBytes int64
	RequireContentType    bool
	Redact                []string
}

// Client safely joins fixed endpoints and performs normalized status handling.
type Client struct {
	baseURL               url.URL
	doer                  shared.Doer
	authenticate          shared.AuthenticateFunc
	headers               http.Header
	provider              string
	maxErrorResponseBytes int64
	requireContentType    bool
	redact                []string
}

// New creates an immutable HTTP helper.
func New(config Config) *Client {
	client := &Client{
		doer:                  config.Doer,
		authenticate:          config.Authenticate,
		headers:               config.Headers.Clone(),
		provider:              config.Provider,
		maxErrorResponseBytes: config.MaxErrorResponseBytes,
		requireContentType:    config.RequireContentType,
		redact:                append([]string(nil), config.Redact...),
	}
	if config.BaseURL != nil {
		client.baseURL = *config.BaseURL
	}
	return client
}

// Do posts a JSON payload to a fixed relative endpoint. A successful response
// body remains owned by the caller; non-success bodies are bounded and closed.
func (c *Client) Do(ctx context.Context, operation, endpoint string, query url.Values, payload []byte, stream bool, requestID string, extra http.Header) (*http.Response, error) {
	if !validEndpoint(endpoint) {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: c.provider, Operation: operation, Code: "invalid_endpoint", Message: "provider endpoint is not a fixed absolute path"}
	}
	u := c.baseURL
	u.Path = joinEndpointPath(u.Path, endpoint)
	u.RawPath = ""
	u.RawQuery = query.Encode()
	u.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: c.provider, Operation: operation, Message: "failed to construct HTTP request", Cause: err}
	}
	request.Header = c.headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	for key, values := range extra {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	if c.authenticate != nil {
		if err := c.authenticate(ctx, request); err != nil {
			return nil, &models.Error{Kind: models.ErrorAuthentication, Provider: c.provider, Operation: operation, Message: "request authentication failed", Cause: err}
		}
	}
	response, err := c.doer.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, models.ContextError(c.provider, operation, ctxErr)
		}
		return nil, &models.Error{Kind: models.ErrorUnavailable, Provider: c.provider, Operation: operation, Message: "HTTP transport failed", Cause: err, Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, DecodeError(response, c.provider, operation, c.maxErrorResponseBytes, c.redact)
	}
	expected := "application/json"
	if stream {
		expected = "text/event-stream"
	}
	if c.requireContentType && !hasMediaType(response.Header.Get("Content-Type"), expected) {
		DrainAndClose(response.Body, 4096)
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: c.provider, Operation: operation, Code: "unexpected_content_type", HTTPStatus: response.StatusCode, RequestID: RequestID(response.Header), Message: fmt.Sprintf("expected %s response", expected)}
	}
	return response, nil
}

func joinEndpointPath(basePath, endpoint string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	if basePath == "" {
		return endpoint
	}
	segments := strings.Split(strings.TrimPrefix(endpoint, "/"), "/")
	for count := len(segments); count > 0; count-- {
		prefix := "/" + strings.Join(segments[:count], "/")
		if strings.HasSuffix(basePath, prefix) {
			return basePath + strings.TrimPrefix(endpoint, prefix)
		}
	}
	return basePath + endpoint
}

func validEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "/") && !strings.HasPrefix(endpoint, "//") && !strings.ContainsAny(endpoint, "\\?#")
}

func hasMediaType(value, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, expected)
}

// DrainAndClose performs a bounded drain and closes body.
func DrainAndClose(body io.ReadCloser, limit int64) {
	if body == nil {
		return
	}
	if limit > 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(body, limit))
	}
	_ = body.Close()
}

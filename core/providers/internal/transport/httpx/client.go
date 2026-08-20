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

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers/internal/shared"
)

// Config configures an immutable HTTP client helper.
type Config struct {
	BaseURL                *url.URL
	Doer                   shared.Doer
	Authenticate           shared.AuthenticateFunc
	Headers                http.Header
	Provider               string
	MaxErrorResponseBytes  int64
	MaxResponseBytes       int64
	MaxStreamResponseBytes int64
	RequireContentType     bool
	Redact                 []string
}

// Client safely joins fixed endpoints and performs normalized status handling.
type Client struct {
	baseURL url.URL
	config  Config
}

// New creates an immutable HTTP helper.
func New(config Config) *Client {
	if config.MaxStreamResponseBytes == 0 {
		config.MaxStreamResponseBytes = config.MaxResponseBytes
	}
	config.Headers = config.Headers.Clone()
	config.Redact = append([]string(nil), config.Redact...)
	client := &Client{config: config}
	if config.BaseURL != nil {
		client.baseURL = *config.BaseURL
	}
	return client
}

// Do posts a JSON payload to a fixed relative endpoint. A successful response
// body remains owned by the caller; non-success bodies are bounded and closed.
func (c *Client) Do(ctx context.Context, operation, endpoint string, query url.Values, payload []byte, stream bool, requestID string, extra http.Header) (*http.Response, error) {
	if !validEndpoint(endpoint) {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: c.config.Provider, Operation: operation, Code: "invalid_endpoint", Message: "provider endpoint is not a fixed absolute path"}
	}
	u := c.baseURL
	u.Path = JoinEndpointPath(u.Path, endpoint)
	u.RawPath = ""
	u.RawQuery = query.Encode()
	u.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Provider: c.config.Provider, Operation: operation, Message: "failed to construct HTTP request", Cause: err}
	}
	request.Header = c.config.Headers.Clone()
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
	if c.config.Authenticate != nil {
		if err := c.config.Authenticate(ctx, request); err != nil {
			return nil, &models.Error{Kind: models.ErrorAuthentication, Provider: c.config.Provider, Operation: operation, Message: "request authentication failed", Cause: err}
		}
	}
	response, err := c.config.Doer.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, models.ContextError(c.config.Provider, operation, ctxErr)
		}
		return nil, &models.Error{Kind: models.ErrorUnavailable, Provider: c.config.Provider, Operation: operation, Message: "HTTP transport failed", Cause: err, Retryable: true}
	}
	if response == nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: c.config.Provider, Operation: operation, Code: "missing_response", Message: "HTTP transport returned no response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, DecodeError(response, c.config.Provider, operation, c.config.MaxErrorResponseBytes, c.config.Redact)
	}
	expected := "application/json"
	if stream {
		expected = "text/event-stream"
	}
	if c.config.RequireContentType && !HasMediaType(response.Header.Get("Content-Type"), expected) {
		DrainAndClose(response.Body, 4096)
		return nil, &models.Error{Kind: models.ErrorProtocol, Provider: c.config.Provider, Operation: operation, Code: "unexpected_content_type", HTTPStatus: response.StatusCode, RequestID: RequestID(response.Header), Message: fmt.Sprintf("expected %s response", expected)}
	}
	if stream {
		if encodingErr := PrepareResponseBody(response, c.config.MaxStreamResponseBytes, c.config.Provider, operation); encodingErr != nil {
			DrainAndClose(response.Body, 0)
			return nil, encodingErr
		}
	}
	return response, nil
}

// JoinEndpointPath joins a configured base path to a fixed provider endpoint.
// When the base path already ends in an API version segment, that caller
// supplied version replaces the endpoint's leading default version.
func JoinEndpointPath(basePath, endpoint string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	if basePath == "" {
		return endpoint
	}
	if _, _, versioned := SplitAPIVersionSuffix(basePath); versioned {
		// Official SDKs may already have resolved the configured base path
		// before adding their own version. Remove only that immediately nested
		// version, leaving the caller's suffix in place.
		if endpoint == basePath {
			return endpoint
		}
		if strings.HasPrefix(endpoint, basePath+"/") {
			remainder := endpoint[len(basePath):]
			if unversioned, ok := trimLeadingAPIVersion(remainder); ok {
				return basePath + unversioned
			}
			return endpoint
		}
		if unversioned, ok := trimLeadingAPIVersion(endpoint); ok {
			return basePath + unversioned
		}
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

// SplitAPIVersionSuffix returns a final version path segment and the path that
// precedes it. Version segments start with a lowercase "v" and a digit, and
// may continue with ASCII letters or digits (for example v2 or v1beta1).
func SplitAPIVersionSuffix(path string) (prefix, version string, ok bool) {
	path = strings.TrimSuffix(path, "/")
	prefix, version, found := strings.CutLast(path, "/")
	if !found {
		if !isAPIVersionSegment(path) {
			return path, "", false
		}
		return "", path, true
	}
	if !isAPIVersionSegment(version) {
		return path, "", false
	}
	return prefix, version, true
}

func trimLeadingAPIVersion(path string) (string, bool) {
	if !strings.HasPrefix(path, "/") {
		return path, false
	}
	remainder := path[1:]
	segment, rest, found := strings.Cut(remainder, "/")
	if !isAPIVersionSegment(segment) {
		return path, false
	}
	if !found {
		return "", true
	}
	return "/" + rest, true
}

func isAPIVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' || segment[1] < '0' || segment[1] > '9' {
		return false
	}
	for index := 2; index < len(segment); index++ {
		char := segment[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}

func validEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "/") && !strings.HasPrefix(endpoint, "//") && !strings.ContainsAny(endpoint, "\\?#")
}

// HasMediaType reports whether a Content-Type value has the expected media
// type, ignoring parameters and ASCII case.
func HasMediaType(value, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, expected)
}

// ReservedHeader reports whether a Config header is owned by provider
// transport policy and therefore cannot be supplied as a custom header.
func ReservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "x-api-key", "x-goog-api-key",
		"host", "content-length", "content-type", "accept",
		"anthropic-version", "x-request-id", "x-client-request-id":
		return true
	default:
		return false
	}
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

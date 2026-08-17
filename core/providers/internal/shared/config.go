// Package shared contains the explicitly named immutable configuration shared
// by provider adapters.
package shared

import (
	"context"
	"net/http"
	"net/url"
)

// Doer is the internal HTTP client boundary.
type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}

// AuthenticateFunc applies provider authentication.
type AuthenticateFunc func(context.Context, *http.Request) error

// Limits are normalized positive transport byte limits.
type Limits struct {
	MaxRequestBytes        int64
	MaxResponseBytes       int64
	MaxStreamResponseBytes int64
	MaxErrorResponseBytes  int64
	MaxSSEEventBytes       int64
	MaxProviderStateBytes  int64
}

// Policy is a normalized internal policy snapshot.
type Policy struct {
	LenientMapping     bool
	IgnoreUnknownEvent bool
	IgnoreContentType  bool
}

// Config is defensively copied and immutable after construction.
type Config struct {
	Provider     string
	BaseURL      *url.URL
	HTTPClient   Doer
	Authenticate AuthenticateFunc
	Headers      http.Header
	Limits       Limits
	Policy       Policy
	Redact       []string
}

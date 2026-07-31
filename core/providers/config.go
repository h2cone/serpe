package providers

import (
	"net/http"
)

// Doer matches http.Client.Do and permits test transports and instrumented
// clients without requiring a concrete *http.Client.
type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Limits bounds independently allocated response data. Zero fields receive
// conservative defaults.
type Limits struct {
	MaxResponseBytes      int64
	MaxErrorResponseBytes int64
	MaxSSEEventBytes      int64
	MaxProviderStateBytes int64
}

// Config creates one immutable Provider. Model ID is deliberately absent and
// is supplied later to Provider.Model.
type Config struct {
	Protocol Protocol
	// BaseURL may be an origin or a path prefix such as
	// https://api.openai.com/v1. A version prefix shared with the selected
	// protocol endpoint is joined only once.
	BaseURL       string
	APIKey        string
	Authenticator Authenticator
	HTTPClient    Doer
	Limits        Limits
	Policy        Policy
	Headers       http.Header
}

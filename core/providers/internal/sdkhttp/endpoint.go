// Package sdkhttp bridges Config transport, authentication, limits, and
// response capture into vendor official SDKs without ambient credentials,
// automatic retries, or hidden whole-call timeouts.
//
// The official adapters neutralize vendor behavior as follows: OpenAI uses
// service constructors (which do not read OPENAI_* defaults), an empty SDK key,
// and zero retries; Anthropic disables environment defaults, uses zero retries,
// and gives its mandatory unary timeout a high ceiling while Bridge restores
// the caller context; Gemini pins the backend, endpoint, API version, and a
// stripped placeholder key. Gemini's canonical JSON body is attached with
// PrepareCall and installed only at the final Bridge boundary. Successful
// streams expose their raw response body so Ouro's bounded SSE parser remains
// the single event parser for every driver.
package sdkhttp

import (
	"net/url"
	"strings"
)

// Endpoint describes the base URL and API version expected by a vendor SDK.
type Endpoint struct {
	// BaseURL is the origin plus any non-version path prefix, ready for the
	// SDK. Trailing slashes are normalized per vendor conventions.
	BaseURL string
	// APIVersion is set when the SDK needs an explicit version (Gemini).
	APIVersion string
}

// OpenAIEndpoint normalizes a configured base URL so the OpenAI SDK receives a
// base that contains exactly one trailing "/v1/" segment.
func OpenAIEndpoint(base *url.URL) Endpoint {
	if base == nil {
		return Endpoint{BaseURL: "https://api.openai.com/v1/"}
	}
	cloned := *base
	path := strings.TrimSuffix(cloned.Path, "/")
	if path == "" {
		path = "/v1"
	} else if !strings.HasSuffix(path, "/v1") {
		// Keep custom prefixes; append /v1 when the final segment is not v1.
		segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(segments) == 0 || segments[len(segments)-1] != "v1" {
			path = path + "/v1"
		}
	}
	cloned.Path = path + "/"
	cloned.RawPath = ""
	cloned.RawQuery = ""
	cloned.Fragment = ""
	return Endpoint{BaseURL: cloned.String()}
}

// AnthropicEndpoint normalizes a configured base URL so the Anthropic SDK does
// not receive a duplicated trailing "/v1" (the SDK appends "v1/messages").
func AnthropicEndpoint(base *url.URL) Endpoint {
	if base == nil {
		return Endpoint{BaseURL: "https://api.anthropic.com/"}
	}
	cloned := *base
	path := strings.TrimSuffix(cloned.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path = strings.TrimSuffix(path, "/v1")
	}
	if path == "" {
		cloned.Path = "/"
	} else {
		cloned.Path = path + "/"
	}
	cloned.RawPath = ""
	cloned.RawQuery = ""
	cloned.Fragment = ""
	return Endpoint{BaseURL: cloned.String()}
}

// GeminiEndpoint normalizes a configured base URL and forces API version
// "v1beta" for the Developer API.
func GeminiEndpoint(base *url.URL) Endpoint {
	if base == nil {
		return Endpoint{BaseURL: "https://generativelanguage.googleapis.com/", APIVersion: "v1beta"}
	}
	cloned := *base
	path := strings.TrimSuffix(cloned.Path, "/")
	if strings.HasSuffix(path, "/v1beta") {
		path = strings.TrimSuffix(path, "/v1beta")
	} else if strings.HasSuffix(path, "/v1") {
		path = strings.TrimSuffix(path, "/v1")
	}
	if path == "" {
		cloned.Path = "/"
	} else {
		cloned.Path = path + "/"
	}
	cloned.RawPath = ""
	cloned.RawQuery = ""
	cloned.Fragment = ""
	return Endpoint{BaseURL: cloned.String(), APIVersion: "v1beta"}
}

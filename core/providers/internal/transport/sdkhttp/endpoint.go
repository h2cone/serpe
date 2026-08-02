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
	return normalizeEndpoint(base, "https://api.openai.com/v1/", "", func(path string) string {
		if path == "" {
			return "/v1"
		}
		if !strings.HasSuffix(path, "/v1") {
			return path + "/v1"
		}
		return path
	})
}

// AnthropicEndpoint normalizes a configured base URL so the Anthropic SDK does
// not receive a duplicated trailing "/v1" (the SDK appends "v1/messages").
func AnthropicEndpoint(base *url.URL) Endpoint {
	return normalizeEndpoint(base, "https://api.anthropic.com/", "", func(path string) string {
		return strings.TrimSuffix(path, "/v1")
	})
}

// GeminiEndpoint normalizes a configured base URL and forces API version
// "v1beta" for the Developer API.
func GeminiEndpoint(base *url.URL) Endpoint {
	return normalizeEndpoint(base, "https://generativelanguage.googleapis.com/", "v1beta", func(path string) string {
		if strings.HasSuffix(path, "/v1beta") {
			return strings.TrimSuffix(path, "/v1beta")
		}
		return strings.TrimSuffix(path, "/v1")
	})
}

func normalizeEndpoint(base *url.URL, fallback, version string, normalizePath func(string) string) Endpoint {
	if base == nil {
		return Endpoint{BaseURL: fallback, APIVersion: version}
	}
	cloned := *base
	cloned.Path = normalizePath(strings.TrimSuffix(cloned.Path, "/")) + "/"
	cloned.RawPath = ""
	cloned.RawQuery = ""
	cloned.Fragment = ""
	return Endpoint{BaseURL: cloned.String(), APIVersion: version}
}

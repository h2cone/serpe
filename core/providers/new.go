package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/h2cone/ouro/core/providers/internal/anthropic"
	"github.com/h2cone/ouro/core/providers/internal/gemini/generatecontent"
	"github.com/h2cone/ouro/core/providers/internal/openai/chatcompletions"
	"github.com/h2cone/ouro/core/providers/internal/openai/responses"
	"github.com/h2cone/ouro/core/providers/internal/shared"
)

const (
	defaultMaxResponseBytes      = 32 << 20
	defaultMaxErrorResponseBytes = 64 << 10
	defaultMaxSSEEventBytes      = 2 << 20
	defaultMaxProviderStateBytes = 2 << 20
)

// New validates and freezes configuration, selects one concrete adapter, and
// returns a Provider. It performs no network operation.
func New(config Config) (Provider, error) {
	internal, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	switch config.Protocol {
	case OpenAIChatCompletions:
		return chatcompletions.New(internal), nil
	case OpenAIResponses:
		return responses.New(internal), nil
	case AnthropicMessages:
		return anthropic.New(internal), nil
	case GeminiGenerateContent:
		return generatecontent.New(internal), nil
	default:
		return nil, fmt.Errorf("providers: unknown protocol %q", config.Protocol)
	}
}

func normalizeConfig(config Config) (shared.Config, error) {
	provider := config.Protocol.providerName()
	if provider == "" {
		return shared.Config{}, fmt.Errorf("providers: missing or unknown protocol %q", config.Protocol)
	}
	if config.APIKey != "" && config.Authenticator != nil {
		return shared.Config{}, fmt.Errorf("providers: APIKey and Authenticator are mutually exclusive")
	}
	base := config.BaseURL
	if base == "" {
		base = config.Protocol.defaultBaseURL()
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return shared.Config{}, fmt.Errorf("providers: BaseURL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return shared.Config{}, fmt.Errorf("providers: BaseURL scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return shared.Config{}, fmt.Errorf("providers: BaseURL must not contain user info, query, or fragment")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""

	headers := config.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	if err := validateHeaders(headers); err != nil {
		return shared.Config{}, err
	}
	policy, valid := config.Policy.normalized()
	if !valid {
		return shared.Config{}, fmt.Errorf("providers: invalid policy")
	}
	limits, err := normalizeLimits(config.Limits)
	if err != nil {
		return shared.Config{}, err
	}
	doer := config.HTTPClient
	if doer == nil {
		transport := defaultTransport()
		doer = &http.Client{Transport: transport}
	}
	authenticate := shared.AuthenticateFunc(nil)
	redact := []string(nil)
	if config.Authenticator != nil {
		authenticator := config.Authenticator
		authenticate = func(ctx context.Context, request *http.Request) error {
			return authenticator.Authenticate(ctx, request)
		}
	} else if config.APIKey != "" {
		key := config.APIKey
		redact = []string{key}
		authenticate = func(_ context.Context, request *http.Request) error {
			switch config.Protocol {
			case OpenAIChatCompletions, OpenAIResponses:
				request.Header.Set("Authorization", "Bearer "+key)
			case AnthropicMessages:
				request.Header.Set("X-API-Key", key)
			case GeminiGenerateContent:
				request.Header.Set("X-Goog-API-Key", key)
			}
			return nil
		}
	}
	return shared.Config{
		Protocol: string(config.Protocol), Provider: provider, BaseURL: parsed,
		HTTPClient: doer, Authenticate: authenticate, Headers: headers,
		Limits: limits,
		Policy: shared.Policy{
			LenientMapping:     policy.Mapping == MappingLenient,
			IgnoreUnknownEvent: policy.UnknownEvent == UnknownEventIgnore,
			IgnoreContentType:  policy.ContentType == ContentTypeIgnore,
		},
		Redact: redact,
	}, nil
}

func defaultTransport() *http.Transport {
	if standard, ok := http.DefaultTransport.(*http.Transport); ok {
		transport := standard.Clone()
		if transport.ResponseHeaderTimeout == 0 {
			transport.ResponseHeaderTimeout = 30 * time.Second
		}
		if transport.MaxIdleConnsPerHost < 16 {
			transport.MaxIdleConnsPerHost = 16
		}
		return transport
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func normalizeLimits(input Limits) (shared.Limits, error) {
	values := []int64{input.MaxResponseBytes, input.MaxErrorResponseBytes, input.MaxSSEEventBytes, input.MaxProviderStateBytes}
	for _, value := range values {
		if value < 0 {
			return shared.Limits{}, fmt.Errorf("providers: byte limits must not be negative")
		}
	}
	if input.MaxResponseBytes == 0 {
		input.MaxResponseBytes = defaultMaxResponseBytes
	}
	if input.MaxErrorResponseBytes == 0 {
		input.MaxErrorResponseBytes = defaultMaxErrorResponseBytes
	}
	if input.MaxSSEEventBytes == 0 {
		input.MaxSSEEventBytes = defaultMaxSSEEventBytes
	}
	if input.MaxProviderStateBytes == 0 {
		input.MaxProviderStateBytes = defaultMaxProviderStateBytes
	}
	return shared.Limits{
		MaxResponseBytes:      input.MaxResponseBytes,
		MaxErrorResponseBytes: input.MaxErrorResponseBytes,
		MaxSSEEventBytes:      input.MaxSSEEventBytes,
		MaxProviderStateBytes: input.MaxProviderStateBytes,
	}, nil
}

var reservedHeaders = map[string]struct{}{
	"authorization": {}, "proxy-authorization": {}, "x-api-key": {}, "x-goog-api-key": {},
	"host": {}, "content-length": {}, "content-type": {}, "accept": {},
	"anthropic-version": {}, "x-request-id": {}, "x-client-request-id": {},
}

func validateHeaders(headers http.Header) error {
	for name, values := range headers {
		canonical := strings.ToLower(name)
		if _, reserved := reservedHeaders[canonical]; reserved {
			return fmt.Errorf("providers: custom header %q is reserved", name)
		}
		if name == "" || strings.IndexFunc(name, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r)
		}) >= 0 {
			return fmt.Errorf("providers: custom header name %q is invalid", name)
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("providers: custom header %q contains a newline", name)
			}
		}
	}
	return nil
}

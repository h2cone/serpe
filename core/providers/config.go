package providers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/h2cone/serpe/core/providers/internal/shared"
	"github.com/h2cone/serpe/core/providers/internal/transport/httpx"
)

const (
	defaultMaxResponseBytes       = 32 << 20
	defaultMaxStreamResponseBytes = 32 << 20
	defaultMaxErrorResponseBytes  = 64 << 10
	defaultMaxSSEEventBytes       = 2 << 20
	defaultMaxProviderStateBytes  = 2 << 20
	defaultMaxRequestBytes        = 32 << 20
	defaultMaxToolCalls           = 128
	defaultMaxCallIDBytes         = 1 << 10
	defaultMaxToolNameBytes       = 1 << 10
	defaultMaxArgumentsBytes      = 16 << 20
	defaultMaxBatchArgumentBytes  = 16 << 20
)

// Doer matches http.Client.Do and permits test transports and instrumented
// clients without requiring a concrete *http.Client.
type Doer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Authenticator adds current authentication material to an outgoing request.
// Implementations must be safe for concurrent use and must not include secrets
// in returned errors.
type Authenticator interface {
	Authenticate(ctx context.Context, request *http.Request) error
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(context.Context, *http.Request) error

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, request *http.Request) error {
	return f(ctx, request)
}

// TokenSource returns a current bearer token and is safe for concurrent use.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to TokenSource.
type TokenSourceFunc func(context.Context) (string, error)

// Token calls f.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) {
	return f(ctx)
}

type bearerAuthenticator struct {
	source TokenSource
}

// BearerAuthenticator creates a rotating bearer-token authenticator.
func BearerAuthenticator(source TokenSource) Authenticator {
	return &bearerAuthenticator{source: source}
}

func (a *bearerAuthenticator) Authenticate(ctx context.Context, request *http.Request) error {
	if a == nil || a.source == nil {
		return fmt.Errorf("bearer authenticator: token source is nil")
	}
	token, err := a.source.Token(ctx)
	if err != nil {
		return fmt.Errorf("bearer authenticator: token source failed: %w", err)
	}
	if token == "" {
		return fmt.Errorf("bearer authenticator: token source returned an empty token")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// Limits bounds independently allocated response data. Zero fields receive
// conservative defaults.
type Limits struct {
	MaxRequestBytes        int64
	MaxResponseBytes       int64
	MaxStreamResponseBytes int64
	MaxErrorResponseBytes  int64
	MaxSSEEventBytes       int64
	MaxProviderStateBytes  int64
	MaxToolCalls           int
	MaxCallIDBytes         int64
	MaxToolNameBytes       int64
	MaxArgumentsBytes      int64
	MaxBatchArgumentBytes  int64
}

// Config creates one immutable Provider. Model ID is deliberately absent and
// is supplied later to Provider.Model.
type Config struct {
	Protocol Protocol
	// Driver selects default HTTP/SSE adapters or the official vendor SDK.
	// The zero value is DriverDefault and is fully equivalent to an explicit
	// DriverDefault. Unknown values fail in New.
	Driver Driver
	// BaseURL may be an origin or a path prefix such as
	// https://api.openai.com/v1. A trailing API version supplied by the caller
	// takes precedence over the protocol default and is joined only once.
	BaseURL       string
	APIKey        string
	Authenticator Authenticator
	HTTPClient    Doer
	Limits        Limits
	Policy        Policy
	Headers       http.Header
}

func normalizeConfig(config Config, spec protocolSpec) (shared.Config, error) {
	if config.APIKey != "" && config.Authenticator != nil {
		return shared.Config{}, fmt.Errorf("providers: APIKey and Authenticator are mutually exclusive")
	}
	base := config.BaseURL
	if base == "" {
		base = spec.defaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return shared.Config{}, fmt.Errorf("providers: BaseURL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return shared.Config{}, fmt.Errorf("providers: BaseURL scheme must be http or https")
	}
	if parsed.Scheme == "http" {
		ip := net.ParseIP(parsed.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return shared.Config{}, fmt.Errorf("providers: plaintext BaseURL requires a literal loopback address")
		}
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
		doer = &http.Client{
			Transport:     defaultTransport(),
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	authenticate := shared.AuthenticateFunc(nil)
	var redact []string
	if config.Authenticator != nil {
		authenticate = config.Authenticator.Authenticate
	} else if config.APIKey != "" {
		key := config.APIKey
		redact = []string{key}
		authenticate = func(_ context.Context, request *http.Request) error {
			request.Header.Set(spec.apiKeyHeader, spec.apiKeyPrefix+key)
			return nil
		}
	}
	return shared.Config{
		Provider: spec.provider, BaseURL: parsed,
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
		transport.Proxy = nil
		if transport.ResponseHeaderTimeout == 0 {
			transport.ResponseHeaderTimeout = 30 * time.Second
		}
		if transport.MaxIdleConnsPerHost < 16 {
			transport.MaxIdleConnsPerHost = 16
		}
		return transport
	}
	return &http.Transport{
		Proxy:                 nil,
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
	values := []*int64{&input.MaxRequestBytes, &input.MaxResponseBytes, &input.MaxStreamResponseBytes, &input.MaxErrorResponseBytes, &input.MaxSSEEventBytes, &input.MaxProviderStateBytes, &input.MaxCallIDBytes, &input.MaxToolNameBytes, &input.MaxArgumentsBytes, &input.MaxBatchArgumentBytes}
	defaults := [...]int64{defaultMaxRequestBytes, defaultMaxResponseBytes, defaultMaxStreamResponseBytes, defaultMaxErrorResponseBytes, defaultMaxSSEEventBytes, defaultMaxProviderStateBytes, defaultMaxCallIDBytes, defaultMaxToolNameBytes, defaultMaxArgumentsBytes, defaultMaxBatchArgumentBytes}
	for i, value := range values {
		if *value < 0 {
			return shared.Limits{}, fmt.Errorf("providers: byte limits must not be negative")
		}
		if *value == 0 {
			*value = defaults[i]
		}
		if *value > defaults[i] {
			return shared.Limits{}, fmt.Errorf("providers: byte limit exceeds package ceiling %d", defaults[i])
		}
	}
	if input.MaxToolCalls < 0 || input.MaxToolCalls > defaultMaxToolCalls {
		return shared.Limits{}, fmt.Errorf("providers: MaxToolCalls must be between 1 and %d", defaultMaxToolCalls)
	}
	if input.MaxToolCalls == 0 {
		input.MaxToolCalls = defaultMaxToolCalls
	}
	if input.MaxCallIDBytes < 1 || input.MaxToolNameBytes < 1 || input.MaxArgumentsBytes < 2 {
		return shared.Limits{}, fmt.Errorf("providers: decoded tool limits are too small")
	}
	minimumBatch := int64(input.MaxToolCalls) * 2
	if input.MaxBatchArgumentBytes < minimumBatch {
		return shared.Limits{}, fmt.Errorf("providers: MaxBatchArgumentBytes cannot hold %d minimal tool calls", input.MaxToolCalls)
	}
	return shared.Limits(input), nil
}

func validateHeaders(headers http.Header) error {
	for name, values := range headers {
		if httpx.ReservedHeader(name) {
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

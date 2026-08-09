package providers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers"
)

func TestDriverNormalization(t *testing.T) {
	t.Parallel()
	for _, driver := range []providers.Driver{"", providers.DriverDefault} {
		provider, err := providers.New(providers.Config{
			Protocol:   providers.OpenAIResponses,
			Driver:     driver,
			BaseURL:    "http://127.0.0.1:1",
			HTTPClient: countingDoer{calls: &atomic.Int64{}},
		})
		if err != nil {
			t.Fatalf("Driver %q: New: %v", driver, err)
		}
		if _, err := provider.Model("model-1"); err != nil {
			t.Fatalf("Driver %q: Model: %v", driver, err)
		}
	}
	if _, err := providers.New(providers.Config{Protocol: providers.OpenAIResponses, Driver: "unknown"}); err == nil {
		t.Fatal("unknown driver accepted")
	}
}

func TestOfficialDriverFactory(t *testing.T) {
	t.Parallel()
	protocols := []providers.Protocol{
		providers.OpenAIChatCompletions,
		providers.OpenAIResponses,
		providers.AnthropicMessages,
		providers.GeminiGenerateContent,
	}
	for _, protocol := range protocols {
		protocol := protocol
		t.Run(string(protocol), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			provider, err := providers.New(providers.Config{
				Protocol:   protocol,
				Driver:     providers.DriverOfficialSDK,
				BaseURL:    "http://127.0.0.1:1",
				APIKey:     "test-secret",
				HTTPClient: countingDoer{calls: &calls},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			modelID := "upstream-model"
			if protocol == providers.GeminiGenerateContent {
				modelID = "gemini-2.0-flash"
			}
			model, err := provider.Model(modelID)
			if err != nil || model == nil {
				t.Fatalf("Model = %#v, %v", model, err)
			}
			if calls.Load() != 0 {
				t.Fatalf("construction made %d network calls", calls.Load())
			}
			if _, ok := model.(models.CapabilityReporter); !ok {
				t.Fatal("upstream model does not report adapter capabilities")
			}
		})
	}
}

func TestOfficialDriverRejectsUnknownProtocol(t *testing.T) {
	t.Parallel()
	// Protocol validation happens before driver dispatch for unknown protocols.
	if _, err := providers.New(providers.Config{Protocol: "unknown.protocol", Driver: providers.DriverOfficialSDK}); err == nil {
		t.Fatal("unknown protocol accepted for official driver")
	}
}

func TestPoisonEnvDoesNotAffectOfficialRequests(t *testing.T) {
	// Sequential: mutates process environment.
	t.Setenv("OPENAI_API_KEY", "env-openai-secret")
	t.Setenv("OPENAI_BASE_URL", "https://env.openai.invalid")
	t.Setenv("OPENAI_ORG_ID", "env-openai-org-secret")
	t.Setenv("OPENAI_PROJECT_ID", "env-openai-project-secret")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Ambient-Secret: env-openai-header-secret")
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic-secret")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.anthropic.invalid")
	t.Setenv("GEMINI_API_KEY", "env-gemini-secret")
	t.Setenv("GOOGLE_API_KEY", "env-google-secret")

	protocols := []providers.Protocol{
		providers.OpenAIResponses,
		providers.AnthropicMessages,
		providers.GeminiGenerateContent,
	}
	for _, protocol := range protocols {
		protocol := protocol
		t.Run(string(protocol), func(t *testing.T) {
			var seen atomic.Pointer[http.Request]
			serverDoer := doerFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				seen.Store(cloned)
				return nil, errors.New("stop after capture")
			})
			modelID := "model-1"
			if protocol == providers.GeminiGenerateContent {
				modelID = "gemini-2.0-flash"
			}
			provider, err := providers.New(providers.Config{
				Protocol:   protocol,
				Driver:     providers.DriverOfficialSDK,
				BaseURL:    "http://127.0.0.1:9",
				APIKey:     "config-secret",
				HTTPClient: serverDoer,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			model, err := provider.Model(modelID)
			if err != nil {
				t.Fatalf("Model: %v", err)
			}
			request := models.NewTextRequest("hello")
			request.Generation.MaxOutputTokens = models.Some(32)
			_, _ = model.Complete(context.Background(), request)
			req := seen.Load()
			if req == nil {
				t.Fatal("no request captured")
			}
			if host := req.URL.Host; host != "127.0.0.1:9" {
				t.Fatalf("request host = %q, want config base host", host)
			}
			bodyHas := false
			// Ensure env secrets never appear in headers the server would see.
			for name, values := range req.Header {
				for _, value := range values {
					if containsAny(value, "env-openai-secret", "env-openai-org-secret", "env-openai-project-secret", "env-openai-header-secret", "env-anthropic-secret", "env-gemini-secret", "env-google-secret") {
						t.Errorf("header %s leaked ambient secret: %q", name, value)
					}
					if containsAny(value, "config-secret") {
						bodyHas = true
					}
				}
			}
			if !bodyHas {
				t.Error("config API key was not applied to request headers")
			}
		})
	}
}

func TestOfficialSDKDisablesRetry(t *testing.T) {
	t.Parallel()
	protocols := []providers.Protocol{
		providers.OpenAIResponses,
		providers.AnthropicMessages,
		providers.GeminiGenerateContent,
	}
	for _, protocol := range protocols {
		protocol := protocol
		t.Run(string(protocol), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("Retry-After", "1")
				writer.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(writer, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
			}))
			defer server.Close()
			modelID := "model-1"
			if protocol == providers.GeminiGenerateContent {
				modelID = "gemini-2.0-flash"
			}
			provider, err := providers.New(providers.Config{
				Protocol:   protocol,
				Driver:     providers.DriverOfficialSDK,
				BaseURL:    server.URL,
				APIKey:     "test-secret",
				HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			model, err := provider.Model(modelID)
			if err != nil {
				t.Fatalf("Model: %v", err)
			}
			req := models.NewTextRequest("hello")
			req.Generation.MaxOutputTokens = models.Some(16)
			_, err = model.Complete(context.Background(), req)
			if err == nil {
				t.Fatal("expected rate limit error")
			}
			var modelErr *models.Error
			if !errors.As(err, &modelErr) {
				t.Fatalf("error = %#v", err)
			}
			if modelErr.RetryAfter != time.Second {
				t.Fatalf("RetryAfter = %v, want 1s", modelErr.RetryAfter)
			}
			if calls.Load() != 1 {
				t.Fatalf("HTTP attempts = %d, want 1 (SDK retry must be disabled)", calls.Load())
			}
		})
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		if part != "" && (value == part || len(value) >= len(part) && (value == "Bearer "+part || value == part || containsSubstring(value, part))) {
			return true
		}
	}
	return false
}

func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && (s == sub || len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

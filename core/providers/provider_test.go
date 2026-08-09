package providers_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers"
)

func TestNewAndModelDoNotUseNetwork(t *testing.T) {
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
			provider, err := providers.New(providers.Config{Protocol: protocol, BaseURL: "http://127.0.0.1:1", HTTPClient: countingDoer{calls: &calls}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			upstreamModel, err := provider.Model("upstream-model")
			if err != nil || upstreamModel == nil {
				t.Fatalf("Model = %#v, %v", upstreamModel, err)
			}
			if calls.Load() != 0 {
				t.Fatalf("construction made %d network calls", calls.Load())
			}
			if _, ok := upstreamModel.(models.CapabilityReporter); !ok {
				t.Fatal("upstream model does not report adapter capabilities")
			}
		})
	}
}

func TestConfigAndModelValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config providers.Config
	}{
		{name: "missing protocol", config: providers.Config{}},
		{name: "unknown protocol", config: providers.Config{Protocol: "unknown"}},
		{name: "auth conflict", config: providers.Config{Protocol: providers.OpenAIResponses, APIKey: "key", Authenticator: providers.AuthenticatorFunc(func(_ context.Context, _ *http.Request) error { return nil })}},
		{name: "base query", config: providers.Config{Protocol: providers.OpenAIResponses, BaseURL: "https://example.test?override=1"}},
		{name: "reserved header", config: providers.Config{Protocol: providers.OpenAIResponses, Headers: http.Header{"Authorization": []string{"bad"}}}},
		{name: "negative limit", config: providers.Config{Protocol: providers.OpenAIResponses, Limits: providers.Limits{MaxSSEEventBytes: -1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := providers.New(test.config); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}

	provider, err := providers.New(providers.Config{Protocol: providers.GeminiGenerateContent})
	if err != nil {
		t.Fatal(err)
	}
	for _, modelID := range []string{"", "models/gemini", "..", "gemini?alt=x", `gemini\\evil`, "gemini%2fescape", "gemini:action"} {
		if _, bindErr := provider.Model(modelID); bindErr == nil {
			t.Errorf("Gemini Model(%q) accepted", modelID)
		} else {
			var modelErr *models.Error
			if !errors.As(bindErr, &modelErr) || modelErr.Kind != models.ErrorInvalidRequest {
				t.Errorf("Gemini Model(%q) error = %#v", modelID, bindErr)
			}
		}
	}
	if _, err := provider.Model("gemini-2.0-flash"); err != nil {
		t.Fatalf("valid Gemini ID rejected: %v", err)
	}
}

type countingDoer struct {
	calls *atomic.Int64
}

func (d countingDoer) Do(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("unexpected network call")
}

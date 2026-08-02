package sdkhttp_test

import (
	"net/url"
	"testing"

	"github.com/h2cone/ouro/core/providers/internal/sdkhttp"
)

func TestOpenAIEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://api.openai.com", "https://api.openai.com/v1/"},
		{"https://api.openai.com/", "https://api.openai.com/v1/"},
		{"https://api.openai.com/v1", "https://api.openai.com/v1/"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/"},
		{"https://proxy.example/prefix", "https://proxy.example/prefix/v1/"},
		{"https://proxy.example/prefix/v1", "https://proxy.example/prefix/v1/"},
	}
	for _, tc := range cases {
		parsed, err := url.Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		got := sdkhttp.OpenAIEndpoint(parsed).BaseURL
		if got != tc.want {
			t.Errorf("OpenAIEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnthropicEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://api.anthropic.com", "https://api.anthropic.com/"},
		{"https://api.anthropic.com/v1", "https://api.anthropic.com/"},
		{"https://api.anthropic.com/v1/", "https://api.anthropic.com/"},
		{"https://proxy.example/prefix", "https://proxy.example/prefix/"},
		{"https://proxy.example/prefix/v1", "https://proxy.example/prefix/"},
	}
	for _, tc := range cases {
		parsed, err := url.Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		got := sdkhttp.AnthropicEndpoint(parsed).BaseURL
		if got != tc.want {
			t.Errorf("AnthropicEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGeminiEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://generativelanguage.googleapis.com", "https://generativelanguage.googleapis.com/"},
		{"https://generativelanguage.googleapis.com/v1beta", "https://generativelanguage.googleapis.com/"},
		{"https://proxy.example/prefix/v1beta", "https://proxy.example/prefix/"},
		{"https://proxy.example/prefix/v1", "https://proxy.example/prefix/"},
	}
	for _, tc := range cases {
		parsed, err := url.Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		ep := sdkhttp.GeminiEndpoint(parsed)
		if ep.BaseURL != tc.want || ep.APIVersion != "v1beta" {
			t.Errorf("GeminiEndpoint(%q) = %#v, want base %q v1beta", tc.in, ep, tc.want)
		}
	}
}

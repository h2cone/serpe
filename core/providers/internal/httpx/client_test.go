package httpx

import "testing"

func TestJoinEndpointPathAvoidsDuplicateVersionPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		base     string
		endpoint string
		want     string
	}{
		{name: "origin", endpoint: "/v1/responses", want: "/v1/responses"},
		{name: "version root", base: "/v1", endpoint: "/v1/responses", want: "/v1/responses"},
		{name: "proxy prefix", base: "/proxy/openai/v1", endpoint: "/v1/responses", want: "/proxy/openai/v1/responses"},
		{name: "full endpoint", base: "/v1/responses", endpoint: "/v1/responses", want: "/v1/responses"},
		{name: "custom prefix", base: "/custom", endpoint: "/v1/responses", want: "/custom/v1/responses"},
		{name: "segment boundary", base: "/api-v1", endpoint: "/v1/responses", want: "/api-v1/v1/responses"},
		{name: "gemini version", base: "/gateway/v1beta", endpoint: "/v1beta/models/gemini:generateContent", want: "/gateway/v1beta/models/gemini:generateContent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := joinEndpointPath(test.base, test.endpoint); got != test.want {
				t.Fatalf("joinEndpointPath(%q, %q) = %q, want %q", test.base, test.endpoint, got, test.want)
			}
		})
	}
}

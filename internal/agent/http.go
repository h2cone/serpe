package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultBaseURL = "https://api.openai.com/v1"

// HTTPTransport sends Responses API requests over HTTP.
type HTTPTransport struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewHTTPTransport creates an HTTP transport. Empty baseURL and client values
// use the OpenAI default endpoint and http.DefaultClient.
func NewHTTPTransport(baseURL, apiKey string, client *http.Client) *HTTPTransport {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if client == nil {
		client = http.DefaultClient
	}

	return &HTTPTransport{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  client,
	}
}

// CreateResponse posts a JSON Responses API request and returns the decoded JSON body.
func (t *HTTPTransport) CreateResponse(ctx context.Context, request map[string]any) (map[string]any, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+t.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("responses API returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

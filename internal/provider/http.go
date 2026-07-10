// Package provider implements the canonical HTTP client. An HTTPProvider wraps
// a codec.Protocol and speaks only canonical types to the agent loop while
// making HTTP calls upstream.
package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
	"github.com/tw8ap/ouro/internal/codec"
)

// Provider is the only interface the agent loop depends on. Its inputs and
// outputs are entirely canonical; it is protocol-agnostic.
type Provider interface {
	Complete(ctx context.Context, req *canon.Request) (*canon.Response, error)
	Stream(ctx context.Context, req *canon.Request) (<-chan canon.Event, error)
}

// HTTPProvider makes HTTP round-trips to an upstream LLM using one Protocol.
type HTTPProvider struct {
	proto   codec.Protocol
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewHTTPProvider constructs an HTTPProvider. Empty baseURL falls back to the
// protocol's DefaultBaseURL; empty client uses http.DefaultClient.
func NewHTTPProvider(proto codec.Protocol, baseURL, apiKey string, client *http.Client) *HTTPProvider {
	if baseURL == "" {
		baseURL = proto.DefaultBaseURL
	}
	if baseURL == "" {
		panic("provider baseURL is required for protocol without DefaultBaseURL")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPProvider{
		proto:   proto,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  client,
	}
}

// Complete performs a non-streaming round-trip and returns the canonical Response.
func (p *HTTPProvider) Complete(ctx context.Context, req *canon.Request) (*canon.Response, error) {
	encReq := *req
	encReq.Stream = false
	body, err := p.proto.Codec.EncodeRequest(&encReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := p.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, data, err := p.do(httpReq)
	if err != nil {
		return nil, err
	}

	out, err := p.proto.Codec.DecodeResponse(data)
	if err != nil {
		return nil, fmt.Errorf("%s decode: %w", p.proto.Name, err)
	}
	out.Provider = p.proto.Name
	_ = resp
	return out, nil
}

// newRequest builds the POST request with the protocol's auth headers.
func (p *HTTPProvider) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+p.proto.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range p.proto.Auth(p.apiKey) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// do sends the request, reads the full body, and normalizes non-2xx into an error.
func (p *HTTPProvider) do(httpReq *http.Request) (*http.Response, []byte, error) {
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil, fmt.Errorf("%s %s: %s", p.proto.Name, resp.Status, strings.TrimSpace(string(data)))
	}
	return resp, data, nil
}

package sdkhttp

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/transport/httpx"
)

type callKey struct{}

type callMetadata struct {
	caller      context.Context
	capture     *Capture
	operation   string
	requestBody []byte
	replaceBody bool
}

func callFrom(ctx context.Context) *callMetadata {
	if ctx == nil {
		return nil
	}
	metadata, _ := ctx.Value(callKey{}).(*callMetadata)
	return metadata
}

// ValidateCall rejects nil values before an official SDK can panic on them.
func ValidateCall(ctx context.Context, request *models.Request, provider, operation string) error {
	if ctx == nil {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: operation, Message: "context is nil"}
	}
	if request == nil {
		return &models.Error{Kind: models.ErrorInvalidRequest, Provider: provider, Operation: operation, Message: "request is nil"}
	}
	return nil
}

// PrepareCall attaches all per-call bridge metadata and places the caller
// marker last so an SDK-added timeout can be removed without losing metadata.
// A nil requestBody leaves the SDK-encoded body unchanged.
func PrepareCall(ctx context.Context, operation string, requestBody []byte) (context.Context, *Capture) {
	ctx, metadata := prepare(ctx, operation, requestBody)
	return context.WithValue(ctx, callKey{}, metadata), metadata.capture
}

// PrepareStreamCall is PrepareCall with an internal cancellation boundary.
// The cancellation context is created before the caller marker so Bridge keeps
// stream cancellation while removing only a vendor SDK's wrapper.
func PrepareStreamCall(ctx context.Context, operation string, requestBody []byte) (context.Context, *Capture, context.CancelFunc) {
	ctx, metadata := prepare(ctx, operation, requestBody)
	ctx, cancel := context.WithCancel(ctx)
	metadata.caller = ctx
	return context.WithValue(ctx, callKey{}, metadata), metadata.capture, cancel
}

func prepare(ctx context.Context, operation string, requestBody []byte) (context.Context, *callMetadata) {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, &callMetadata{
		caller: ctx, capture: &Capture{}, operation: operation,
		requestBody: append([]byte(nil), requestBody...), replaceBody: requestBody != nil,
	}
}

// Capture holds per-call response headers (and optional unary body) so concurrent
// Complete/Stream calls on the same model never share a mutable slot.
type Capture struct {
	Header http.Header
	// Body is the successful non-stream response body when the bridge tees it.
	// Stream responses leave Body nil.
	Body []byte

	streamMu   sync.Mutex
	streamBody io.ReadCloser
}

func (c *Capture) setStreamBody(body io.ReadCloser) {
	if c == nil {
		return
	}
	c.streamMu.Lock()
	c.streamBody = body
	c.streamMu.Unlock()
}

// StreamBody returns the captured successful streaming response body.
func (c *Capture) StreamBody() io.ReadCloser {
	if c == nil {
		return nil
	}
	c.streamMu.Lock()
	body := c.streamBody
	c.streamMu.Unlock()
	return body
}

// RequestID extracts a public provider request identifier from captured headers.
func (c *Capture) RequestID() string {
	if c == nil {
		return ""
	}
	return httpx.RequestID(c.Header)
}

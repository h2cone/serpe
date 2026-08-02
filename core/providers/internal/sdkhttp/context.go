package sdkhttp

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/h2cone/ouro/core/providers/internal/httpx"
)

type callerContextKey struct{}

// withCallerContext stores the caller's context so Transport can restore it and
// neutralize vendor SDK request timeouts that would otherwise terminate earlier
// than the caller's deadline.
func withCallerContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callerContextKey{}, ctx)
}

func callerContext(ctx context.Context) (context.Context, bool) {
	if ctx == nil {
		return nil, false
	}
	parent, ok := ctx.Value(callerContextKey{}).(context.Context)
	return parent, ok
}

type captureKey struct{}

type requestBodyKey struct{}

// withRequestBody preserves the canonical protocol payload so Bridge can
// replace a vendor SDK's re-encoded JSON body at the final HTTP boundary.
func withRequestBody(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestBodyKey{}, append([]byte(nil), body...))
}

func requestBodyFrom(ctx context.Context) ([]byte, bool) {
	if ctx == nil {
		return nil, false
	}
	body, ok := ctx.Value(requestBodyKey{}).([]byte)
	return body, ok
}

// PrepareCall attaches all per-call bridge metadata and places the caller
// marker last so an SDK-added timeout can be removed without losing metadata.
// A nil requestBody leaves the SDK-encoded body unchanged.
func PrepareCall(ctx context.Context, operation string, requestBody []byte) (context.Context, *Capture) {
	ctx, capture := withCallMetadata(ctx, operation, requestBody)
	return withCallerContext(ctx), capture
}

// PrepareStreamCall is PrepareCall with an internal cancellation boundary.
// The cancellation context is created before the caller marker so Bridge keeps
// stream cancellation while removing only a vendor SDK's wrapper.
func PrepareStreamCall(ctx context.Context, operation string, requestBody []byte) (context.Context, *Capture, context.CancelFunc) {
	ctx, capture := withCallMetadata(ctx, operation, requestBody)
	ctx, cancel := context.WithCancel(ctx)
	return withCallerContext(ctx), capture, cancel
}

func withCallMetadata(ctx context.Context, operation string, requestBody []byte) (context.Context, *Capture) {
	capture := &Capture{}
	ctx = withCapture(ctx, capture)
	ctx = withOperation(ctx, operation)
	if requestBody != nil {
		ctx = withRequestBody(ctx, requestBody)
	}
	return ctx, capture
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

func withCapture(ctx context.Context, capture *Capture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, captureKey{}, capture)
}

func captureFrom(ctx context.Context) *Capture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(captureKey{}).(*Capture)
	return capture
}

// RequestID extracts a public provider request identifier from captured headers.
func (c *Capture) RequestID() string {
	if c == nil {
		return ""
	}
	return httpx.RequestID(c.Header)
}

package sdkhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers/internal/shared"
	"github.com/h2cone/ouro/core/providers/internal/transport/httpx"
)

func TestBridgeReplacesSDKBodyAfterRestoringCallerContext(t *testing.T) {
	t.Parallel()
	canonical := []byte(`{"contents":[{"parts":[{"text":"hello"}]}],"large":9007199254740993}`)
	ctx, capture := PrepareCall(context.Background(), "generate", canonical)
	vendorCtx, cancelVendor := context.WithCancel(ctx)
	cancelVendor()

	request, err := http.NewRequestWithContext(vendorCtx, http.MethodPost, "https://example.test/generate", strings.NewReader(`{"sdk":"body"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")

	bridge := NewBridge(BridgeConfig{
		Provider: "test",
		Limits: shared.Limits{
			MaxResponseBytes:      1024,
			MaxErrorResponseBytes: 1024,
			MaxSSEEventBytes:      1024,
		},
		Authenticate: func(ctx context.Context, request *http.Request) error {
			if err := ctx.Err(); err != nil {
				t.Fatalf("Authenticate received vendor-canceled context: %v", err)
			}
			request.Header.Set("Authorization", "Bearer configured")
			return nil
		},
		Doer: doerFunc(func(request *http.Request) (*http.Response, error) {
			if err := request.Context().Err(); err != nil {
				t.Fatalf("Doer received vendor-canceled context: %v", err)
			}
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Fatalf("ReadAll request: %v", readErr)
			}
			if string(body) != string(canonical) {
				t.Fatalf("request body = %s, want canonical %s", body, canonical)
			}
			copyBody, getErr := request.GetBody()
			if getErr != nil {
				t.Fatalf("GetBody: %v", getErr)
			}
			defer copyBody.Close()
			copyData, readErr := io.ReadAll(copyBody)
			if readErr != nil || string(copyData) != string(canonical) {
				t.Fatalf("GetBody data = %s, %v", copyData, readErr)
			}
			if request.ContentLength != int64(len(canonical)) {
				t.Fatalf("ContentLength = %d, want %d", request.ContentLength, len(canonical))
			}
			if request.Header.Get("Content-Encoding") != "" {
				t.Fatalf("stale Content-Encoding = %q", request.Header.Get("Content-Encoding"))
			}
			if request.Header.Get("Authorization") != "Bearer configured" {
				t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-ID": {"request-1"}},
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Request:    request,
			}, nil
		}),
	})
	response, err := bridge.Do(request)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("ReadAll response: %v", err)
	}
	if capture.RequestID() != "request-1" || string(capture.Body) != `{}` {
		t.Fatalf("capture = request ID %q, body %q", capture.RequestID(), capture.Body)
	}
}

func TestPrepareStreamCallKeepsInternalCancellation(t *testing.T) {
	t.Parallel()
	ctx, capture, cancel := PrepareStreamCall(context.Background(), "stream", nil)
	metadata := callFrom(ctx)
	if capture == nil || metadata == nil || metadata.capture != capture {
		t.Fatal("stream capture was not attached")
	}
	cancel()
	if !errors.Is(metadata.caller.Err(), context.Canceled) {
		t.Fatalf("caller marker error = %v, want context canceled", metadata.caller.Err())
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestLimitedBodyAllowsExactLimit(t *testing.T) {
	t.Parallel()
	body := httpx.LimitReadCloser(io.NopCloser(strings.NewReader("1234")), 4, &models.Error{
		Kind: models.ErrorProtocol, Code: "response_too_large",
	})
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "1234" {
		t.Fatalf("body = %q", data)
	}
}

func TestLimitedBodyAllowsTemporaryEmptyReadAtExactLimit(t *testing.T) {
	t.Parallel()
	body := httpx.LimitReadCloser(&emptyBeforeEOFReadCloser{data: []byte("1234")}, 4, errors.New("overflow"))
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "1234" {
		t.Fatalf("body = %q", data)
	}
}

func TestLimitedBodyReturnsNormalizedOverflow(t *testing.T) {
	t.Parallel()
	want := &models.Error{Kind: models.ErrorProtocol, Code: "response_too_large"}
	body := httpx.LimitReadCloser(io.NopCloser(strings.NewReader("12345")), 4, want)
	_, err := io.ReadAll(body)
	var modelErr *models.Error
	if !errors.As(err, &modelErr) || modelErr != want {
		t.Fatalf("error = %#v, want normalized overflow", err)
	}
	if modelErr.Retryable {
		t.Fatal("response-size overflow must not be retryable")
	}
}

type emptyBeforeEOFReadCloser struct {
	data  []byte
	empty bool
}

func (r *emptyBeforeEOFReadCloser) Read(buffer []byte) (int, error) {
	if len(r.data) > 0 {
		count := copy(buffer, r.data)
		r.data = r.data[count:]
		return count, nil
	}
	if !r.empty {
		r.empty = true
		return 0, nil
	}
	return 0, io.EOF
}

func (*emptyBeforeEOFReadCloser) Close() error { return nil }

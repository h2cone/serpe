package httpx

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestDecodeErrorOpenAIEnvelope(t *testing.T) {
	t.Parallel()
	body := `{"error":{"message":"Incorrect API key","type":"invalid_request_error","code":"invalid_api_key"}}`
	err := DecodeError(jsonResponse(403, body), "openai", "stream", 4096, nil)
	me := mustModelError(t, err)
	if me.Kind != models.ErrorPermission {
		t.Fatalf("kind = %q, want permission", me.Kind)
	}
	if me.Message != "Incorrect API key" {
		t.Fatalf("message = %q", me.Message)
	}
	if me.Code != "invalid_api_key" {
		t.Fatalf("code = %q", me.Code)
	}
}

func TestDecodeErrorXAIStringError(t *testing.T) {
	t.Parallel()
	// xAI returns a string "error" and top-level "code" rather than OpenAI's nested object.
	body := `{"code":"permission-denied","error":"Your newly created team doesn't have any credits or licenses yet."}`
	err := DecodeError(jsonResponse(403, body), "openai", "stream", 4096, nil)
	me := mustModelError(t, err)
	if me.Kind != models.ErrorPermission {
		t.Fatalf("kind = %q, want permission", me.Kind)
	}
	if me.Code != "permission-denied" {
		t.Fatalf("code = %q", me.Code)
	}
	if !strings.Contains(me.Message, "credits or licenses") {
		t.Fatalf("message = %q, want credits text", me.Message)
	}
}

func TestDecodeErrorPlainTextSnippet(t *testing.T) {
	t.Parallel()
	err := DecodeError(jsonResponse(502, "Bad Gateway from edge"), "openai", "stream", 4096, nil)
	me := mustModelError(t, err)
	if me.Message != "Bad Gateway from edge" {
		t.Fatalf("message = %q", me.Message)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func mustModelError(t *testing.T, err error) *models.Error {
	t.Helper()
	me, ok := err.(*models.Error)
	if !ok || me == nil {
		t.Fatalf("error type = %T (%v), want *models.Error", err, err)
	}
	return me
}

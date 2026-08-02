package httpx

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func FuzzDecodeError(f *testing.F) {
	f.Add([]byte(`{"error":{"message":"limited","code":"rate"}}`), 429)
	f.Add([]byte("not json"), 500)
	f.Fuzz(func(t *testing.T, body []byte, status int) {
		if status < 100 || status > 999 {
			status = 500
		}
		response := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
		err := DecodeError(response, "fuzz", "generate", 4096, []string{"secret"})
		if err == nil {
			t.Fatal("DecodeError returned nil")
		}
		_ = err.Error()
	})
}

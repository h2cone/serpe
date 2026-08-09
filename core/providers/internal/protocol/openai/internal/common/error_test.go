package common

import (
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestNormalizeError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code      string
		typeName  string
		wantKind  models.ErrorKind
		retryable bool
	}{
		{code: "rate_limit_exceeded", wantKind: models.ErrorRateLimited, retryable: true},
		{typeName: "authentication_error", wantKind: models.ErrorAuthentication},
		{code: "invalid_request_error", wantKind: models.ErrorInvalidRequest},
		{code: "server_error", wantKind: models.ErrorUnavailable, retryable: true},
	}
	for _, test := range tests {
		err := NormalizeError(test.code, test.typeName, "message", "stream_next", "request-id")
		if err.Kind != test.wantKind || err.Retryable != test.retryable || err.RequestID != "request-id" || err.Code == "" {
			t.Errorf("NormalizeError(%q, %q) = %#v", test.code, test.typeName, err)
		}
	}
}

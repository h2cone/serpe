package providers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newLoopbackServer is httptest.NewTestServer plus a loopback listener so
// provider HTTP clients (which do not use Server.Client) can still connect.
func newLoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTestServer(t, handler)
	server.Start()
	return server
}

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUnsupportedDeadlinesFailBeforeMutation(t *testing.T) {
	srv, manager := newTestServer(t, nil, nil, nil)
	rr := httptest.NewRecorder() // deliberately lacks ResponseController deadlines
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError || !bytes.Contains(rr.Body.Bytes(), []byte("deadline_unsupported")) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	sessions, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("unsupported deadline mutated the store: %d sessions", len(sessions))
	}
}

type readDeadlineOnlyRecorder struct{ *httptest.ResponseRecorder }

func (*readDeadlineOnlyRecorder) SetReadDeadline(time.Time) error { return nil }

func TestUnsupportedWriteDeadlineFailsBeforeHeaders(t *testing.T) {
	srv, _ := newTestServer(t, nil, nil, nil)
	rr := &readDeadlineOnlyRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError || !bytes.Contains(rr.Body.Bytes(), []byte("deadline_unsupported")) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t, nil, nil, nil)
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body["ok"] {
		t.Fatalf("body=%v", body)
	}
}

func TestSessionsCRUD(t *testing.T) {
	ids := []string{"a", "b"}
	idx := 0
	srv, _ := newTestServer(t, nil, nil, func() string {
		id := ids[idx]
		idx++
		return id
	})
	h := srv.Handler()

	// Create
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"title":"Hello"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["id"] != "a" || created["title"] != "Hello" {
		t.Fatalf("created=%v", created)
	}

	// List
	rr = newRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d", rr.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["id"] != "a" {
		t.Fatalf("list=%v", list)
	}

	// Get
	rr = newRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/a", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Patch title
	rr = newRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/sessions/a", bytes.NewBufferString(`{"title":"Renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rr.Code, rr.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &patched)
	if patched["title"] != "Renamed" {
		t.Fatalf("patched=%v", patched)
	}

	// Fork
	rr = newRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/a/fork", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("fork status=%d body=%s", rr.Code, rr.Body.String())
	}
	var forked map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &forked)
	if forked["id"] != "b" || forked["parent_id"] != "a" {
		t.Fatalf("forked=%v", forked)
	}

	// Delete
	rr = newRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions/a", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", rr.Code)
	}
	rr = newRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/a", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get after delete status=%d", rr.Code)
	}
}

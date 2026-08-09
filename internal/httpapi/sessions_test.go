package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t, nil, nil, nil)
	rr := httptest.NewRecorder()
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
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(`{"title":"Hello"}`))
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
	rr = httptest.NewRecorder()
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
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/a", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Patch title
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/sessions/a", bytes.NewBufferString(`{"title":"Renamed"}`))
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
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/a/fork", bytes.NewBufferString(`{}`))
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
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions/a", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", rr.Code)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/a", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get after delete status=%d", rr.Code)
	}
}

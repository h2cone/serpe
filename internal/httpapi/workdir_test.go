package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/httpapi"
	"github.com/h2cone/serpe/internal/workdir"
	"github.com/h2cone/serpe/runtime/loops"
	"github.com/h2cone/serpe/runtime/sessions"
)

func TestPickWorkingDirSuccess(t *testing.T) {
	dir := t.TempDir()
	srv := newPickerServer(t, func(context.Context, string) (string, error) {
		return dir, nil
	})
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["cwd"] != filepath.Clean(dir) {
		t.Fatalf("body=%v", body)
	}
}

func TestPickWorkingDirCanceled(t *testing.T) {
	srv := newPickerServer(t, func(context.Context, string) (string, error) {
		return "", workdir.ErrCanceled
	})
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPickWorkingDirUnavailableByDefault(t *testing.T) {
	srv, _ := newTestServer(t, nil, nil, nil)
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPickWorkingDirBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := newPickerServer(t, func(context.Context, string) (string, error) {
		close(started)
		<-release
		return t.TempDir(), nil
	})
	done := make(chan int, 1)
	go func() {
		rr := newRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rr, req)
		done <- rr.Code
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("picker did not start")
	}
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("busy status=%d body=%s", rr.Code, rr.Body.String())
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("first pick status=%d", code)
	}
}

func TestPickWorkingDirRejectsUnknownField(t *testing.T) {
	srv := newPickerServer(t, func(context.Context, string) (string, error) {
		t.Fatal("picker should not run")
		return "", nil
	})
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{"path":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPickWorkingDirPassesStartHint(t *testing.T) {
	dir := t.TempDir()
	var gotStart string
	var mu sync.Mutex
	srv := newPickerServer(t, func(_ context.Context, start string) (string, error) {
		mu.Lock()
		gotStart = start
		mu.Unlock()
		return dir, nil
	})
	payload, err := json.Marshal(map[string]string{"start": dir})
	if err != nil {
		t.Fatal(err)
	}
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if gotStart != filepath.Clean(dir) {
		t.Fatalf("start=%q want %q", gotStart, filepath.Clean(dir))
	}
}

func TestPickWorkingDirRejectsQuery(t *testing.T) {
	srv := newPickerServer(t, func(context.Context, string) (string, error) {
		t.Fatal("picker should not run")
		return "", nil
	})
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir?x=1", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPickWorkingDirInvalidSelection(t *testing.T) {
	srv := newPickerServer(t, func(context.Context, string) (string, error) {
		return "", errors.New("not a directory")
	})
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workdir", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func newPickerServer(t *testing.T, pick func(context.Context, string) (string, error)) *httpapi.Server {
	t.Helper()
	runner, err := loops.New(loops.Config{
		Model: &scriptedModel{responses: []*models.Response{textResponse("hello")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := sessions.NewManager(sessions.NewMemoryStore(), sessions.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	srv, err := httpapi.New(httpapi.Config{
		Runner:         runner,
		Manager:        mgr,
		CWD:            t.TempDir(),
		PickWorkingDir: pick,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

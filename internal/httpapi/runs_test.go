package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/runtime/sessions"
)

func TestRunSSESuccess(t *testing.T) {
	model := &scriptedModel{responses: []*models.Response{textResponse("hi there")}}
	srv, mgr := newTestServer(t, model, nil, func() string { return "s1" })

	created, err := mgr.Create(context.Background(), sessions.New("s1", t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Messages) != 0 {
		t.Fatalf("pre-run messages=%d", len(created.Messages))
	}

	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		bytes.NewBufferString(`{"session_id":"s1","prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type=%q", ct)
	}

	frames := parseSSEFrames(t, rr.Body.String())
	kinds := frameKinds(frames)
	if !containsSeq(kinds, "run_start", "model_start", "part_start", "delta", "run_end", "done") {
		t.Fatalf("frame kinds=%v", kinds)
	}
	if countKind(kinds, "run_end") != 1 {
		t.Fatalf("run_end count=%d in %v", countKind(kinds, "run_end"), kinds)
	}
	if countKind(kinds, "done") != 1 {
		t.Fatalf("done count=%d in %v", countKind(kinds, "done"), kinds)
	}
	if countKind(kinds, "error") != 0 {
		t.Fatalf("unexpected error frames: %v", frames)
	}

	done := lastOfKind(frames, "done")
	if done["session_id"] != "s1" {
		t.Fatalf("done=%v", done)
	}
	if int(done["message_count"].(float64)) != 2 {
		t.Fatalf("message_count=%v", done["message_count"])
	}

	got, err := mgr.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("transcript len=%d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != models.RoleUser {
		t.Fatalf("msg0 role=%s", got.Messages[0].Role)
	}
	if got.Messages[1].Role != models.RoleAssistant {
		t.Fatalf("msg1 role=%s", got.Messages[1].Role)
	}
}

func TestRunSSEFailureNoPersist(t *testing.T) {
	model := &scriptedModel{
		responses: []*models.Response{nil},
		errs:      []error{&models.Error{Kind: models.ErrorProtocol, Operation: "stream", Message: "boom"}},
	}
	srv, mgr := newTestServer(t, model, nil, func() string { return "s1" })
	if _, err := mgr.Create(context.Background(), sessions.New("s1", t.TempDir())); err != nil {
		t.Fatal(err)
	}

	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		bytes.NewBufferString(`{"session_id":"s1","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)

	got, err := mgr.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("persisted on failure: %d messages", len(got.Messages))
	}
	// Pre-stream validation errors return JSON, not SSE; either way no messages.
}

func TestRunMissingSession(t *testing.T) {
	store := &loadCountingStore{Store: sessions.NewMemoryStore()}
	srv, _ := newTestServerWithStore(t, nil, nil, nil, store)
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		bytes.NewBufferString(`{"session_id":"nope","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q, want JSON before SSE is opened", ct)
	}
	if loads := store.loads.Load(); loads != 1 {
		t.Fatalf("missing run loaded session %d times, want 1", loads)
	}
}

func TestRunLoadsSessionOnceBeforeOpeningModelStream(t *testing.T) {
	store := &loadCountingStore{Store: sessions.NewMemoryStore()}
	model := &observingModel{
		Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}},
		beforeStream: func() {
			if loads := store.loads.Load(); loads != 1 {
				t.Errorf("turn start loaded session %d times, want 1", loads)
			}
		},
	}
	srv, mgr := newTestServerWithStore(t, model, nil, nil, store)
	if _, err := mgr.Create(context.Background(), sessions.New("s1", t.TempDir())); err != nil {
		t.Fatal(err)
	}
	store.loads.Store(0)

	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		bytes.NewBufferString(`{"session_id":"s1","prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

type loadCountingStore struct {
	sessions.Store
	loads atomic.Int64
}

func (s *loadCountingStore) Load(ctx context.Context, id string) ([]byte, error) {
	s.loads.Add(1)
	return s.Store.Load(ctx, id)
}

type observingModel struct {
	models.Model
	beforeStream func()
}

func (m *observingModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	m.beforeStream()
	return m.Model.Stream(ctx, req)
}

func TestRunWithTool(t *testing.T) {
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(models.ToolCall{ID: "call_1", Name: "now", Arguments: []byte(`{}`)}),
		textResponse("It is noon."),
	}}
	srv, mgr := newTestServer(t, model, mustExec(t, nowTool{}), func() string { return "s1" })
	if _, err := mgr.Create(context.Background(), sessions.New("s1", t.TempDir())); err != nil {
		t.Fatal(err)
	}

	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		bytes.NewBufferString(`{"session_id":"s1","prompt":"what time?"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	frames := parseSSEFrames(t, rr.Body.String())
	kinds := frameKinds(frames)
	if !containsSeq(kinds, "tool_start", "tool_end", "run_end", "done") {
		t.Fatalf("frame kinds=%v", kinds)
	}
	start := firstOfKind(frames, "tool_start")
	end := firstOfKind(frames, "tool_end")
	if start == nil || end == nil {
		t.Fatalf("missing tool frames: %v", frames)
	}
	if int(start["idx"].(float64)) != 0 || int(end["idx"].(float64)) != 0 {
		t.Fatalf("tool idx start=%v end=%v", start["idx"], end["idx"])
	}
	// Current serial contract: start is immediately followed by that call's end.
	startAt := indexOfKind(kinds, "tool_start")
	if startAt < 0 || startAt+1 >= len(kinds) || kinds[startAt+1] != "tool_end" {
		t.Fatalf("expected adjacent tool_start/tool_end, kinds=%v", kinds)
	}
	if countKind(kinds, "run_end") != 1 || countKind(kinds, "done") != 1 {
		t.Fatalf("kinds=%v", kinds)
	}

	got, err := mgr.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	// user + assistant(tool_call) + user(tool_result) + assistant(text) = 4
	if len(got.Messages) != 4 {
		t.Fatalf("transcript len=%d, want 4", len(got.Messages))
	}
}

func parseSSEFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	sc := bufio.NewScanner(strings.NewReader(body))
	var data strings.Builder
	flush := func() {
		if data.Len() == 0 {
			return
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data.String()), &m); err != nil {
			t.Fatalf("bad frame %q: %v", data.String(), err)
		}
		frames = append(frames, m)
		data.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return frames
}

func frameKinds(frames []map[string]any) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		if t, ok := f["t"].(string); ok {
			out = append(out, t)
		}
	}
	return out
}

func countKind(kinds []string, k string) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}

func containsSeq(kinds []string, want ...string) bool {
	i := 0
	for _, k := range kinds {
		if i < len(want) && k == want[i] {
			i++
		}
	}
	return i == len(want)
}

func lastOfKind(frames []map[string]any, kind string) map[string]any {
	var last map[string]any
	for _, f := range frames {
		if f["t"] == kind {
			last = f
		}
	}
	return last
}

func firstOfKind(frames []map[string]any, kind string) map[string]any {
	for _, f := range frames {
		if f["t"] == kind {
			return f
		}
	}
	return nil
}

func indexOfKind(kinds []string, kind string) int {
	for i, k := range kinds {
		if k == kind {
			return i
		}
	}
	return -1
}

func TestRunWithTwoToolsAssertsIndex(t *testing.T) {
	model := &scriptedModel{responses: []*models.Response{
		toolCallResponse(
			models.ToolCall{ID: "c0", Name: "now", Arguments: []byte(`{}`)},
			models.ToolCall{ID: "c1", Name: "now", Arguments: []byte(`{}`)}),
		textResponse("ok"),
	}}
	srv, mgr := newTestServer(t, model, mustExec(t, nowTool{}), func() string { return "s1" })
	if _, err := mgr.Create(context.Background(), sessions.New("s1", t.TempDir())); err != nil {
		t.Fatal(err)
	}
	rr := newRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/runs",
		bytes.NewBufferString(`{"session_id":"s1","prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	frames := parseSSEFrames(t, rr.Body.String())
	kinds := frameKinds(frames)
	var starts, ends []int
	for _, f := range frames {
		kind, _ := f["t"].(string)
		if kind != "tool_start" && kind != "tool_end" {
			continue
		}
		if kind == "tool_start" {
			starts = append(starts, int(f["idx"].(float64)))
		} else {
			ends = append(ends, int(f["idx"].(float64)))
		}
	}
	if !slices.Equal(starts, []int{0, 1}) {
		t.Fatalf("tool starts=%v kinds=%v", starts, kinds)
	}
	slices.Sort(ends)
	if !slices.Equal(ends, []int{0, 1}) {
		t.Fatalf("tool ends=%v", ends)
	}
}

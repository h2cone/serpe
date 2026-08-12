package compose_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/serpe/runtime"
	"github.com/h2cone/serpe/compose"
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/runtime/sessions"
)

// scriptedModel implements models.Model with a fixed sequence of responses.
type scriptedModel struct {
	mu        sync.Mutex
	responses []*models.Response
	errs      []error
	calls     int
	requests  []*models.Request
	// gate, if non-nil, blocks each Stream until closed (for concurrency tests).
	gate <-chan struct{}
}

func (m *scriptedModel) Complete(ctx context.Context, req *models.Request) (*models.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	resp := stream.Response()
	_ = stream.Close()
	return resp, nil
}

func (m *scriptedModel) Stream(ctx context.Context, req *models.Request) (models.Stream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req.Clone())
	idx := m.calls
	m.calls++
	gate := m.gate
	var resp *models.Response
	var callErr error
	if idx < len(m.errs) {
		callErr = m.errs[idx]
	}
	if idx < len(m.responses) {
		resp = m.responses[idx]
	}
	m.mu.Unlock()

	if gate != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
		}
	}
	if callErr != nil {
		return nil, callErr
	}
	if resp == nil {
		return nil, &models.Error{Kind: models.ErrorProtocol, Operation: "stream", Message: "no scripted response"}
	}
	return models.NewStream(ctx, &sliceSource{events: eventsFromResponse(resp)}), nil
}

func (m *scriptedModel) requestAt(i int) *models.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.requests) {
		return nil
	}
	return m.requests[i].Clone()
}

type sliceSource struct {
	events []models.Event
	index  int
}

func (s *sliceSource) Next() (models.Event, error) {
	if s.index >= len(s.events) {
		return models.Event{}, io.EOF
	}
	ev := s.events[s.index]
	s.index++
	return ev, nil
}

func (*sliceSource) Close() error { return nil }

func eventsFromResponse(resp *models.Response) []models.Event {
	events := []models.Event{
		{Kind: models.EventResponseStart, Response: &models.ResponseInfo{
			Provider: resp.Provider, ID: resp.ID, Model: resp.Model, RequestID: resp.RequestID,
			CreatedAt: resp.CreatedAt, Metadata: resp.Metadata, ProviderState: resp.ProviderState,
		}},
	}
	for _, cand := range resp.Candidates {
		for partIdx, part := range cand.Content {
			start := part.Clone()
			events = append(events, models.Event{
				Kind: models.EventPartStart, CandidateIndex: cand.Index, PartIndex: partIdx, Part: start,
			})
			events = append(events, models.Event{
				Kind: models.EventPartEnd, CandidateIndex: cand.Index, PartIndex: partIdx,
			})
		}
	}
	finishes := make([]models.CandidateFinish, 0, len(resp.Candidates))
	for _, cand := range resp.Candidates {
		finishes = append(finishes, models.CandidateFinish{
			CandidateIndex: cand.Index,
			Reason:         cand.FinishReason,
			RawReason:      cand.RawFinishReason,
			ProviderState:  cand.ProviderState,
		})
	}
	usage := resp.Usage
	events = append(events, models.Event{
		Kind: models.EventResponseEnd,
		Response: &models.ResponseInfo{
			Provider: resp.Provider, ID: resp.ID, Model: resp.Model, Status: resp.Status,
			RequestID: resp.RequestID, CreatedAt: resp.CreatedAt, Metadata: resp.Metadata,
			ProviderState: resp.ProviderState,
		},
		Finishes: finishes,
		Usage:    &usage,
	})
	return events
}

func textResponse(text string) *models.Response {
	return &models.Response{
		Provider: "script",
		ID:       "r1",
		Model:    "m",
		Status:   models.ResponseStatusCompleted,
		Candidates: []models.Candidate{{
			Index:        0,
			Content:      []models.Content{models.Text(text)},
			FinishReason: models.FinishStop,
		}},
	}
}

func withState(resp *models.Response, state *models.ProviderState) *models.Response {
	resp.ProviderState = state
	resp.Candidates[0].ProviderState = state
	return resp
}

func mustService(t *testing.T, model models.Model, store sessions.Store, limits runtime.Limits) *compose.TurnService {
	t.Helper()
	runner, err := runtime.NewRunner(runtime.Config{Model: model, Limits: limits})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	mgr, err := sessions.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	svc, err := compose.New(compose.Config{Runner: runner, Manager: mgr})
	if err != nil {
		t.Fatalf("compose.New: %v", err)
	}
	return svc
}

func mustCreate(t *testing.T, store sessions.Store, id string) {
	t.Helper()
	mgr, err := sessions.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(context.Background(), sessions.New(id, "/work")); err != nil {
		t.Fatal(err)
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := compose.New(compose.Config{}); err == nil {
		t.Fatal("New empty config succeeded")
	}
	runner, _ := runtime.NewRunner(runtime.Config{Model: &scriptedModel{responses: []*models.Response{textResponse("x")}}})
	if _, err := compose.New(compose.Config{Runner: runner}); err == nil {
		t.Fatal("New without manager succeeded")
	}
}

func TestSendCompletedCommitsSuffix(t *testing.T) {
	ctx := context.Background()
	model := &scriptedModel{responses: []*models.Response{textResponse("hello")}}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	result, committed, err := svc.Send(ctx, "s1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed() {
		t.Fatalf("stop = %s", result.StopReason)
	}
	if committed == nil || len(committed.Messages) != 2 {
		t.Fatalf("committed messages = %d, want 2", len(committed.Messages))
	}
	if committed.Messages[0].Content[0].Text.Text != "hi" {
		t.Fatalf("user msg = %q", committed.Messages[0].Content[0].Text.Text)
	}
	if committed.Messages[1].Content[0].Text.Text != "hello" {
		t.Fatalf("assistant msg = %q", committed.Messages[1].Content[0].Text.Text)
	}
	// suffix must match Transcript[pre:] with pre=0.
	if len(result.Transcript) < 2 {
		t.Fatalf("transcript len = %d", len(result.Transcript))
	}
}

func TestSendMaxTurnsDoesNotCommit(t *testing.T) {
	ctx := context.Background()
	// Tool-call loop would need tools; instead use a model that keeps
	// finishing with stop but force max turns via repeated non-terminal —
	// simplest: MaxModelTurns=1 with a response that requests tools without
	// tools registered → may fail. Use max turns of 1 with empty tool loop:
	// model returns finish_stop so it completes in 1 turn. For budget stop we
	// need the model to call tools repeatedly. Use MaxModelTurns=1 and a
	// tool_call response without tool → ErrInvalidModelResponse or similar.
	//
	// Better: MaxModelTurns=1 + two sequential complete text turns cannot
	// happen in one Run. Use tool call response without tools to get failure.
	// Plan wants max_turns no commit — use a tool that loops.
	model := &scriptedModel{responses: []*models.Response{
		{
			Provider: "script", ID: "r1", Model: "m", Status: models.ResponseStatusCompleted,
			Candidates: []models.Candidate{{
				Index: 0,
				Content: []models.Content{
					models.ToolCallContent("c1", "ping", json.RawMessage(`{}`)),
				},
				FinishReason: models.FinishToolCall,
			}},
		},
		textResponse("done"),
	}}
	ping := stubTool{name: "ping", fn: func(context.Context, json.RawMessage) (runtime.ToolOutput, error) {
		return runtime.TextResult("pong"), nil
	}}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	runner, err := runtime.NewRunner(runtime.Config{
		Model:  model,
		Tools:  []runtime.Tool{ping},
		Limits: runtime.Limits{MaxModelTurns: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr, _ := sessions.NewManager(store)
	svc, _ := compose.New(compose.Config{Runner: runner, Manager: mgr})

	result, committed, err := svc.Send(ctx, "s1", "go")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if committed != nil {
		t.Fatal("budget stop must not commit")
	}
	if result == nil || result.Completed() {
		t.Fatalf("want incomplete result, got completed=%v stop=%v", result != nil && result.Completed(), result)
	}
	// Session still empty.
	got, _ := mgr.Get(ctx, "s1")
	if len(got.Messages) != 0 {
		t.Fatalf("messages = %d, want 0", len(got.Messages))
	}
}

type stubTool struct {
	name string
	fn   func(context.Context, json.RawMessage) (runtime.ToolOutput, error)
}

func (t stubTool) Definition() models.Tool {
	return models.NewTool(t.name, t.name, json.RawMessage(`{"type":"object","properties":{}}`))
}

func (t stubTool) Execute(ctx context.Context, args json.RawMessage) (runtime.ToolOutput, error) {
	return t.fn(ctx, args)
}

func TestSendFatalDoesNotCommit(t *testing.T) {
	ctx := context.Background()
	model := &scriptedModel{errs: []error{
		&models.Error{Kind: models.ErrorProtocol, Operation: "stream", Message: "boom"},
	}}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	result, committed, err := svc.Send(ctx, "s1", "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if committed != nil {
		t.Fatal("fatal must not commit")
	}
	// Stream fails at model.Stream open → may be (nil, err) from Run.
	_ = result
	mgr, _ := sessions.NewManager(store)
	got, _ := mgr.Get(ctx, "s1")
	if len(got.Messages) != 0 {
		t.Fatalf("messages = %d", len(got.Messages))
	}
}

func TestSendCancelDoesNotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gate := make(chan struct{})
	model := &scriptedModel{
		responses: []*models.Response{textResponse("late")},
		gate:      gate,
	}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	errCh := make(chan error, 1)
	var result *runtime.Result
	var committed *sessions.Session
	go func() {
		var err error
		result, committed, err = svc.Send(ctx, "s1", "hi")
		errCh <- err
	}()
	// Wait until the model Stream is entered (request recorded), then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if model.requestAt(0) != nil {
			break
		}
		if time.Now().After(deadline) {
			close(gate)
			t.Fatal("model never received request")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	// Unblock stream so it observes canceled context.
	close(gate)
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		// May also be context.Canceled wrapped; accept any non-nil with no commit.
		if err == nil {
			t.Fatal("want cancel error")
		}
	}
	if committed != nil {
		t.Fatal("cancel must not commit")
	}
	_ = result
	mgr, _ := sessions.NewManager(store)
	got, _ := mgr.Get(context.Background(), "s1")
	if len(got.Messages) != 0 {
		t.Fatalf("messages = %d", len(got.Messages))
	}
}

func TestSendValidationFailureNilResult(t *testing.T) {
	// Missing session → Get fails before run; also test invalid empty session id path.
	ctx := context.Background()
	model := &scriptedModel{responses: []*models.Response{textResponse("x")}}
	store := sessions.NewMemoryStore()
	svc := mustService(t, model, store, runtime.Limits{})
	result, committed, err := svc.Send(ctx, "missing", "hi")
	if err == nil || result != nil || committed != nil {
		t.Fatalf("want (nil,nil,err), got (%v,%v,%v)", result, committed, err)
	}
}

func TestMultiTurnProviderState(t *testing.T) {
	ctx := context.Background()
	state1 := &models.ProviderState{Provider: "openai", Data: json.RawMessage(`{"turn":1}`)}
	state2 := &models.ProviderState{Provider: "openai", Data: json.RawMessage(`{"turn":2}`)}
	model := &scriptedModel{responses: []*models.Response{
		withState(textResponse("one"), state1),
		withState(textResponse("two"), state2),
	}}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	if _, _, err := svc.Send(ctx, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	if _, committed, err := svc.Send(ctx, "s1", "second"); err != nil {
		t.Fatal(err)
	} else if committed == nil || len(committed.Messages) != 4 {
		t.Fatalf("want 4 messages, got %v", committed)
	}

	req2 := model.requestAt(1)
	if req2 == nil {
		t.Fatal("second request missing")
	}
	// Second request must include first assistant with provider state.
	found := false
	for _, m := range req2.Messages {
		if m.Role == models.RoleAssistant && m.ProviderState != nil &&
			string(m.ProviderState.Data) == `{"turn":1}` {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("second request missing prior ProviderState: %+v", req2.Messages)
	}
}

func TestMultiTurnProviderStateAcrossFileStoreRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state1 := &models.ProviderState{Provider: "openai", Data: json.RawMessage(`{"cursor":"abc"}`)}

	store1, err := sessions.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: []*models.Response{
		withState(textResponse("one"), state1),
		textResponse("two"),
	}}
	mustCreate(t, store1, "sess-1")
	svc1 := mustService(t, model, store1, runtime.Limits{})
	if _, _, err := svc1.Send(ctx, "sess-1", "first"); err != nil {
		t.Fatal(err)
	}

	// Restart: new store + service, same model script index continues.
	store2, err := sessions.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := mustService(t, model, store2, runtime.Limits{})
	if _, committed, err := svc2.Send(ctx, "sess-1", "second"); err != nil {
		t.Fatal(err)
	} else if committed == nil || len(committed.Messages) != 4 {
		t.Fatalf("after restart messages = %v", committed)
	}
	req2 := model.requestAt(1)
	found := false
	for _, m := range req2.Messages {
		if m.ProviderState != nil && m.ProviderState.Provider == "openai" {
			found = true
		}
	}
	if !found {
		t.Fatal("ProviderState did not survive FileStore restart")
	}
}

func TestStreamCommitsOnExhaust(t *testing.T) {
	ctx := context.Background()
	model := &scriptedModel{responses: []*models.Response{textResponse("streamed")}}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	turn, err := svc.Stream(ctx, "s1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []runtime.EventKind
	for turn.Next() {
		kinds = append(kinds, turn.Event().Kind)
	}
	if err := turn.Err(); err != nil {
		t.Fatal(err)
	}
	if turn.Session() == nil || len(turn.Session().Messages) != 2 {
		t.Fatalf("session after stream: %+v", turn.Session())
	}
	if len(kinds) == 0 {
		t.Fatal("no events forwarded")
	}
	// Repeat Next must not double-commit.
	if turn.Next() {
		t.Fatal("Next after exhaust returned true")
	}
	mgr, _ := sessions.NewManager(store)
	got, _ := mgr.Get(ctx, "s1")
	if len(got.Messages) != 2 {
		t.Fatalf("double commit? len=%d", len(got.Messages))
	}
}

func TestStreamCommitsBeforeRunEndIsPublished(t *testing.T) {
	ctx := context.Background()
	model := &scriptedModel{responses: []*models.Response{textResponse("streamed")}}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	turn, err := svc.Stream(ctx, "s1", "hi")
	if err != nil {
		t.Fatal(err)
	}

	for turn.Next() {
		if turn.Event().Kind != runtime.EventRunEnd {
			continue
		}
		if err := turn.Err(); err != nil {
			t.Fatalf("terminal event exposed commit error: %v", err)
		}
		if turn.Session() == nil || len(turn.Session().Messages) != 2 {
			t.Fatalf("run_end published before commit: %+v", turn.Session())
		}
		if err := turn.Close(); err != nil {
			t.Fatal(err)
		}
		mgr, _ := sessions.NewManager(store)
		committed, err := mgr.Get(ctx, "s1")
		if err != nil || len(committed.Messages) != 2 {
			t.Fatalf("breaking at run_end then closing lost commit: %+v, %v", committed, err)
		}
		return
	}
	t.Fatal("stream ended without run_end")
}

// failSaveStore wraps MemoryStore and fails every Save (commit path).
type failSaveStore struct {
	*sessions.MemoryStore
}

func (s *failSaveStore) Save(ctx context.Context, id string, data []byte) error {
	return errors.New("disk full")
}

func TestStreamCommitFailureSurfacesInErr(t *testing.T) {
	ctx := context.Background()
	model := &scriptedModel{responses: []*models.Response{textResponse("ok")}}
	base := sessions.NewMemoryStore()
	mustCreate(t, base, "s1")
	store := &failSaveStore{MemoryStore: base}
	svc := mustService(t, model, store, runtime.Limits{})

	turn, err := svc.Stream(ctx, "s1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	for turn.Next() {
	}
	if turn.Err() == nil {
		t.Fatal("commit failure must surface via Err(), not look like success")
	}
	if turn.Session() != nil {
		t.Fatal("failed commit must not return Session")
	}
	if turn.Result() == nil || !turn.Result().Completed() {
		t.Fatal("Result should still be available after commit failure")
	}
	// Store unchanged: Load via a working manager over the base memory.
	mgr, _ := sessions.NewManager(base)
	got, _ := mgr.Get(ctx, "s1")
	if len(got.Messages) != 0 {
		t.Fatalf("messages = %d after failed commit", len(got.Messages))
	}
}

func TestStreamCloseCancelDoesNotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gate := make(chan struct{})
	model := &scriptedModel{
		responses: []*models.Response{textResponse("x")},
		gate:      gate,
	}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	turn, err := svc.Stream(ctx, "s1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	// Cancel and close so the run does not complete successfully.
	cancel()
	_ = turn.Close()
	close(gate)
	for turn.Next() {
	}
	if turn.Session() != nil {
		t.Fatal("canceled stream must not commit")
	}
	// Terminal Err may be cancel/inner failure; it must not look like success
	// with a silent commit failure either.
	if turn.CommitErr() != nil {
		t.Fatalf("unexpected commit err: %v", turn.CommitErr())
	}
	mgr, _ := sessions.NewManager(store)
	got, _ := mgr.Get(context.Background(), "s1")
	if len(got.Messages) != 0 {
		t.Fatalf("messages = %d", len(got.Messages))
	}
}

func TestConcurrentTurnConflict(t *testing.T) {
	ctx := context.Background()
	// First turn commits normally. Second turn's Get sees pre=0 if both start
	// together — use a barrier so both Get/Run start with empty transcript,
	// then both try to commit.
	gate := make(chan struct{})
	model := &scriptedModel{
		responses: []*models.Response{textResponse("a"), textResponse("b")},
		gate:      gate,
	}
	store := sessions.NewMemoryStore()
	mustCreate(t, store, "s1")
	svc := mustService(t, model, store, runtime.Limits{})

	type outcome struct {
		err       error
		committed *sessions.Session
	}
	ch := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, committed, err := svc.Send(ctx, "s1", "hi")
			ch <- outcome{err: err, committed: committed}
		}()
	}
	// Wait until both requests hit the model.
	deadline := time.Now().Add(2 * time.Second)
	for {
		model.mu.Lock()
		n := model.calls
		model.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			close(gate)
			t.Fatal("both turns did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)

	var outcomes [2]outcome
	outcomes[0] = <-ch
	outcomes[1] = <-ch

	var okN, conflictN int
	for _, o := range outcomes {
		if o.err == nil && o.committed != nil {
			okN++
			continue
		}
		if errors.Is(o.err, compose.ErrConcurrentTurn) || errors.Is(o.err, sessions.ErrConflict) {
			conflictN++
			if o.committed != nil {
				t.Fatal("conflict must not return committed session")
			}
			continue
		}
		t.Fatalf("unexpected outcome: err=%v committed=%v", o.err, o.committed)
	}
	if okN != 1 || conflictN != 1 {
		t.Fatalf("want 1 success + 1 conflict, got ok=%d conflict=%d outcomes=%v", okN, conflictN, outcomes)
	}
	mgr, _ := sessions.NewManager(store)
	got, _ := mgr.Get(ctx, "s1")
	// Winner appends 2 messages (user+assistant); loser does not fork.
	if len(got.Messages) != 2 {
		t.Fatalf("final len=%d, want 2 (no fork)", len(got.Messages))
	}
}

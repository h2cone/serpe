package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/ouro/core/models"
)

// flakyStore wraps MemoryStore with controllable Save failure and blocking.
type flakyStore struct {
	*MemoryStore
	failSave  bool
	blockSave chan struct{}
	saving    chan struct{}
	saveOnce  sync.Once
}

func (f *flakyStore) Save(ctx context.Context, session *Session) error {
	if f.failSave {
		return errors.New("save failed")
	}
	if f.blockSave != nil {
		f.saveOnce.Do(func() { close(f.saving) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.blockSave:
		}
	}
	return f.MemoryStore.Save(ctx, session)
}

func mustManager(t *testing.T, store Store) *Manager {
	t.Helper()
	m, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestNewManagerNilStore(t *testing.T) {
	if _, err := NewManager(nil); err == nil {
		t.Fatal("NewManager(nil) succeeded, want error")
	}
}

func TestCreateGet(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()

	if _, err := m.Create(ctx, nil); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Create(nil) = %v, want ErrInvalidSession", err)
	}
	if _, err := m.Create(ctx, &Session{ID: "s1"}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Create(invalid) = %v, want ErrInvalidSession", err)
	}
	created, err := m.Create(ctx, New("s1", "/wd"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != "s1" || created.CWD != "/wd" {
		t.Fatalf("unexpected created snapshot: %+v", created)
	}
	if _, err := m.Create(ctx, New("s1", "/other")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create = %v, want ErrAlreadyExists", err)
	}
	if _, err := m.Get(ctx, "s1 "); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Get with invalid ID = %v, want ErrInvalidSession", err)
	}
	if _, err := m.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}

	// Returned snapshots are independent of stored state.
	got, err := m.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	got.CWD = "/mutated"
	got.Metadata = map[string]string{"title": "x"}
	again, _ := m.Get(ctx, "s1")
	if again.CWD != "/wd" || again.Metadata != nil {
		t.Fatal("Get exposed internal storage")
	}
}

func TestUpdateCommit(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}
	first, err := m.Update(ctx, "s1", func(s *Session) error {
		s.CWD = "/a"
		s.Metadata = map[string]string{"title": "t"}
		s.UpdatedAt = time.Time{} // manager-owned, must be ignored
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if first.CWD != "/a" || first.Metadata["title"] != "t" {
		t.Fatalf("Update did not commit: %+v", first)
	}
	if first.UpdatedAt.IsZero() || first.UpdatedAt.Before(first.CreatedAt) {
		t.Fatalf("UpdatedAt not manager-maintained: %+v", first.UpdatedAt)
	}
	second, err := m.Update(ctx, "s1", func(s *Session) error {
		s.CWD = "/b"
		return nil
	})
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("UpdatedAt went backwards: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
	if _, err := m.Update(ctx, "s1", nil); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Update with nil mutator = %v, want ErrInvalidSession", err)
	}
}

func TestUpdateFailureRollsBack(t *testing.T) {
	ctx := context.Background()

	t.Run("mutator error", func(t *testing.T) {
		m := mustManager(t, NewMemoryStore())
		if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
			t.Fatal(err)
		}
		mutErr := errors.New("mutator failed")
		_, err := m.Update(ctx, "s1", func(s *Session) error {
			s.CWD = "/mutated"
			s.Messages = append(s.Messages, models.NewUserMessage(models.Text("x")))
			return mutErr
		})
		if !errors.Is(err, mutErr) {
			t.Fatalf("Update = %v, want mutator error", err)
		}
		got, _ := m.Get(ctx, "s1")
		if got.CWD != "/wd" || len(got.Messages) != 0 {
			t.Fatalf("mutator error polluted stored state: %+v", got)
		}
	})

	t.Run("store error", func(t *testing.T) {
		m := mustManager(t, &flakyStore{MemoryStore: NewMemoryStore(), failSave: true})
		if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
			t.Fatal(err)
		}
		_, err := m.Update(ctx, "s1", func(s *Session) error {
			s.CWD = "/mutated"
			return nil
		})
		if err == nil {
			t.Fatal("Update succeeded despite failing store")
		}
		got, _ := m.Get(ctx, "s1")
		if got.CWD != "/wd" {
			t.Fatalf("save error polluted stored state: %+v", got)
		}
	})
}

func TestUpdateProtectsImmutableState(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(ctx, "s1",
		models.NewUserMessage(models.Text("one")),
		models.NewAssistantMessage(models.Text("two")),
	); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Get(ctx, "s1")

	attempts := []struct {
		name   string
		mutate func(*Session) error
	}{
		{"change ID", func(s *Session) error { s.ID = "other"; return nil }},
		{"change parent", func(s *Session) error { s.ParentID = "p"; return nil }},
		{"change created", func(s *Session) error { s.CreatedAt = time.Now().UTC(); return nil }},
		{"rewrite message", func(s *Session) error {
			s.Messages[0].Content[0].Text.Text = "rewritten"
			return nil
		}},
		{"truncate", func(s *Session) error { s.Messages = s.Messages[:1]; return nil }},
		{"reorder", func(s *Session) error {
			s.Messages[0], s.Messages[1] = s.Messages[1], s.Messages[0]
			return nil
		}},
	}
	for _, tt := range attempts {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := m.Update(ctx, "s1", tt.mutate); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("Update = %v, want ErrInvalidSession", err)
			}
			after, _ := m.Get(ctx, "s1")
			if len(after.Messages) != len(before.Messages) ||
				!after.Messages[0].Equal(before.Messages[0]) ||
				!after.Messages[1].Equal(before.Messages[1]) {
				t.Fatal("rejected update modified stored transcript")
			}
		})
	}
}

func TestAppend(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(ctx, "s1"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("empty Append = %v, want ErrInvalidSession", err)
	}

	user := models.NewUserMessage(models.Text("hi"))
	assistant := models.NewAssistantMessage(models.Text("hello"))
	got, err := m.Append(ctx, "s1", user, assistant)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Content[0].Text.Text != "hi" || got.Messages[1].Content[0].Text.Text != "hello" {
		t.Fatalf("Append lost order: %+v", got.Messages)
	}

	// Mutating the input after Append must not alias stored state.
	user.Content[0].Text.Text = "mutated"
	check, _ := m.Get(ctx, "s1")
	if check.Messages[0].Content[0].Text.Text != "hi" {
		t.Fatal("Append retained input storage")
	}

	// A batch with an invalid message must not commit any of it.
	if _, err := m.Append(ctx, "s1",
		models.NewUserMessage(models.Text("ok")),
		models.Message{Role: "bogus"},
	); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Append with invalid message = %v, want ErrInvalidSession", err)
	}
	check, _ = m.Get(ctx, "s1")
	if len(check.Messages) != 2 {
		t.Fatalf("failed batch committed partially: %d messages", len(check.Messages))
	}
}

func TestFork(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	src := New("s1", "/wd")
	src.Metadata = map[string]string{"title": "t"}
	if _, err := m.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(ctx, "s1", models.NewUserMessage(models.Text("hi"))); err != nil {
		t.Fatal(err)
	}
	parent, _ := m.Get(ctx, "s1")

	// The Windows wall clock advances in ~15ms ticks; sleep past a tick so
	// the fork's fresh timestamp is strictly observable.
	time.Sleep(25 * time.Millisecond)
	child, err := m.Fork(ctx, "s1", "s2")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if child.ID != "s2" || child.ParentID != "s1" || child.CWD != "/wd" || child.Metadata["title"] != "t" {
		t.Fatalf("unexpected fork: %+v", child)
	}
	if len(child.Messages) != 1 || !child.Messages[0].Equal(parent.Messages[0]) {
		t.Fatal("fork did not copy the transcript")
	}
	if child.CreatedAt.IsZero() || !child.UpdatedAt.Equal(child.CreatedAt) {
		t.Fatal("fork timestamps must be fresh and equal")
	}
	if child.CreatedAt.Equal(parent.CreatedAt) {
		t.Fatal("fork reuses the source creation time")
	}

	// Source and child are isolated at every depth.
	child.Messages[0].Content[0].Text.Text = "mutated"
	child.Metadata["title"] = "mutated"
	check, _ := m.Get(ctx, "s1")
	if check.Messages[0].Content[0].Text.Text != "hi" || check.Metadata["title"] != "t" {
		t.Fatal("fork aliases source storage")
	}
	check2, _ := m.Get(ctx, "s2")
	if check2.Messages[0].Content[0].Text.Text != "hi" || check2.Metadata["title"] != "t" {
		t.Fatal("fork snapshot aliases child storage")
	}

	// Failure cases create nothing.
	if _, err := m.Fork(ctx, "s1", "s1"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Fork same ID = %v, want ErrInvalidSession", err)
	}
	if _, err := m.Fork(ctx, "missing", "s3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Fork missing source = %v, want ErrNotFound", err)
	}
	if _, err := m.Fork(ctx, "s1", "s2"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("Fork existing new ID = %v, want ErrAlreadyExists", err)
	}
	if _, err := m.Get(ctx, "s3"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed fork left a partial record")
	}
}

func TestManagerContextCanceled(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Create(ctx, New("s1", "/wd")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create = %v, want context.Canceled", err)
	}
	if _, err := m.Get(context.Background(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("canceled Create modified state")
	}
}

func TestConcurrentSameIDNoLostUpdate(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}
	const goroutines, perGoroutine = 8, 5
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				text := fmt.Sprintf("g%d-%d", g, i)
				if _, err := m.Append(ctx, "s1", models.NewUserMessage(models.Text(text))); err != nil {
					t.Errorf("Append(%s): %v", text, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	got, err := m.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != goroutines*perGoroutine {
		t.Fatalf("lost updates: got %d messages, want %d", len(got.Messages), goroutines*perGoroutine)
	}
	seen := map[string]bool{}
	for i := range got.Messages {
		text := got.Messages[i].Content[0].Text.Text
		if seen[text] {
			t.Fatalf("duplicate message %q", text)
		}
		seen[text] = true
	}
}

func TestConcurrentDifferentIDsParallel(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("s%d", i)
			if _, err := m.Create(ctx, New(id, "/wd")); err != nil {
				errs <- err
				return
			}
			if _, err := m.Append(ctx, id, models.NewUserMessage(models.Text("hi"))); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("parallel operation failed: %v", err)
	}
	for i := 0; i < n; i++ {
		got, err := m.Get(ctx, fmt.Sprintf("s%d", i))
		if err != nil || len(got.Messages) != 1 {
			t.Fatalf("session s%d = %v, %v", i, got, err)
		}
	}
}

// TestAppendBlocksDelete verifies that Delete waits for an in-flight Append
// on the same ID instead of interleaving.
func TestAppendBlocksDelete(t *testing.T) {
	blockSave := make(chan struct{})
	store := &flakyStore{MemoryStore: NewMemoryStore(), blockSave: blockSave, saving: make(chan struct{})}
	m := mustManager(t, store)
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}

	// Append blocks inside Save while holding the s1 write lease.
	appendDone := make(chan struct{})
	go func() {
		_, _ = m.Append(ctx, "s1", models.NewUserMessage(models.Text("a")))
		close(appendDone)
	}()
	<-store.saving

	deleteDone := make(chan struct{})
	go func() {
		_ = m.Delete(ctx, "s1")
		close(deleteDone)
	}()
	select {
	case <-appendDone:
		t.Fatal("append finished before the block was released")
	default:
	}

	close(blockSave)
	<-appendDone
	<-deleteDone

	if _, err := m.Get(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete did not run after the append")
	}
}

// TestAppendBlocksFork verifies that Fork observes the fully committed
// transcript and cannot see a half-applied batch.
func TestAppendBlocksFork(t *testing.T) {
	blockSave := make(chan struct{})
	store := &flakyStore{MemoryStore: NewMemoryStore(), blockSave: blockSave, saving: make(chan struct{})}
	m := mustManager(t, store)
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}

	appendDone := make(chan struct{})
	go func() {
		_, _ = m.Append(ctx, "s1", models.NewUserMessage(models.Text("a")))
		close(appendDone)
	}()
	<-store.saving

	forkDone := make(chan struct{})
	go func() {
		_, _ = m.Fork(ctx, "s1", "s1-fork")
		close(forkDone)
	}()
	select {
	case <-appendDone:
		t.Fatal("append finished before the block was released")
	default:
	}

	close(blockSave)
	<-appendDone
	<-forkDone

	child, err := m.Get(ctx, "s1-fork")
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Messages) != 1 {
		t.Fatalf("fork saw %d messages, want the single committed append", len(child.Messages))
	}
	if child.ParentID != "s1" {
		t.Fatalf("fork parent = %q", child.ParentID)
	}
}

// TestProviderStateRoundTrip emulates the agent runtime flow: an assistant
// message produced by Response.AssistantMessage keeps its provider state
// through session append/get and can drive the next request.
func TestProviderStateRoundTrip(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
		t.Fatal(err)
	}
	resp := &models.Response{
		Candidates: []models.Candidate{{
			Index: 0,
			Content: []models.Content{
				models.Text("hello"),
				models.ToolCallContent("c1", "lookup", json.RawMessage(`{"q":"x"}`)),
			},
		}},
		ProviderState: &models.ProviderState{Provider: "openai", Data: json.RawMessage(`{"next":1}`)},
	}
	assistant, err := resp.AssistantMessage(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Append(ctx, "s1",
		models.NewUserMessage(models.Text("hi")), assistant,
	); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Messages[1].Equal(assistant) {
		t.Fatal("provider state changed across the session round trip")
	}
	// The next request carries the same provider state.
	req := &models.Request{Messages: append([]models.Message(nil), got.Messages...)}
	if string(req.Messages[1].ProviderState.Data) != `{"next":1}` || req.Messages[1].ProviderState.Provider != "openai" {
		t.Fatal("provider state did not reach the next request")
	}
}

func TestLockTableReclaimed(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("s%d", i)
		if _, err := m.Create(ctx, New(id, "/wd")); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Append(ctx, id, models.NewUserMessage(models.Text("x"))); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Fork(ctx, id, id+"-f"); err != nil {
			t.Fatal(err)
		}
		if err := m.Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := m.Delete(ctx, id+"-f"); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.locks) != 0 {
		t.Fatalf("lock table not reclaimed: %d entries", len(m.locks))
	}
}

// loadFailingStore wraps MemoryStore and fails the next Load on demand, to
// simulate a context canceled between a successful write and the post-write
// read-back.
type loadFailingStore struct {
	*MemoryStore
	failNextLoad bool
}

func (s *loadFailingStore) Load(ctx context.Context, id string) (*Session, error) {
	if s.failNextLoad {
		s.failNextLoad = false
		return nil, context.Canceled
	}
	return s.MemoryStore.Load(ctx, id)
}

// TestCommittedWriteSurvivesPostWriteLoadFailure verifies that when the store
// write succeeds but the following read-back fails (e.g. a context canceled
// after the write landed), the Manager reports success and a snapshot of the
// committed state rather than an error for an already-durable change.
func TestCommittedWriteSurvivesPostWriteLoadFailure(t *testing.T) {
	ctx := context.Background()

	t.Run("update", func(t *testing.T) {
		store := &loadFailingStore{MemoryStore: NewMemoryStore()}
		m := mustManager(t, store)
		if _, err := m.Create(ctx, New("s1", "/wd")); err != nil {
			t.Fatal(err)
		}
		// Fail the post-Save read-back (inside Manager.committed). The mutator
		// runs after the initial Load and before Save, so this targets exactly
		// the committed Load.
		got, err := m.Update(ctx, "s1", func(s *Session) error {
			s.CWD = "/committed"
			store.failNextLoad = true
			return nil
		})
		if err != nil {
			t.Fatalf("Update returned %v for a committed write", err)
		}
		if got.CWD != "/committed" {
			t.Fatalf("snapshot = %+v, want /committed", got)
		}
		check, _ := m.Get(ctx, "s1")
		if check.CWD != "/committed" {
			t.Fatalf("committed change not durable: %+v", check)
		}
	})

	t.Run("create", func(t *testing.T) {
		store := &loadFailingStore{MemoryStore: NewMemoryStore()}
		store.failNextLoad = true
		m := mustManager(t, store)
		got, err := m.Create(ctx, New("s1", "/wd"))
		if err != nil {
			t.Fatalf("Create returned %v for a committed write", err)
		}
		if got.ID != "s1" || got.CWD != "/wd" {
			t.Fatalf("snapshot = %+v", got)
		}
		check, _ := m.Get(ctx, "s1")
		if check == nil || check.CWD != "/wd" {
			t.Fatalf("committed create not durable: %+v", check)
		}
	})
}

// noCloneStore stores and returns session pointers without cloning, so it
// isolates whether the Manager clones at its own boundary rather than relying
// on the store.
type noCloneStore struct {
	saved map[string]*Session
}

func (s *noCloneStore) Create(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.saved[session.ID]; ok {
		return ErrAlreadyExists
	}
	s.saved[session.ID] = session
	return nil
}

func (s *noCloneStore) Load(ctx context.Context, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validID(id) {
		return nil, invalidf("invalid ID %q", id)
	}
	got, ok := s.saved[id]
	if !ok {
		return nil, ErrNotFound
	}
	return got, nil
}

func (s *noCloneStore) Save(ctx context.Context, session *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.saved[session.ID]; !ok {
		return ErrNotFound
	}
	s.saved[session.ID] = session
	return nil
}

func (s *noCloneStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	delete(s.saved, id)
	return nil
}

// TestManagerClonesAtWriteBoundary verifies the Manager clones caller input at
// its write boundary (Create) and deep-copies the source transcript into a
// forked child rather than aliasing it, using a store that does not clone.
func TestManagerClonesAtWriteBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("create input is cloned", func(t *testing.T) {
		store := &noCloneStore{saved: map[string]*Session{}}
		m := mustManager(t, store)
		input := New("s1", "/wd")
		input.Messages = []models.Message{models.NewUserMessage(models.Text("hi"))}
		if _, err := m.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
		// Mutate the caller's input after Create.
		input.CWD = "/mutated"
		input.Messages[0].Content[0].Text.Text = "mutated"
		got, _ := m.Get(ctx, "s1")
		if got.CWD != "/wd" || got.Messages[0].Content[0].Text.Text != "hi" {
			t.Fatalf("Create did not clone input at the boundary: %+v", got)
		}
	})

	t.Run("fork child does not alias source", func(t *testing.T) {
		store := &noCloneStore{saved: map[string]*Session{}}
		m := mustManager(t, store)
		src := New("s1", "/wd")
		src.Messages = []models.Message{models.NewUserMessage(models.Text("hi"))}
		if _, err := m.Create(ctx, src); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Fork(ctx, "s1", "s2"); err != nil {
			t.Fatal(err)
		}
		// With a non-cloning store, Get returns the stored pointer, so mutating
		// the source transcript must not affect the forked child.
		source, _ := m.Get(ctx, "s1")
		child, _ := m.Get(ctx, "s2")
		source.Messages[0].Content[0].Text.Text = "mutated"
		if child.Messages[0].Content[0].Text.Text != "hi" {
			t.Fatalf("Fork aliased the source transcript into the child: %+v", child.Messages[0])
		}
	})
}

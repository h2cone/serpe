package sessions

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
)

// flakyStore wraps MemoryStore with controllable Save failure and blocking.
type flakyStore struct {
	*MemoryStore
	failSave  bool
	blockSave chan struct{}
	saving    chan struct{}
	saveOnce  sync.Once
}

func (f *flakyStore) Save(ctx context.Context, id string, data []byte) error {
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
	return f.MemoryStore.Save(ctx, id, data)
}

func mustManager(t *testing.T, store Store) *Manager {
	t.Helper()
	m, err := NewManager(store, Limits{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestNewManagerNilStore(t *testing.T) {
	if _, err := NewManager(nil, Limits{}); err == nil {
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
	created, err := m.Create(ctx, New("s1", testCWD("wd")))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != "s1" || created.CWD != testCWD("wd") {
		t.Fatalf("unexpected created snapshot: %+v", created)
	}
	if _, err := m.Create(ctx, New("s1", testCWD("other"))); !errors.Is(err, ErrAlreadyExists) {
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
	if again.CWD != testCWD("wd") || again.Metadata != nil {
		t.Fatal("Get exposed internal storage")
	}
}

func TestManagerRejectsRecordKeyMismatch(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	data, err := marshalSession(New("payload-id", testCWD("wd")))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "storage-id", data); err != nil {
		t.Fatal(err)
	}
	m := mustManager(t, store)
	if _, err := m.Get(ctx, "storage-id"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Get key/payload mismatch = %v, want ErrInvalidSession", err)
	}
	listed, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("List exposed mismatched record: %+v", listed)
	}
}

func TestPatchCWDAndMetadata(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
		t.Fatal(err)
	}
	newCWD := testCWD("new")
	got, err := m.Patch(ctx, "s1", SessionPatch{CWD: &newCWD})
	if err != nil {
		t.Fatal(err)
	}
	if got.CWD != newCWD {
		t.Fatalf("CWD = %q", got.CWD)
	}
	if got.UpdatedAt.Before(got.CreatedAt) {
		t.Fatalf("UpdatedAt went backwards: %v", got.UpdatedAt)
	}
	blank := "  "
	if _, err := m.Patch(ctx, "s1", SessionPatch{CWD: &blank}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("blank CWD = %v", err)
	}
	title, owner := "t", "u"
	got, err = m.Patch(ctx, "s1", SessionPatch{Metadata: map[string]*string{"title": &title, "owner": &owner}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["title"] != "t" || got.Metadata["owner"] != "u" {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
	next := "x"
	in := map[string]*string{"title": &next}
	got, err = m.Patch(ctx, "s1", SessionPatch{Metadata: in})
	if err != nil {
		t.Fatal(err)
	}
	next = "mut"
	again, _ := m.Get(ctx, "s1")
	if again.Metadata["title"] != "x" {
		t.Fatal("Patch must copy metadata values")
	}
	if _, err := m.Patch(ctx, "s1", SessionPatch{Metadata: map[string]*string{"title": nil, "owner": nil}}); err != nil {
		t.Fatal(err)
	}
	got, _ = m.Get(ctx, "s1")
	if got.Metadata != nil {
		t.Fatalf("clear metadata: %+v", got.Metadata)
	}
	if _, err := m.Patch(ctx, "s1", SessionPatch{Metadata: map[string]*string{"bad key": nil}}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("bad metadata key = %v", err)
	}
	_ = again
}

func TestPatchMetadataPreservesConcurrentKeys(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			key := fmt.Sprintf("key-%d", i)
			value := fmt.Sprintf("value-%d", i)
			_, err := m.Patch(ctx, "s1", SessionPatch{Metadata: map[string]*string{key: &value}})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := m.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metadata) != writers {
		t.Fatalf("metadata keys = %d, want %d: %+v", len(got.Metadata), writers, got.Metadata)
	}
	for i := 0; i < writers; i++ {
		key := fmt.Sprintf("key-%d", i)
		if want := fmt.Sprintf("value-%d", i); got.Metadata[key] != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got.Metadata[key], want)
		}
	}

	if _, err := m.Patch(ctx, "s1", SessionPatch{Metadata: map[string]*string{"key-0": nil}}); err != nil {
		t.Fatal(err)
	}
	got, _ = m.Get(ctx, "s1")
	if _, ok := got.Metadata["key-0"]; ok {
		t.Fatal("nil patch did not delete key-0")
	}
	if _, err := m.Patch(ctx, "s1", SessionPatch{Metadata: map[string]*string{"bad key": nil}}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("invalid patch key = %v, want ErrInvalidSession", err)
	}
}

func TestIntentWriteStoreFailureRollsBack(t *testing.T) {
	store := &flakyStore{MemoryStore: NewMemoryStore()}
	m := mustManager(t, store)
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
		t.Fatal(err)
	}
	store.failSave = true
	mutated := "/mutated"
	if _, err := m.Patch(ctx, "s1", SessionPatch{CWD: &mutated}); err == nil {
		t.Fatal("Patch succeeded despite failing store")
	}
	got, _ := m.Get(ctx, "s1")
	if got.CWD != testCWD("wd") {
		t.Fatalf("save error polluted stored state: %+v", got)
	}
}

func TestAppend(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
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

func TestAppendAt(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendAt(ctx, "s1", 0); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("empty AppendAt = %v, want ErrInvalidSession", err)
	}
	if _, err := m.AppendAt(ctx, "s1", -1, models.NewUserMessage(models.Text("x"))); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("negative at = %v, want ErrInvalidSession", err)
	}

	got, err := m.AppendAt(ctx, "s1", 0,
		models.NewUserMessage(models.Text("hi")),
		models.NewAssistantMessage(models.Text("hello")),
	)
	if err != nil {
		t.Fatalf("AppendAt: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("len = %d, want 2", len(got.Messages))
	}

	// Wrong expected length → conflict, no write.
	if _, err := m.AppendAt(ctx, "s1", 0, models.NewUserMessage(models.Text("fork"))); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale AppendAt = %v, want ErrConflict", err)
	}
	check, _ := m.Get(ctx, "s1")
	if len(check.Messages) != 2 {
		t.Fatalf("conflict mutated transcript: %d", len(check.Messages))
	}

	// Correct length continues the transcript.
	got, err = m.AppendAt(ctx, "s1", 2, models.NewUserMessage(models.Text("again")))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 || got.Messages[2].Content[0].Text.Text != "again" {
		t.Fatalf("AppendAt continue: %+v", got.Messages)
	}
}

func TestFork(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	src := New("s1", testCWD("wd"))
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
	if child.ID != "s2" || child.ParentID != "s1" || child.CWD != testCWD("wd") || child.Metadata["title"] != "t" {
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
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create = %v, want context.Canceled", err)
	}
	if _, err := m.Get(context.Background(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("canceled Create modified state")
	}
}

func TestConcurrentSameIDNoLostUpdate(t *testing.T) {
	m := mustManager(t, NewMemoryStore())
	ctx := context.Background()
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
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
			if _, err := m.Create(ctx, New(id, testCWD("wd"))); err != nil {
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
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
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
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
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
	if _, err := m.Create(ctx, New("s1", testCWD("wd"))); err != nil {
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
		if _, err := m.Create(ctx, New(id, testCWD("wd"))); err != nil {
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

// noCopyStore deliberately aliases record byte slices. It verifies that the
// Manager's codec boundary does not expose caller-owned Session memory even
// when a third-party backend violates Store's byte-ownership contract.
type noCopyStore struct {
	saved map[string][]byte
}

func (s *noCopyStore) Create(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.saved[id]; ok {
		return ErrAlreadyExists
	}
	s.saved[id] = data
	return nil
}

func (s *noCopyStore) Load(ctx context.Context, id string) ([]byte, error) {
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

func (s *noCopyStore) Save(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	s.saved[id] = data
	return nil
}

func (s *noCopyStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.saved[id]; !ok {
		return ErrNotFound
	}
	delete(s.saved, id)
	return nil
}

func (s *noCopyStore) List(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(s.saved))
	for id, data := range s.saved {
		out = append(out, Record{ID: id, Data: data})
	}
	return out, nil
}

func (s *noCopyStore) ListIDsPage(ctx context.Context, afterID string, limit int) ([]string, string, error) {
	if err := validatePage(afterID, limit); err != nil {
		return nil, "", err
	}
	selected := &maxIDHeap{}
	heap.Init(selected)
	for id := range s.saved {
		selectPageID(selected, id, afterID, limit+1)
	}
	ids, next := completeIDPage(selected, limit)
	return ids, next, ctx.Err()
}

func (s *noCopyStore) Close() error { return nil }

// TestManagerClonesAtWriteBoundary verifies the Manager codec owns caller
// input and produces independent source/fork snapshots even with a backend
// that does not honor byte ownership.
func TestManagerClonesAtWriteBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("create input is cloned", func(t *testing.T) {
		store := &noCopyStore{saved: map[string][]byte{}}
		m := mustManager(t, store)
		input := New("s1", testCWD("wd"))
		input.Messages = []models.Message{models.NewUserMessage(models.Text("hi"))}
		if _, err := m.Create(ctx, input); err != nil {
			t.Fatal(err)
		}
		// Mutate the caller's input after Create.
		input.CWD = "/mutated"
		input.Messages[0].Content[0].Text.Text = "mutated"
		got, _ := m.Get(ctx, "s1")
		if got.CWD != testCWD("wd") || got.Messages[0].Content[0].Text.Text != "hi" {
			t.Fatalf("Create did not clone input at the boundary: %+v", got)
		}
	})

	t.Run("fork child does not alias source", func(t *testing.T) {
		store := &noCopyStore{saved: map[string][]byte{}}
		m := mustManager(t, store)
		src := New("s1", testCWD("wd"))
		src.Messages = []models.Message{models.NewUserMessage(models.Text("hi"))}
		if _, err := m.Create(ctx, src); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Fork(ctx, "s1", "s2"); err != nil {
			t.Fatal(err)
		}
		// Decoded snapshots remain independent even when stored bytes alias.
		source, _ := m.Get(ctx, "s1")
		child, _ := m.Get(ctx, "s2")
		source.Messages[0].Content[0].Text.Text = "mutated"
		if child.Messages[0].Content[0].Text.Text != "hi" {
			t.Fatalf("Fork aliased the source transcript into the child: %+v", child.Messages[0])
		}
	})
}

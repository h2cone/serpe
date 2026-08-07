package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/h2cone/ouro/core/models"
)

// storeFactory builds a fresh Store for contract tests. FileStore factories
// typically use t.TempDir().
type storeFactory func(t *testing.T) Store

func TestStoreContract_Memory(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store {
		t.Helper()
		return NewMemoryStore()
	})
}

func TestStoreContract_File(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store {
		t.Helper()
		s, err := NewFileStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return s
	})
}

func runStoreContract(t *testing.T, newStore storeFactory) {
	t.Helper()
	t.Run("lifecycle", func(t *testing.T) { contractLifecycle(t, newStore(t)) })
	t.Run("ownership", func(t *testing.T) { contractOwnership(t, newStore(t)) })
	t.Run("context_canceled", func(t *testing.T) { contractContextCanceled(t, newStore(t)) })
	t.Run("invalid_id_load", func(t *testing.T) { contractInvalidID(t, newStore(t)) })
	t.Run("concurrent_create", func(t *testing.T) { contractConcurrentCreate(t, newStore(t)) })
}

func contractLifecycle(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	s := New("s1", "/wd")
	if err := store.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, New("s1", "/other")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create = %v, want ErrAlreadyExists", err)
	}
	got, err := store.Load(ctx, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CWD != "/wd" {
		t.Fatalf("Load CWD = %q", got.CWD)
	}
	updated := got.Clone()
	updated.CWD = "/new"
	if err := store.Save(ctx, updated); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save(ctx, New("missing", "/w")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Save of missing = %v, want ErrNotFound", err)
	}
	got, _ = store.Load(ctx, "s1")
	if got.CWD != "/new" {
		t.Fatalf("after Save CWD = %q, want /new", got.CWD)
	}
	if err := store.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	if _, err := store.Load(ctx, "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after Delete = %v, want ErrNotFound", err)
	}
}

func contractOwnership(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	in := New("s1", "/wd")
	if err := store.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.CWD = "/mutated"
	got, err := store.Load(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.CWD != "/wd" {
		t.Fatalf("Create retained input storage: CWD = %q", got.CWD)
	}
	got.CWD = "/mutated"
	got.Metadata = map[string]string{"title": "x"}
	again, _ := store.Load(ctx, "s1")
	if again.CWD != "/wd" || again.Metadata != nil {
		t.Fatal("Load exposed internal storage")
	}
	saved := New("s1", "/wd")
	saved.Messages = append(saved.Messages, models.NewUserMessage(models.Text("hi")))
	if err := store.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}
	saved.Messages[0].Content[0].Text.Text = "mutated"
	check, _ := store.Load(ctx, "s1")
	if check.Messages[0].Content[0].Text.Text != "hi" {
		t.Fatal("Save retained input storage")
	}
}

func contractContextCanceled(t *testing.T, store Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Create(ctx, New("s1", "/wd")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create canceled = %v", err)
	}
	// Ensure nothing was written: open a live context for Load.
	if _, err := store.Load(context.Background(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canceled Create left state: %v", err)
	}
}

func contractInvalidID(t *testing.T, store Store) {
	t.Helper()
	_, err := store.Load(context.Background(), " ")
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Load invalid ID = %v, want ErrInvalidSession", err)
	}
}

func contractConcurrentCreate(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- store.Create(ctx, New("s1", "/wd"))
		}()
	}
	wg.Wait()
	close(errCh)
	var okN, existsN int
	for err := range errCh {
		switch {
		case err == nil:
			okN++
		case errors.Is(err, ErrAlreadyExists):
			existsN++
		default:
			t.Fatalf("Create = %v", err)
		}
	}
	if okN != 1 || existsN != n-1 {
		t.Fatalf("want 1 ok + %d already-exists, got ok=%d exists=%d", n-1, okN, existsN)
	}
}

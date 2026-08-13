package sessions

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

// storeFactory builds a fresh opaque-record backend for contract tests.
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
		s, err := NewFileStore(privateTempDir(t))
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return s
	})
}

func runStoreContract(t *testing.T, newStore storeFactory) {
	t.Helper()
	withStore := func(t *testing.T) Store {
		store := newStore(t)
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
		return store
	}
	t.Run("lifecycle", func(t *testing.T) { contractLifecycle(t, withStore(t)) })
	t.Run("ownership", func(t *testing.T) { contractOwnership(t, withStore(t)) })
	t.Run("context_canceled", func(t *testing.T) { contractContextCanceled(t, withStore(t)) })
	t.Run("invalid_id_load", func(t *testing.T) { contractInvalidID(t, withStore(t)) })
	t.Run("concurrent_create", func(t *testing.T) { contractConcurrentCreate(t, withStore(t)) })
	t.Run("list", func(t *testing.T) { contractList(t, withStore(t)) })
}

func contractList(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	empty, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("List empty len=%d, want 0", len(empty))
	}
	if err := store.Create(ctx, "a", []byte("one")); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if err := store.Create(ctx, "b", []byte("two")); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	got, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len=%d, want 2", len(got))
	}
	records := make(map[string]string, len(got))
	for i := range got {
		records[got[i].ID] = string(got[i].Data)
		got[i].Data[0] = 'x'
	}
	if records["a"] != "one" || records["b"] != "two" {
		t.Fatalf("List records=%v", records)
	}
	loaded, err := store.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded) != "one" {
		t.Fatalf("List exposed backend bytes: %q", loaded)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.List(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("List canceled = %v, want context.Canceled", err)
	}
}

func contractLifecycle(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.Create(ctx, "s1", []byte("one")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, "s1", []byte("other")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create = %v, want ErrAlreadyExists", err)
	}
	got, err := store.Load(ctx, "s1")
	if err != nil || string(got) != "one" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	if err := store.Save(ctx, "s1", []byte("two")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save(ctx, "missing", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Save missing = %v, want ErrNotFound", err)
	}
	got, _ = store.Load(ctx, "s1")
	if string(got) != "two" {
		t.Fatalf("after Save = %q, want two", got)
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
	input := []byte("one")
	if err := store.Create(ctx, "s1", input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'x'
	got, err := store.Load(ctx, "s1")
	if err != nil || string(got) != "one" {
		t.Fatalf("Create retained caller bytes: %q, %v", got, err)
	}
	got[0] = 'x'
	again, _ := store.Load(ctx, "s1")
	if string(again) != "one" {
		t.Fatalf("Load exposed backend bytes: %q", again)
	}
	saved := []byte("two")
	if err := store.Save(ctx, "s1", saved); err != nil {
		t.Fatal(err)
	}
	saved[0] = 'x'
	check, _ := store.Load(ctx, "s1")
	if !bytes.Equal(check, []byte("two")) {
		t.Fatalf("Save retained caller bytes: %q", check)
	}
}

func contractContextCanceled(t *testing.T, store Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Create(ctx, "s1", []byte("one")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create canceled = %v", err)
	}
	if _, err := store.Load(context.Background(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canceled Create left state: %v", err)
	}
}

func contractInvalidID(t *testing.T, store Store) {
	t.Helper()
	if _, err := store.Load(context.Background(), " "); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Load invalid ID = %v, want ErrInvalidSession", err)
	}
}

func contractConcurrentCreate(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Create(ctx, "s1", []byte("one"))
		}()
	}
	wg.Wait()
	close(errs)
	var succeeded, existed int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAlreadyExists):
			existed++
		default:
			t.Fatalf("Create = %v", err)
		}
	}
	if succeeded != 1 || existed != n-1 {
		t.Fatalf("want 1 success + %d conflicts, got %d + %d", n-1, succeeded, existed)
	}
}

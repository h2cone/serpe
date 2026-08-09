package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestMemoryStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if err := store.Create(ctx, New("s1", "/wd")); err != nil {
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
	if _, err := store.Load(ctx, " "); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Load with invalid ID = %v, want ErrInvalidSession", err)
	}
}

func TestMemoryStoreOwnership(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Mutating the input after Create must not affect stored state.
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

	// Mutating the output after Load must not affect stored state.
	got.CWD = "/mutated"
	got.Metadata = map[string]string{"title": "x"}
	again, _ := store.Load(ctx, "s1")
	if again.CWD != "/wd" || again.Metadata != nil {
		t.Fatal("Load exposed internal storage")
	}

	// Mutating the input after Save must not affect stored state.
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

func TestMemoryStoreContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewMemoryStore()

	if err := store.Create(ctx, New("s1", "/w")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create with canceled ctx = %v, want context.Canceled", err)
	}
	if _, err := store.Load(ctx, "s1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load with canceled ctx = %v, want context.Canceled", err)
	}
	if _, err := store.Load(context.Background(), "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canceled Create modified state: %v", err)
	}

	if err := store.Create(context.Background(), New("s2", "/w")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, New("s2", "/changed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save with canceled ctx = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, "s2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete with canceled ctx = %v, want context.Canceled", err)
	}
	got, err := store.Load(context.Background(), "s2")
	if err != nil || got.CWD != "/w" {
		t.Fatalf("canceled Save/Delete modified state: %v %v", got, err)
	}
}

func TestMemoryStoreConcurrent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	ids := []string{"a", "b", "c", "d"}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			switch i % 4 {
			case 0:
				_ = store.Create(ctx, New(id, "/wd"))
			case 1:
				if s, err := store.Load(ctx, id); err == nil && s != nil {
					_ = s.Clone() // touch the snapshot
				}
			case 2:
				_ = store.Delete(ctx, id)
			case 3:
				_ = store.Save(ctx, New(id, "/wd"))
			}
		}(i)
	}
	wg.Wait()
}

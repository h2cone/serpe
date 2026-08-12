package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
)

func TestNewFileStoreRequiresDir(t *testing.T) {
	if _, err := NewFileStore(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("NewFileStore(missing) succeeded")
	}
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(file); err == nil {
		t.Fatal("NewFileStore(file) succeeded")
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s := &Session{
		ID: "sess-1", CWD: "/work", CreatedAt: now, UpdatedAt: now,
		Metadata: map[string]string{"title": "t"},
		Messages: []models.Message{
			models.NewUserMessage(models.Text("hi")),
			func() models.Message {
				m := models.NewAssistantMessage(models.Text("hello"))
				m.ProviderState = &models.ProviderState{
					Provider: "openai",
					Data:     json.RawMessage(`{"k":1}`),
				}
				return m
			}(),
		},
	}
	mgr := mustManager(t, store)
	if _, err := mgr.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Get(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sessionEqual(s, got) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestFileStorePathSafety(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Portable ID policy rejects these before any filesystem write.
	bad := []string{
		"../escape",
		"a/b",
		`a\b`,
		"has space",
		"con",
		"CON",
		"CON.txt",
		"prn",
		"aux",
		"nul",
		"COM1",
		"lpt9.foo",
		".",
		"..",
		"",
	}
	for _, id := range bad {
		err := store.Create(ctx, id, []byte("opaque"))
		if err == nil {
			t.Fatalf("Create(%q) succeeded", id)
		}
		if !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("Create(%q) = %v, want ErrInvalidSession", id, err)
		}
	}
	// Confirm no stray files for separator-based IDs.
	entries, _ := os.ReadDir(store.root)
	for _, e := range entries {
		if e.Name() != "" && filepath.Ext(e.Name()) == ".json" {
			t.Fatalf("unexpected file %q after rejected Create", e.Name())
		}
	}
}

func TestFileStoreOrphanTmpIgnoredByLoad(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "s1", []byte("committed")); err != nil {
		t.Fatal(err)
	}
	// Plant an orphan tmp that would look like a partial write.
	orphan := filepath.Join(root, "s1.deadbeef.tmp")
	if err := os.WriteFile(orphan, []byte(`{"schema_version":1,"id":"s1","parent_id":"","cwd":"/hacked","created_at":"2026-08-07T12:00:00Z","updated_at":"2026-08-07T12:00:00Z","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "committed" {
		t.Fatalf("Load used orphan tmp: %q", got)
	}
	// Load must not delete the active/orphan tmp (cleanup is not on Load).
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("Load deleted orphan tmp: %v", err)
	}
}

func TestFileStoreStartupCleansTmp(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "s1.abc.tmp")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("startup did not clean *.tmp")
	}
}

func TestFileStoreSaveCleansOtherTmp(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "s1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "s1.stale.tmp")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "s1", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("successful Save did not clean same-id orphan tmp")
	}
}

func TestFileStoreLoadDoesNotDeleteActiveTmp(t *testing.T) {
	// Concurrent Load must not remove a temp that Create/Save still owns.
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "s1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "s1.writing.tmp")
	if err := os.WriteFile(active, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("Load deleted active tmp: %v", err)
	}
}

func TestFileStoreCorruptJSON(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "s1.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := mustManager(t, store)
	_, err = mgr.Get(ctx, "s1")
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("corrupt Load = %v, want ErrInvalidSession", err)
	}
}

func TestFileStoreConcurrentCreateSameID(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const n = 32
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- store.Create(ctx, "same-id", []byte{byte('0' + i%10)})
		}(i)
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
			t.Fatalf("unexpected Create error: %v", err)
		}
	}
	if okN != 1 || existsN != n-1 {
		t.Fatalf("want 1 success + %d ErrAlreadyExists, got ok=%d exists=%d", n-1, okN, existsN)
	}
	got, err := store.Load(ctx, "same-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded record: %q", got)
	}
}

func TestFileStoreConcurrentLoadDuringSave(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "s1", []byte("0")); err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	errCh := make(chan error, n+1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := store.Save(ctx, "s1", []byte{byte('0' + i%10)}); err != nil {
				errCh <- err
				return
			}
		}
	}()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.Load(ctx, "s1")
			if err != nil {
				errCh <- err
				return
			}
			if len(got) != 1 || got[0] < '0' || got[0] > '9' {
				errCh <- errors.New("torn or invalid load")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestFileStoreManagerIntegrationRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store1, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr1 := mustManager(t, store1)
	if _, err := mgr1.Create(ctx, New("sess-1", "/work")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr1.Append(ctx, "sess-1",
		models.NewUserMessage(models.Text("q")),
		models.NewAssistantMessage(models.Text("a")),
	); err != nil {
		t.Fatal(err)
	}

	// Simulate process restart: new FileStore + Manager on same root.
	store2, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr2 := mustManager(t, store2)
	got, err := mgr2.Get(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Content[0].Text.Text != "q" {
		t.Fatalf("restart Get: %+v", got.Messages)
	}
}

func TestPortableID(t *testing.T) {
	if !validID("good-id_1.x") {
		t.Fatal("expected good-id_1.x valid")
	}
	if validID("COM1") {
		t.Fatal("COM1 must be invalid")
	}
	if validID("has space") || validID("../x") || validID("") {
		t.Fatal("unsafe ids must be invalid")
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if validID(string(long)) {
		t.Fatal("129-char id must be invalid")
	}
}

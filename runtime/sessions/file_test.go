package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
)

type alwaysFailReader struct{ err error }

func (r alwaysFailReader) Read([]byte) (int, error) { return 0, r.err }

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

func TestFileStoreLockConflictRestartAndFailedConstructionRelease(t *testing.T) {
	root := privateTempDir(t)
	first, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := NewFileStore(root); err == nil {
		_ = second.Close()
		t.Fatal("second FileStore acquired an already-held maintenance lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("restart with persistent lock file: %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, storeFormatName), []byte("unknown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if failed, err := NewFileStore(root); err == nil {
		_ = failed.Close()
		t.Fatal("unknown marker was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, storeFormatName), []byte(storeFormatV2), 0o600); err != nil {
		t.Fatal(err)
	}
	afterFailure, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("failed constructor retained maintenance lock: %v", err)
	}
	_ = afterFailure.Close()
}

func TestFileStorePinsRootNamespaceAndRejectsReplacement(t *testing.T) {
	parent := privateTempDir(t)
	root := filepath.Join(parent, "store")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	restrictTestStoreDir(t, root)
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Create(context.Background(), "pinned", []byte("original")); err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(parent, "moved-store")
	if err := os.Rename(root, moved); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatalf("rename an open store root: %v", err)
		}
		got, loadErr := store.Load(context.Background(), "pinned")
		if loadErr != nil || string(got) != "original" {
			t.Fatalf("store after rejected root rename = %q, %v", got, loadErr)
		}
		t.Logf("Windows denied replacement of the open store root: %v", err)
		return
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	restrictTestStoreDir(t, root)
	if err := os.WriteFile(filepath.Join(root, storeFormatName), []byte(storeFormatV2), 0o600); err != nil {
		t.Fatal(err)
	}
	recordName := encodeRecordName("pinned")
	if err := os.WriteFile(filepath.Join(root, recordName), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load(context.Background(), "pinned"); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Load after root replacement = %v, want ErrStoreCorrupt", err)
	}
	got, err := store.rootHandle.ReadFile(recordName)
	if err != nil {
		t.Fatalf("read through pinned root handle: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("pinned root handle read %q, want original", got)
	}
}

func TestFileStoreRejectsMarkerAndRecordSymlinks(t *testing.T) {
	t.Run("marker", func(t *testing.T) {
		root := privateTempDir(t)
		target := filepath.Join(root, "marker-target")
		if err := os.WriteFile(target, []byte(storeFormatV2), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, storeFormatName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if store, err := NewFileStore(root); err == nil {
			_ = store.Close()
			t.Fatal("symlink format marker was accepted")
		}
	})

	t.Run("record", func(t *testing.T) {
		root := privateTempDir(t)
		store, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.Create(context.Background(), "linked", []byte("safe")); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "outside-record")
		if err := os.WriteFile(target, []byte("unsafe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(store.path("linked")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, store.path("linked")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := store.Load(context.Background(), "linked"); !errors.Is(err, ErrStoreCorrupt) {
			t.Fatalf("Load symlink = %v, want ErrStoreCorrupt", err)
		}
	})
}

func TestFileStoreRejectsCaseVariantJSONCandidate(t *testing.T) {
	root := privateTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "hidden.JSON"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := NewFileStore(root); !errors.Is(err, ErrMigrationRequired) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("NewFileStore hidden.JSON = %v, want ErrMigrationRequired", err)
	}
}

func TestFileStoreRejectsUnsafeOwnerAccessPolicy(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		root := privateTempDir(t)
		makeTestStorePathUnsafe(t, root, true)
		if store, err := NewFileStore(root); err == nil {
			_ = store.Close()
			t.Fatal("FileStore accepted an access-broad root")
		}
	})

	t.Run("record", func(t *testing.T) {
		root := privateTempDir(t)
		store, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.Create(context.Background(), "broad", []byte("secret")); err != nil {
			t.Fatal(err)
		}
		makeTestStorePathUnsafe(t, store.path("broad"), false)
		if _, err := store.Load(context.Background(), "broad"); !errors.Is(err, ErrStoreCorrupt) {
			t.Fatalf("Load broad-access record = %v, want ErrStoreCorrupt", err)
		}
	})
}

func TestFileStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	s := &Session{
		ID: "sess-1", CWD: testCWD("work"), CreatedAt: now, UpdatedAt: now,
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
	store, err := NewFileStore(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
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
	root := privateTempDir(t)
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
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
	root := privateTempDir(t)
	orphan := filepath.Join(root, encodeRecordName("s1")+"."+strings.Repeat("a", 32)+recordTempSuffix)
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("startup did not clean *.tmp")
	}
}

func TestFileStoreTempNameContract(t *testing.T) {
	id := strings.Repeat("a", 128)
	name := encodeRecordName(id) + "." + strings.Repeat("b", 32) + recordTempSuffix
	if len(name) != 250 || !isOwnedTempName(name) {
		t.Fatalf("canonical maximum temp name len=%d accepted=%t", len(name), isOwnedTempName(name))
	}
	for _, invalid := range []string{
		name + "x",
		encodeRecordName(id) + "." + strings.Repeat("B", 32) + recordTempSuffix,
		".serpe-record-" + strings.Repeat("b", 32) + recordTempSuffix,
		"r2_invalid.json." + strings.Repeat("b", 32) + recordTempSuffix,
	} {
		if isOwnedTempName(invalid) {
			t.Fatalf("accepted non-owned temp %q", invalid)
		}
	}
}

func TestFileStoreTempEntropyAndCollisionCeiling(t *testing.T) {
	t.Run("entropy failure", func(t *testing.T) {
		root := privateTempDir(t)
		store, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		previous := fileStoreRandom
		fileStoreRandom = alwaysFailReader{err: errors.New("entropy unavailable")}
		defer func() { fileStoreRandom = previous }()
		if err := store.Create(context.Background(), "entropy", []byte("data")); err == nil {
			t.Fatal("Create succeeded without entropy")
		}
		if _, err := os.Lstat(store.path("entropy")); !os.IsNotExist(err) {
			t.Fatalf("target appeared after entropy failure: %v", err)
		}
	})

	for _, test := range []struct {
		name       string
		collisions int
		wantOK     bool
	}{{"three collisions", 3, true}, {"four collisions", 4, false}} {
		t.Run(test.name, func(t *testing.T) {
			root := privateTempDir(t)
			store, err := NewFileStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			id := "collision"
			var random bytes.Buffer
			for attempt := 0; attempt < 4; attempt++ {
				block := bytes.Repeat([]byte{byte(attempt + 1)}, 16)
				random.Write(block)
				if attempt < test.collisions {
					name := encodeRecordName(id) + "." + fmt.Sprintf("%x", block) + recordTempSuffix
					if err := os.WriteFile(filepath.Join(root, name), []byte("occupied"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			previous := fileStoreRandom
			fileStoreRandom = &random
			defer func() { fileStoreRandom = previous }()
			err = store.Create(context.Background(), id, []byte("complete"))
			if test.wantOK && err != nil {
				t.Fatalf("Create after three collisions: %v", err)
			}
			if !test.wantOK && err == nil {
				t.Fatal("Create succeeded after four collisions")
			}
			if test.wantOK {
				data, loadErr := store.Load(context.Background(), id)
				if loadErr != nil || string(data) != "complete" {
					t.Fatalf("Load = %q, %v", data, loadErr)
				}
			} else if _, statErr := os.Lstat(store.path(id)); !os.IsNotExist(statErr) {
				t.Fatalf("target appeared after exhausted collisions: %v", statErr)
			}
		})
	}
}

func TestFileStoreSaveCleansOtherTmp(t *testing.T) {
	ctx := context.Background()
	root := privateTempDir(t)
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
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
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("Save removed an unowned temporary name")
	}
}

func TestFileStoreLoadDoesNotDeleteActiveTmp(t *testing.T) {
	// Concurrent Load must not remove a temp that Create/Save still owns.
	ctx := context.Background()
	root := privateTempDir(t)
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
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
	root := privateTempDir(t)
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.WriteFile(filepath.Join(root, "s1.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ListIDsPage(ctx, "", 100)
	if !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("corrupt filename scan = %v, want ErrStoreCorrupt", err)
	}
}

func TestFileStoreConcurrentCreateSameID(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
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
	store, err := NewFileStore(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
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
	root := privateTempDir(t)

	store1, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr1 := mustManager(t, store1)
	if _, err := mgr1.Create(ctx, New("sess-1", testCWD("work"))); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr1.Append(ctx, "sess-1",
		models.NewUserMessage(models.Text("q")),
		models.NewAssistantMessage(models.Text("a")),
	); err != nil {
		t.Fatal(err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate process restart: new FileStore + Manager on same root.
	store2, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr2 := mustManager(t, store2)
	t.Cleanup(func() { _ = mgr2.Close() })
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

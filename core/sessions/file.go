package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileStore is a disk-backed Store. Each record is a single opaque payload
// file under root: <root>/<id>.json. Writes use temp file + rename for
// atomic visibility. Cross-process coordination is out of scope.
//
// Session IDs use the package-wide portable alphabet (see Session docs);
// FileStore does not impose a second ID policy.
type FileStore struct {
	root string
	// createMu serializes Create so the exclusive-publish path cannot race
	// with another Create for the same ID inside this process.
	createMu sync.Mutex
}

// Compile-time check that FileStore implements Store.
var _ Store = (*FileStore)(nil)

// NewFileStore opens root as a session store directory. root must exist and
// be writable. On success it best-effort removes orphan *.tmp files left by
// previous crashes (safe only at construction, before concurrent writers).
func NewFileStore(root string) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: root directory is required", ErrInvalidSession)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrInvalidSession, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: root: %v", ErrInvalidSession, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: root is not a directory", ErrInvalidSession)
	}
	// Probe writability without leaving permanent debris.
	probe := filepath.Join(abs, ".serpe-store-probe-"+randomHex(4))
	if err := os.WriteFile(probe, []byte{0}, 0o600); err != nil {
		return nil, fmt.Errorf("%w: root not writable: %v", ErrInvalidSession, err)
	}
	_ = os.Remove(probe)

	s := &FileStore{root: abs}
	s.cleanupAllTmp()
	return s, nil
}

// Create inserts a record. See Store.Create.
// Publish is exclusive (hard-link or O_EXCL claim): concurrent Creates for
// the same ID return ErrAlreadyExists rather than clobbering via rename.
func (s *FileStore) Create(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	return s.atomicWrite(id, data, true)
}

// Load returns an independent byte record. See Store.Load.
// Load never deletes .tmp files: concurrent Create/Save may own them.
func (s *FileStore) Load(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validID(id) {
		return nil, invalidf("invalid ID %q", id)
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// Save replaces the stored record. See Store.Save.
func (s *FileStore) Save(ctx context.Context, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	if _, err := os.Stat(s.path(id)); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return s.atomicWrite(id, data, false)
}

// Delete removes a record. See Store.Delete.
func (s *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	err := os.Remove(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// List returns independent records. See Store.List.
// Orphan .tmp files are ignored. Order is undefined.
func (s *FileStore) List(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validID(id) {
			continue
		}
		data, err := s.Load(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, Record{ID: id, Data: data})
	}
	return out, nil
}

func (s *FileStore) path(id string) string {
	return filepath.Join(s.root, id+".json")
}

// atomicWrite writes a same-directory temp file, fsyncs, then publishes to the
// destination. When createOnly is true, publish is exclusive (no clobber).
// When false, rename replaces an existing destination (Save).
func (s *FileStore) atomicWrite(id string, data []byte, createOnly bool) error {
	// Own the byte slice before performing multi-step I/O.
	data = append([]byte(nil), data...)
	random := randomHex(8)
	tmpName := id + "." + random + ".tmp"
	tmpPath := filepath.Join(s.root, tmpName)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// Ensure cleanup of this temp on any failure path before publish.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	dest := s.path(id)
	if createOnly {
		if err := publishCreate(tmpPath, dest); err != nil {
			return err
		}
	} else {
		// On Windows, os.Rename over an existing path can fail with access
		// denied / sharing violation while a concurrent Load holds the file
		// open. renameReplace retries only those transient errors.
		if err := renameReplace(tmpPath, dest); err != nil {
			return err
		}
	}
	committed = true
	// Best-effort: remove other orphan temps for this id (not this random;
	// that file was renamed or linked away).
	s.cleanupIDTmp(id)
	// Best-effort directory fsync is platform-specific; omitted on Windows.
	return nil
}

// publishCreate places tmp at dest only if dest does not already exist.
// Prefer hard-link (atomic create-if-absent). Fall back to O_EXCL claim +
// rename when the filesystem rejects hard links.
func publishCreate(tmp, dest string) error {
	if err := os.Link(tmp, dest); err == nil {
		_ = os.Remove(tmp)
		return nil
	} else if os.IsExist(err) || errors.Is(err, os.ErrExist) {
		return ErrAlreadyExists
	}
	// Link unsupported or failed for a non-existence reason. Claim dest
	// exclusively, then rename the temp over the empty claim.
	if _, err := os.Stat(dest); err == nil {
		return ErrAlreadyExists
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	claim, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) || errors.Is(err, os.ErrExist) {
			return ErrAlreadyExists
		}
		return err
	}
	if err := claim.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if err := renameReplace(tmp, dest); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}

// renameReplace renames oldpath onto newpath, retrying only transient Windows
// sharing / access-denied errors from concurrent readers. Permanent failures
// return immediately. On non-Windows platforms the first error is final.
func renameReplace(oldpath, newpath string) error {
	const attempts = 20
	var err error
	for i := 0; i < attempts; i++ {
		err = os.Rename(oldpath, newpath)
		if err == nil {
			return nil
		}
		if !isTransientRenameError(err) || i+1 >= attempts {
			return err
		}
		time.Sleep(time.Duration(5*(i+1)) * time.Millisecond)
	}
	return err
}

func (s *FileStore) cleanupAllTmp() {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(s.root, name))
		}
	}
}

func (s *FileStore) cleanupIDTmp(id string) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	// Match <id>.<random>.tmp
	prefix := id + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		// Require exactly one extra segment between id and .tmp: id.random.tmp
		rest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tmp")
		if rest == "" || strings.Contains(rest, ".") {
			continue
		}
		_ = os.Remove(filepath.Join(s.root, name))
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to pid-ish uniqueness so callers still
		// make progress rather than panicking mid-write.
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(b)
}

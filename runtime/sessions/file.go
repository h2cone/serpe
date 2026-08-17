package sessions

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// FileStore is a v2 disk store rooted at a pinned absolute directory. It
// holds an exclusive maintenance lock until Close and uses lowercase base32
// record filenames so portable IDs remain one-to-one on case-insensitive
// filesystems.
type FileStore struct {
	storeRoot

	life    drainClose
	writeMu sync.RWMutex
}

var _ Store = (*FileStore)(nil)

// NewFileStore opens an existing, absolute, private directory. A missing
// marker is initialized only for an empty store; any JSON candidate without a
// marker requires explicit offline migration.
func NewFileStore(root string) (*FileStore, error) {
	pinned, err := openStoreRoot(root, storeRootOptions{})
	if err != nil {
		return nil, err
	}
	store := &FileStore{storeRoot: pinned}
	store.life.init()
	if err := store.initializeLayout(); err != nil {
		_ = pinned.close()
		return nil, err
	}
	store.cleanupOwnedTemps()
	return store, nil
}

func (s *FileStore) begin(ctx context.Context) error {
	if err := s.life.enter(); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		s.life.leave()
		return err
	}
	if err := s.check(); err != nil {
		s.life.leave()
		return err
	}
	return nil
}

func (s *FileStore) end() { s.life.leave() }

// Close releases the cross-process lock after entered operations drain.
func (s *FileStore) Close() error {
	if s == nil {
		return nil
	}
	return s.life.close(s.storeRoot.close)
}

func (s *FileStore) Create(ctx context.Context, id string, data []byte) error {
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	if err := s.begin(ctx); err != nil {
		return err
	}
	defer s.end()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.atomicWrite(ctx, id, data, true)
}

func (s *FileStore) Load(ctx context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, invalidf("invalid ID %q", id)
	}
	if err := s.begin(ctx); err != nil {
		return nil, err
	}
	defer s.end()
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.loadRecord(ctx, id)
}

func (s *FileStore) loadRecord(ctx context.Context, id string) ([]byte, error) {
	name := encodeRecordName(id)
	listed, err := s.rootHandle.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: record is not a regular file", ErrStoreCorrupt)
	}
	file, err := openRootRegular(s.rootHandle, name, os.O_RDONLY)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: record could not be opened safely", ErrStoreCorrupt)
	}
	info, statErr := file.Stat()
	if statErr != nil || validateStoreRegular(file, info) != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
	}
	data, readErr := readAllContext(ctx, file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func readAllContext(ctx context.Context, reader io.Reader) ([]byte, error) {
	buffer := make([]byte, 0, 32<<10)
	chunk := make([]byte, 32<<10)
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		n, err := reader.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buffer, nil
			}
			return nil, err
		}
	}
}

func (s *FileStore) Save(ctx context.Context, id string, data []byte) error {
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	if err := s.begin(ctx); err != nil {
		return err
	}
	defer s.end()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.atomicWrite(ctx, id, data, false)
}

func (s *FileStore) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return invalidf("invalid ID %q", id)
	}
	if err := s.begin(ctx); err != nil {
		return err
	}
	defer s.end()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	name := encodeRecordName(id)
	listed, err := s.rootHandle.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if !listed.Mode().IsRegular() || listed.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
	}
	file, err := openRootRegular(s.rootHandle, name, os.O_RDONLY)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := s.rootHandle.Remove(name); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if err := s.syncDir(); err != nil {
		return fmt.Errorf("%w: delete directory sync: %v", ErrCommitUncertain, err)
	}
	return nil
}

// List is potentially expensive and intended for maintenance/non-HTTP use.
func (s *FileStore) List(ctx context.Context) ([]Record, error) {
	if err := s.begin(ctx); err != nil {
		return nil, err
	}
	defer s.end()
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	entries, err := readRootDirEntries(s.rootHandle, ".")
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0)
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if !isJSONCandidate(entry.Name()) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("%w: unsafe record entry", ErrStoreCorrupt)
		}
		id, err := decodeRecordName(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: non-canonical record filename", ErrStoreCorrupt)
		}
		data, err := s.loadRecord(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Record{ID: id, Data: data})
	}
	return out, nil
}

// ListIDsPage reopens a fresh directory handle for every traversal so callers
// never share a directory offset.
func (s *FileStore) ListIDsPage(ctx context.Context, afterID string, limit int) ([]string, string, error) {
	if err := validatePage(afterID, limit); err != nil {
		return nil, "", err
	}
	if err := s.begin(ctx); err != nil {
		return nil, "", err
	}
	defer s.end()
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	directory, err := openRootDirectory(s.rootHandle, ".")
	if err != nil {
		return nil, "", err
	}
	identity, identityErr := storeFileIdentity(directory)
	if identityErr != nil || identity != s.rootIdentity {
		_ = directory.Close()
		return nil, "", fmt.Errorf("%w: root identity changed", ErrStoreCorrupt)
	}
	defer directory.Close()
	selected := &maxIDHeap{}
	heap.Init(selected)
	for {
		if err := contextError(ctx); err != nil {
			return nil, "", err
		}
		entries, err := directory.ReadDir(256)
		for _, entry := range entries {
			name := entry.Name()
			if !isJSONCandidate(name) {
				continue
			}
			id, decodeErr := decodeRecordName(name)
			if decodeErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil, "", fmt.Errorf("%w: non-canonical record filename", ErrStoreCorrupt)
			}
			selectPageID(selected, id, afterID, limit+1)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", err
		}
	}
	ids, next := completeIDPage(selected, limit)
	return ids, next, nil
}

func (s *FileStore) atomicWrite(ctx context.Context, id string, data []byte, createOnly bool) error {
	dest := encodeRecordName(id)
	if createOnly {
		return s.publishExclusive(ctx, dest, data)
	}
	return s.replaceExclusive(ctx, dest, data)
}

func (s *FileStore) path(id string) string { return filepath.Join(s.root, encodeRecordName(id)) }

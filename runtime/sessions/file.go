package sessions

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	storeFormatName    = ".serpe-store-format"
	storeLockName      = ".serpe-store.lock"
	storeFormatV2      = "serpe.sessions.filestore.v2\n"
	formatTempPrefix   = storeFormatName + "."
	recordTempSuffix   = ".tmp"
	recordNamePrefixV2 = "r2_"
	recordNameSuffixV2 = ".json"
)

var recordBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)
var fileStoreRandom = io.Reader(rand.Reader)

// FileStore is a v2 disk store rooted at a pinned absolute directory. It
// holds an exclusive maintenance lock until Close and uses lowercase base32
// record filenames so portable IDs remain one-to-one on case-insensitive
// filesystems.
type FileStore struct {
	root         string
	rootFile     *os.File
	rootHandle   *os.Root
	rootIdentity string
	lockFile     *os.File
	unlock       func() error

	stateMu   sync.Mutex
	stateCond *sync.Cond
	active    int
	closing   bool
	closeDone chan struct{}
	closeErr  error

	writeMu sync.RWMutex
}

var _ Store = (*FileStore)(nil)

// NewFileStore opens an existing, absolute, private directory. A missing
// marker is initialized only for an empty store; any JSON candidate without a
// marker requires explicit offline migration.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: FileStore root must be an absolute path", ErrInvalidSession)
	}
	clean := filepath.Clean(root)
	if err := validateStorePlatform(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	listed, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: root: %v", ErrInvalidSession, err)
	}
	if listed.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: root must not be a symlink", ErrInvalidSession)
	}
	rootFile, err := openStoreDirectory(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: open root: %v", ErrInvalidSession, err)
	}
	failed := true
	defer func() {
		if failed {
			_ = rootFile.Close()
		}
	}()
	info, err := rootFile.Stat()
	if err != nil || validateStoreRoot(rootFile, info) != nil {
		return nil, fmt.Errorf("%w: root ownership or permissions are unsafe", ErrInvalidSession)
	}
	identity, err := storeFileIdentity(rootFile)
	if err != nil {
		return nil, fmt.Errorf("%w: pin root identity: %v", ErrInvalidSession, err)
	}
	rootHandle, err := os.OpenRoot(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: open rooted store namespace: %v", ErrInvalidSession, err)
	}
	defer func() {
		if failed {
			_ = rootHandle.Close()
		}
	}()
	rootView, err := rootHandle.Open(".")
	if err != nil {
		return nil, fmt.Errorf("%w: inspect rooted store namespace: %v", ErrInvalidSession, err)
	}
	rootViewIdentity, identityErr := storeFileIdentity(rootView)
	rootViewInfo, statErr := rootView.Stat()
	validationErr := validateStoreRoot(rootView, rootViewInfo)
	closeErr := rootView.Close()
	if identityErr != nil || statErr != nil || validationErr != nil || closeErr != nil || rootViewIdentity != identity {
		return nil, fmt.Errorf("%w: rooted store namespace identity mismatch", ErrInvalidSession)
	}

	lockFile, err := openStoreLock(rootHandle)
	if err != nil {
		return nil, err
	}
	defer func() {
		if failed {
			_ = lockFile.Close()
		}
	}()
	unlock, err := lockStoreFile(lockFile)
	if err != nil {
		return nil, fmt.Errorf("%w: store is already open or cannot be locked", ErrInvalidSession)
	}
	defer func() {
		if failed {
			_ = unlock()
		}
	}()

	store := &FileStore{
		root:         clean,
		rootFile:     rootFile,
		rootHandle:   rootHandle,
		rootIdentity: identity,
		lockFile:     lockFile,
		unlock:       unlock,
		closeDone:    make(chan struct{}),
	}
	store.stateCond = sync.NewCond(&store.stateMu)
	if err := store.initializeLayout(); err != nil {
		return nil, err
	}
	store.cleanupOwnedTemps()
	failed = false
	return store, nil
}

func openStoreLock(root *os.Root) (*os.File, error) {
	if root == nil {
		return nil, fmt.Errorf("%w: rooted store namespace is unavailable", ErrInvalidSession)
	}
	for attempt := 0; attempt < 3; attempt++ {
		listed, err := root.Lstat(storeLockName)
		create := os.IsNotExist(err)
		if err != nil && !create {
			return nil, fmt.Errorf("%w: inspect store lock: %v", ErrInvalidSession, err)
		}
		if err == nil && (listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular()) {
			return nil, fmt.Errorf("%w: store lock is not a regular file", ErrInvalidSession)
		}
		var file *os.File
		var openErr error
		if create {
			file, openErr = root.OpenFile(storeLockName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		} else {
			file, openErr = openRootRegular(root, storeLockName, os.O_RDWR)
		}
		if openErr != nil {
			if attempt < 2 && ((create && os.IsExist(openErr)) || (!create && os.IsNotExist(openErr))) {
				continue
			}
			return nil, fmt.Errorf("%w: open store lock: %v", ErrInvalidSession, openErr)
		}
		info, statErr := file.Stat()
		if statErr != nil || validateStoreRegular(file, info) != nil {
			_ = file.Close()
			return nil, fmt.Errorf("%w: store lock is unsafe", ErrInvalidSession)
		}
		return file, nil
	}
	return nil, fmt.Errorf("%w: store lock changed repeatedly", ErrInvalidSession)
}

func openRootRegular(root *os.Root, name string, flags int) (*os.File, error) {
	if root == nil {
		return nil, fmt.Errorf("rooted store namespace is unavailable")
	}
	listed, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
		return nil, fmt.Errorf("entry is not a regular non-link file")
	}
	file, err := root.OpenFile(name, flags, 0)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	validationErr := validateStoreRegular(file, info)
	if statErr != nil || validationErr != nil || !os.SameFile(listed, info) {
		_ = file.Close()
		return nil, fmt.Errorf("entry changed while it was opened")
	}
	return file, nil
}

func openRootDirectory(root *os.Root, name string) (*os.File, error) {
	if root == nil {
		return nil, fmt.Errorf("rooted store namespace is unavailable")
	}
	listed, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if listed.Mode()&os.ModeSymlink != 0 || !listed.IsDir() {
		return nil, fmt.Errorf("entry is not a non-link directory")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	validationErr := validateStoreRoot(file, info)
	if statErr != nil || validationErr != nil || !os.SameFile(listed, info) {
		_ = file.Close()
		return nil, fmt.Errorf("directory changed while it was opened")
	}
	return file, nil
}

func (s *FileStore) initializeLayout() error {
	markerInfo, err := s.rootHandle.Lstat(storeFormatName)
	if err == nil {
		if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
			return fmt.Errorf("%w: invalid format marker", ErrStoreCorrupt)
		}
		marker, err := openRootRegular(s.rootHandle, storeFormatName, os.O_RDONLY)
		if err != nil {
			return fmt.Errorf("%w: open format marker", ErrStoreCorrupt)
		}
		info, statErr := marker.Stat()
		if statErr != nil || validateStoreRegular(marker, info) != nil {
			_ = marker.Close()
			return fmt.Errorf("%w: unsafe format marker", ErrStoreCorrupt)
		}
		data, readErr := io.ReadAll(io.LimitReader(marker, int64(len(storeFormatV2))+1))
		closeErr := marker.Close()
		if readErr != nil || closeErr != nil || string(data) != storeFormatV2 {
			return fmt.Errorf("%w: unknown format marker", ErrStoreCorrupt)
		}
		return s.scanCanonicalNames()
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect format marker", ErrStoreCorrupt)
	}
	entries, err := readRootDirEntries(s.rootHandle, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if isJSONCandidate(entry.Name()) {
			return fmt.Errorf("%w: legacy or unmarked JSON records found", ErrMigrationRequired)
		}
	}
	if err := s.publishMarker(); err != nil {
		return err
	}
	return nil
}

func (s *FileStore) scanCanonicalNames() error {
	entries, err := readRootDirEntries(s.rootHandle, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !isJSONCandidate(name) {
			continue
		}
		if entry.IsDir() {
			return fmt.Errorf("%w: JSON candidate is a directory", ErrStoreCorrupt)
		}
		if _, err := decodeRecordName(name); err != nil {
			return fmt.Errorf("%w: non-canonical record filename", ErrStoreCorrupt)
		}
		listed, err := s.rootHandle.Lstat(name)
		if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
			return fmt.Errorf("%w: unsafe record entry", ErrStoreCorrupt)
		}
		file, err := openRootRegular(s.rootHandle, name, os.O_RDONLY)
		if err != nil {
			return fmt.Errorf("%w: open record entry", ErrStoreCorrupt)
		}
		info, statErr := file.Stat()
		validationErr := validateStoreRegular(file, info)
		closeErr := file.Close()
		if statErr != nil || validationErr != nil || closeErr != nil {
			return fmt.Errorf("%w: unsafe record entry", ErrStoreCorrupt)
		}
	}
	return nil
}

func (s *FileStore) publishMarker() error {
	tempName, file, err := s.createTemp(formatTempPrefix)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.rootHandle.Remove(tempName)
		}
	}()
	if _, err := file.Write([]byte(storeFormatV2)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := s.rootHandle.Link(tempName, storeFormatName); err != nil {
		if os.IsExist(err) || errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: format marker appeared unexpectedly", ErrStoreCorrupt)
		}
		return fmt.Errorf("publish format marker: %w", err)
	}
	committed = true
	if err := s.rootHandle.Remove(tempName); err != nil {
		return fmt.Errorf("%w: remove format temp: %v", ErrCommitUncertain, err)
	}
	if err := syncStoreDirectory(s.rootFile); err != nil {
		return fmt.Errorf("%w: sync format marker: %v", ErrCommitUncertain, err)
	}
	return nil
}

func readRootDirEntries(root *os.Root, name string) (entries []os.DirEntry, returnErr error) {
	directory, err := openRootDirectory(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	return directory.ReadDir(-1)
}

func (s *FileStore) begin(ctx context.Context) error {
	s.stateMu.Lock()
	if s.closing {
		s.stateMu.Unlock()
		return ErrClosed
	}
	s.active++
	s.stateMu.Unlock()
	if err := contextError(ctx); err != nil {
		s.end()
		return err
	}
	if err := s.checkRoot(); err != nil {
		s.end()
		return err
	}
	return nil
}

func (s *FileStore) end() {
	s.stateMu.Lock()
	s.active--
	if s.active == 0 {
		s.stateCond.Broadcast()
	}
	s.stateMu.Unlock()
}

func (s *FileStore) checkRoot() error {
	listed, err := os.Lstat(s.root)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.IsDir() {
		return fmt.Errorf("%w: root directory entry changed", ErrStoreCorrupt)
	}
	current, err := openStoreDirectory(s.root)
	if err != nil {
		return fmt.Errorf("%w: root directory entry changed", ErrStoreCorrupt)
	}
	info, statErr := current.Stat()
	identity, identityErr := storeFileIdentity(current)
	validationErr := validateStoreRoot(current, info)
	closeErr := current.Close()
	if statErr != nil || identityErr != nil || validationErr != nil || closeErr != nil || identity != s.rootIdentity {
		return fmt.Errorf("%w: root identity changed", ErrStoreCorrupt)
	}
	return nil
}

// Close releases the cross-process lock after entered operations drain.
func (s *FileStore) Close() error {
	if s == nil {
		return nil
	}
	s.stateMu.Lock()
	if s.closing {
		done := s.closeDone
		s.stateMu.Unlock()
		<-done
		s.stateMu.Lock()
		err := s.closeErr
		s.stateMu.Unlock()
		return err
	}
	s.closing = true
	for s.active != 0 {
		s.stateCond.Wait()
	}
	s.stateMu.Unlock()

	var closeErrors []error
	if s.unlock != nil {
		if err := s.unlock(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if s.lockFile != nil {
		if err := s.lockFile.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if s.rootHandle != nil {
		if err := s.rootHandle.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if s.rootFile != nil {
		if err := s.rootFile.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	err := errors.Join(closeErrors...)
	s.stateMu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.stateMu.Unlock()
	return err
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
	if err := syncStoreDirectory(s.rootFile); err != nil {
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
	data = append([]byte(nil), data...)
	tempName, file, err := s.createTemp(encodeRecordName(id) + ".")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.rootHandle.Remove(tempName)
		}
	}()
	for len(data) > 0 {
		if err := contextError(ctx); err != nil {
			_ = file.Close()
			return err
		}
		chunk := data
		if len(chunk) > 64<<10 {
			chunk = chunk[:64<<10]
		}
		n, err := file.Write(chunk)
		if err != nil {
			_ = file.Close()
			return err
		}
		if n == 0 {
			_ = file.Close()
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	destination := encodeRecordName(id)
	if createOnly {
		if err := s.rootHandle.Link(tempName, destination); err != nil {
			if os.IsExist(err) || errors.Is(err, os.ErrExist) {
				return ErrAlreadyExists
			}
			return fmt.Errorf("exclusive record publish: %w", err)
		}
		committed = true
		if err := s.rootHandle.Remove(tempName); err != nil {
			return fmt.Errorf("%w: remove linked temp: %v", ErrCommitUncertain, err)
		}
	} else {
		listed, err := s.rootHandle.Lstat(destination)
		if err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}
			return err
		}
		if !listed.Mode().IsRegular() || listed.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
		}
		current, err := openRootRegular(s.rootHandle, destination, os.O_RDONLY)
		if err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}
			return fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
		}
		if err := current.Close(); err != nil {
			return err
		}
		if err := renameReplace(s.rootHandle, tempName, destination); err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}
			return err
		}
		committed = true
	}
	if err := syncStoreDirectory(s.rootFile); err != nil {
		return fmt.Errorf("%w: record directory sync: %v", ErrCommitUncertain, err)
	}
	return nil
}

func (s *FileStore) createTemp(prefix string) (string, *os.File, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(fileStoreRandom, random[:]); err != nil {
			return "", nil, fmt.Errorf("generate store temporary name: %w", err)
		}
		name := prefix + hex.EncodeToString(random[:]) + recordTempSuffix
		file, err := s.rootHandle.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, file, nil
		}
		if !os.IsExist(err) || attempt == 3 {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("temporary name attempts exhausted")
}

func (s *FileStore) path(id string) string { return filepath.Join(s.root, encodeRecordName(id)) }

func encodeRecordName(id string) string {
	encoded := strings.ToLower(recordBase32.EncodeToString([]byte(id)))
	return recordNamePrefixV2 + encoded + recordNameSuffixV2
}

func decodeRecordName(name string) (string, error) {
	if !strings.HasPrefix(name, recordNamePrefixV2) || !strings.HasSuffix(name, recordNameSuffixV2) {
		return "", fmt.Errorf("invalid record name")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, recordNamePrefixV2), recordNameSuffixV2)
	if body == "" || body != strings.ToLower(body) || strings.Contains(body, "=") {
		return "", fmt.Errorf("invalid record base32")
	}
	decoded, err := recordBase32.DecodeString(strings.ToUpper(body))
	if err != nil {
		return "", err
	}
	id := string(decoded)
	if !validID(id) || encodeRecordName(id) != name {
		return "", fmt.Errorf("non-canonical record name")
	}
	return id, nil
}

func isJSONCandidate(name string) bool {
	return len(name) >= len(recordNameSuffixV2) && strings.EqualFold(name[len(name)-len(recordNameSuffixV2):], recordNameSuffixV2)
}

func (s *FileStore) cleanupOwnedTemps() {
	entries, err := readRootDirEntries(s.rootHandle, ".")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !isOwnedTempName(entry.Name()) {
			continue
		}
		listed, err := s.rootHandle.Lstat(entry.Name())
		if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
			continue
		}
		file, err := openRootRegular(s.rootHandle, entry.Name(), os.O_RDONLY)
		if err != nil {
			continue
		}
		info, statErr := file.Stat()
		validationErr := validateStoreRegular(file, info)
		closeErr := file.Close()
		if statErr != nil || validationErr != nil || closeErr != nil {
			continue
		}
		_ = s.rootHandle.Remove(entry.Name())
	}
}

func isOwnedTempName(name string) bool {
	if len(name) > 250 || !strings.HasSuffix(name, recordTempSuffix) {
		return false
	}
	withoutSuffix := strings.TrimSuffix(name, recordTempSuffix)
	dot := strings.LastIndexByte(withoutSuffix, '.')
	if dot < 0 {
		return false
	}
	base, hexadecimal := withoutSuffix[:dot], withoutSuffix[dot+1:]
	if len(hexadecimal) != 32 || hexadecimal != strings.ToLower(hexadecimal) {
		return false
	}
	if _, err := hex.DecodeString(hexadecimal); err != nil {
		return false
	}
	if base == storeFormatName {
		return true
	}
	_, err := decodeRecordName(base)
	return err == nil
}

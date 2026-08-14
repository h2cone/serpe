package sessions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxStoreIdentityBytes = 256

var fileStoreRandom = io.Reader(rand.Reader)

// storeRoot is the pinned exclusive directory used by live FileStore and
// offline maintenance. It owns the root pin, identity, lock, and exclusive
// publish/replace writes.
type storeRoot struct {
	root         string
	rootFile     *os.File
	rootHandle   *os.Root
	rootIdentity string
	lockFile     *os.File
	unlock       func() error
	removeLock   bool
}

type storeRootOptions struct {
	removeNewLock bool
}

func openStoreRoot(path string, opts storeRootOptions) (storeRoot, error) {
	var root storeRoot
	if path == "" || !filepath.IsAbs(path) {
		return storeRoot{}, fmt.Errorf("%w: store root must be an absolute path", ErrInvalidSession)
	}
	clean := filepath.Clean(path)
	if err := validateStorePlatform(); err != nil {
		return storeRoot{}, fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	listed, err := os.Lstat(clean)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.IsDir() {
		return storeRoot{}, fmt.Errorf("%w: root is not a non-symlink directory", ErrInvalidSession)
	}
	rootFile, err := openStoreDirectory(clean)
	if err != nil {
		return storeRoot{}, fmt.Errorf("%w: open root: %v", ErrInvalidSession, err)
	}
	failed := true
	defer func() {
		if failed {
			_ = rootFile.Close()
		}
	}()
	info, err := rootFile.Stat()
	if err != nil || validateStoreRoot(rootFile, info) != nil {
		return storeRoot{}, fmt.Errorf("%w: root ownership or permissions are unsafe", ErrInvalidSession)
	}
	identity, err := storeFileIdentity(rootFile)
	if err != nil {
		return storeRoot{}, fmt.Errorf("%w: pin root identity: %v", ErrInvalidSession, err)
	}
	if identity == "" || len(identity) > maxStoreIdentityBytes {
		return storeRoot{}, fmt.Errorf("%w: root identity is invalid", ErrStoreCorrupt)
	}
	rootHandle, err := os.OpenRoot(clean)
	if err != nil {
		return storeRoot{}, fmt.Errorf("%w: open rooted store namespace: %v", ErrInvalidSession, err)
	}
	defer func() {
		if failed {
			_ = rootHandle.Close()
		}
	}()
	rootView, err := openRootDirectory(rootHandle, ".")
	if err != nil {
		return storeRoot{}, fmt.Errorf("%w: inspect rooted store namespace: %v", ErrInvalidSession, err)
	}
	rootViewIdentity, identityErr := storeFileIdentity(rootView)
	closeErr := rootView.Close()
	if identityErr != nil || closeErr != nil || rootViewIdentity != identity {
		return storeRoot{}, fmt.Errorf("%w: rooted store namespace identity mismatch", ErrInvalidSession)
	}

	_, beforeErr := rootHandle.Lstat(storeLockName)
	lockWasMissing := os.IsNotExist(beforeErr)
	lockFile, err := openStoreLock(rootHandle)
	if err != nil {
		return storeRoot{}, err
	}
	defer func() {
		if failed {
			_ = lockFile.Close()
		}
	}()
	unlock, err := lockStoreFile(lockFile)
	if err != nil {
		return storeRoot{}, fmt.Errorf("%w: store is already open or cannot be locked", ErrInvalidSession)
	}
	defer func() {
		if failed {
			_ = unlock()
		}
	}()

	failed = false
	root = storeRoot{
		root:         clean,
		rootFile:     rootFile,
		rootHandle:   rootHandle,
		rootIdentity: identity,
		lockFile:     lockFile,
		unlock:       unlock,
		removeLock:   opts.removeNewLock && lockWasMissing,
	}
	return root, nil
}

func (r *storeRoot) close() error {
	if r == nil {
		return nil
	}
	var errs []error
	// A dry-run removes a lock file that it had to create. Unlink it while
	// the old file is still locked: unlocking first would let another opener
	// acquire this directory entry just before we remove it.
	if r.removeLock && r.rootHandle != nil {
		errs = append(errs, r.rootHandle.Remove(storeLockName))
		if r.rootFile != nil {
			errs = append(errs, syncStoreDirectory(r.rootFile))
		}
		r.removeLock = false
	}
	if r.unlock != nil {
		errs = append(errs, r.unlock())
		r.unlock = nil
	}
	if r.lockFile != nil {
		errs = append(errs, r.lockFile.Close())
		r.lockFile = nil
	}
	if r.rootHandle != nil {
		errs = append(errs, r.rootHandle.Close())
		r.rootHandle = nil
	}
	if r.rootFile != nil {
		errs = append(errs, r.rootFile.Close())
		r.rootFile = nil
	}
	return errors.Join(errs...)
}

func (r *storeRoot) check() error {
	listed, err := os.Lstat(r.root)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.IsDir() {
		return fmt.Errorf("%w: root directory entry changed", ErrStoreCorrupt)
	}
	current, err := openStoreDirectory(r.root)
	if err != nil {
		return fmt.Errorf("%w: root directory entry changed", ErrStoreCorrupt)
	}
	info, statErr := current.Stat()
	identity, identityErr := storeFileIdentity(current)
	validationErr := validateStoreRoot(current, info)
	closeErr := current.Close()
	if statErr != nil || identityErr != nil || validationErr != nil || closeErr != nil || identity != r.rootIdentity {
		return fmt.Errorf("%w: root identity changed", ErrStoreCorrupt)
	}
	return nil
}

func (r *storeRoot) syncDir() error {
	return syncStoreDirectory(r.rootFile)
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

func readRootDirEntries(root *os.Root, name string) (entries []os.DirEntry, returnErr error) {
	directory, err := openRootDirectory(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	return directory.ReadDir(-1)
}

func (r *storeRoot) createTemp(prefix string) (string, *os.File, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(fileStoreRandom, random[:]); err != nil {
			return "", nil, fmt.Errorf("generate store temporary name: %w", err)
		}
		name := prefix + hex.EncodeToString(random[:]) + recordTempSuffix
		file, err := r.rootHandle.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, file, nil
		}
		if !os.IsExist(err) || attempt == 3 {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("temporary name attempts exhausted")
}

func (r *storeRoot) publishExclusive(ctx context.Context, destName string, data []byte) error {
	return r.publishNamed(ctx, destName, data, ErrAlreadyExists)
}

func (r *storeRoot) publishNamed(ctx context.Context, destName string, data []byte, existErr error) error {
	data = append([]byte(nil), data...)
	tempName, file, err := r.createTemp(destName + ".")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = r.rootHandle.Remove(tempName)
		}
	}()
	if err := writeFileContext(ctx, file, data); err != nil {
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
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := r.rootHandle.Link(tempName, destName); err != nil {
		if os.IsExist(err) || errors.Is(err, os.ErrExist) {
			if existErr != nil {
				return existErr
			}
			return err
		}
		return fmt.Errorf("exclusive record publish: %w", err)
	}
	committed = true
	if err := r.rootHandle.Remove(tempName); err != nil {
		return fmt.Errorf("%w: remove linked temp: %v", ErrCommitUncertain, err)
	}
	if err := r.syncDir(); err != nil {
		return fmt.Errorf("%w: record directory sync: %v", ErrCommitUncertain, err)
	}
	return nil
}

func (r *storeRoot) replaceExclusive(ctx context.Context, destName string, data []byte) error {
	data = append([]byte(nil), data...)
	tempName, file, err := r.createTemp(destName + ".")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = r.rootHandle.Remove(tempName)
		}
	}()
	if err := writeFileContext(ctx, file, data); err != nil {
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
	if err := contextError(ctx); err != nil {
		return err
	}
	listed, err := r.rootHandle.Lstat(destName)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if !listed.Mode().IsRegular() || listed.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
	}
	current, err := openRootRegular(r.rootHandle, destName, os.O_RDONLY)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("%w: unsafe record", ErrStoreCorrupt)
	}
	if err := current.Close(); err != nil {
		return err
	}
	if err := renameReplace(r.rootHandle, tempName, destName); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	committed = true
	if err := r.syncDir(); err != nil {
		return fmt.Errorf("%w: record directory sync: %v", ErrCommitUncertain, err)
	}
	return nil
}

func writeFileContext(ctx context.Context, file *os.File, data []byte) error {
	for len(data) > 0 {
		if err := contextError(ctx); err != nil {
			return err
		}
		chunk := data
		if len(chunk) > 64<<10 {
			chunk = chunk[:64<<10]
		}
		n, err := file.Write(chunk)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writeExclusiveSynced(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written := 0
	for written < len(data) {
		n, writeErr := file.Write(data[written:])
		if writeErr != nil || n == 0 {
			_ = file.Close()
			return errors.Join(writeErr, io.ErrShortWrite)
		}
		written += n
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func readBoundedContext(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	if limit < 1 {
		return nil, fmt.Errorf("invalid read limit")
	}
	buffer := bytes.NewBuffer(make([]byte, 0, minInt64(limit, 64<<10)))
	chunk := make([]byte, 64<<10)
	var total int64
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		n, err := reader.Read(chunk)
		if n > 0 {
			if int64(n) > limit-total {
				return nil, fmt.Errorf("%w: bounded record read overflow", ErrRecordTooLarge)
			}
			total += int64(n)
			_, _ = buffer.Write(chunk[:n])
		}
		if errors.Is(err, io.EOF) {
			return buffer.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

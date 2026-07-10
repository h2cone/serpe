package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const defaultFileMode os.FileMode = 0o644

func atomicWrite(path string, data []byte, defaultMode os.FileMode) error {
	target, mode, err := writeTarget(path, defaultMode)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".ouro-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	replaceOK := false
	defer func() {
		if !replaceOK {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := atomicReplace(tmpName, target); err != nil {
		return err
	}
	replaceOK = true
	return nil
}

func writeTarget(path string, defaultMode os.FileMode) (target string, mode os.FileMode, err error) {
	target = path
	mode = defaultMode
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return target, mode, nil
		}
		return "", 0, err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", 0, fmt.Errorf("resolve symlink %s: %w", path, err)
		}
		info, err = os.Stat(target)
		if err != nil {
			return "", 0, err
		}
	}
	if info.IsDir() {
		return "", 0, fmt.Errorf("%s is a directory", target)
	}
	return target, info.Mode().Perm(), nil
}

func atomicReplace(tempName, target string) error {
	return os.Rename(tempName, target)
}

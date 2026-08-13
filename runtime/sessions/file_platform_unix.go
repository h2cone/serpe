//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package sessions

import (
	"fmt"
	"os"

	"github.com/h2cone/serpe/internal/securefs"
	"golang.org/x/sys/unix"
)

func storeFileIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("nil file")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("unix:v1:%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func validateStoreRoot(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil || !info.IsDir() {
		return fmt.Errorf("root is not a directory")
	}
	return securefs.ValidatePrivate(file, info)
}

func validateStoreRegular(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil || !info.Mode().IsRegular() {
		return fmt.Errorf("entry is not a regular file")
	}
	return securefs.ValidatePrivate(file, info)
}

func lockStoreFile(file *os.File) (func() error, error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func() error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
}

func syncStoreDirectory(file *os.File) error { return file.Sync() }

func validateStorePlatform() error { return nil }

func openStoreDirectory(path string) (*os.File, error) { return securefs.OpenDirectory(path, true) }

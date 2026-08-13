//go:build windows

package sessions

import (
	"fmt"
	"os"

	"github.com/h2cone/serpe/internal/securefs"
	"golang.org/x/sys/windows"
)

func storeFileIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("nil file")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:v1:%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func validateStoreRoot(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil || !info.IsDir() {
		return fmt.Errorf("root is not a directory")
	}
	return securefs.ValidatePrivate(file, info)
}

func validateStoreRegular(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("entry is not a regular non-reparse file")
	}
	return securefs.ValidatePrivate(file, info)
}

func lockStoreFile(file *os.File) (func() error, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		return nil, err
	}
	return func() error {
		return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
	}, nil
}

// Windows has no portable directory-fsync operation. Root.Rename preserves
// atomic visibility, while full power-loss durability is filesystem-dependent.
func syncStoreDirectory(*os.File) error { return nil }

func validateStorePlatform() error { return nil }

func openStoreDirectory(path string) (*os.File, error) { return securefs.OpenDirectory(path, true) }

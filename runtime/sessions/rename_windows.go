//go:build windows

package sessions

import (
	"errors"
	"os"
	"syscall"
	"time"
)

const (
	windowsAccessDenied     syscall.Errno = 5
	windowsSharingViolation syscall.Errno = 32
)

func renameReplace(root *os.Root, oldName, newName string) error {
	const attempts = 20
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = root.Rename(oldName, newName)
		if err == nil {
			return nil
		}
		if !isTransientRenameError(err) || attempt+1 == attempts {
			return err
		}
		time.Sleep(time.Duration(5*(attempt+1)) * time.Millisecond)
	}
	return err
}

func isTransientRenameError(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == windowsAccessDenied || errno == windowsSharingViolation
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return isTransientRenameError(linkErr.Err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return isTransientRenameError(pathErr.Err)
	}
	return false
}

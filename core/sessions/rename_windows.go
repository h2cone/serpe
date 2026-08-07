//go:build windows

package sessions

import (
	"errors"
	"os"
	"syscall"
)

// Windows ERROR_ACCESS_DENIED and ERROR_SHARING_VIOLATION, returned when a
// concurrent reader still holds the destination open during rename-replace.
const (
	windowsAccessDenied     syscall.Errno = 5
	windowsSharingViolation syscall.Errno = 32
)

// isTransientRenameError reports whether err is a Windows rename conflict
// that may clear once concurrent readers release the destination.
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

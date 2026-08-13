//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package securefs

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenRegular opens the final path component without following a symbolic
// link, validates the opened handle as a regular file, and optionally applies
// the private owner/access policy.
func OpenRegular(path string, private bool) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("securefs: wrap file descriptor")
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("securefs: path is not a regular file")
	}
	if err == nil && private {
		err = ValidatePrivate(file, info)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// OpenDirectory opens the final path component without following a symbolic
// link and optionally applies the private owner/access policy.
func OpenDirectory(path string, private bool) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("securefs: wrap directory descriptor")
	}
	info, err := file.Stat()
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("securefs: path is not a directory")
	}
	if err == nil && private {
		err = ValidatePrivate(file, info)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

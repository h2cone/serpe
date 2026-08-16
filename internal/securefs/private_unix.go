//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// Package securefs centralizes private-file ownership and access checks used
// by FileStore.
package securefs

import (
	"fmt"
	"os"
	"syscall"
)

// ValidatePrivate requires ownership by the effective service user and denies
// every group/other permission bit. The caller remains responsible for the
// expected file type and no-follow opening policy.
func ValidatePrivate(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil {
		return fmt.Errorf("securefs: missing file handle or metadata")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("securefs: file ownership is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("securefs: file is not owned by the effective service user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("securefs: group or other access is permitted")
	}
	return nil
}

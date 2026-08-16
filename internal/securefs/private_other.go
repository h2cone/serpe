//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

// Package securefs centralizes private-file ownership and access checks used
// by FileStore.
package securefs

import (
	"fmt"
	"os"
)

func ValidatePrivate(*os.File, os.FileInfo) error {
	return fmt.Errorf("securefs: private ownership validation is unsupported on this platform")
}

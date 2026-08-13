//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package securefs

import (
	"fmt"
	"os"
)

func OpenRegular(string, bool) (*os.File, error) {
	return nil, fmt.Errorf("securefs: no-follow regular-file opening is unsupported on this platform")
}

func OpenDirectory(string, bool) (*os.File, error) {
	return nil, fmt.Errorf("securefs: no-follow directory opening is unsupported on this platform")
}

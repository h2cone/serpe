//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package builtin

import (
	"fmt"
	"os"
)

func platformFileIdentity(*os.File) (string, error) {
	return "", fmt.Errorf("stable file identity is unsupported on this platform")
}

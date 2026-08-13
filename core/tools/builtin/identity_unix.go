//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package builtin

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func platformFileIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("nil file handle")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("unix:v1:%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}

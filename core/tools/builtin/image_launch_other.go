//go:build !linux && !windows

package builtin

import (
	"fmt"
	"os"
	"os/exec"
)

func validateBashLaunchPlatform() error {
	return fmt.Errorf("builtin: safe handle-based Bash launch is unsupported on this platform")
}

func openPinnedBashFile(path string) (*os.File, error) { return os.Open(path) }

func bindPinnedBashImage(*exec.Cmd, *os.File) error {
	return fmt.Errorf("builtin: safe handle-based Bash launch is unsupported on this platform")
}

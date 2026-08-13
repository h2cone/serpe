//go:build linux

package builtin

import (
	"fmt"
	"os"
	"os/exec"
)

func validateBashLaunchPlatform() error {
	info, err := os.Stat("/proc/self/fd")
	if err != nil || !info.IsDir() {
		return fmt.Errorf("builtin: safe Bash image launch requires /proc/self/fd")
	}
	return validateProcessContainment()
}

func openPinnedBashFile(path string) (*os.File, error) { return os.Open(path) }

func bindPinnedBashImage(command *exec.Cmd, image *os.File) error {
	if command == nil || image == nil {
		return fmt.Errorf("builtin: invalid pinned Bash launch")
	}
	childFD := 3 + len(command.ExtraFiles)
	command.ExtraFiles = append(command.ExtraFiles, image)
	command.Path = fmt.Sprintf("/proc/self/fd/%d", childFD)
	return nil
}

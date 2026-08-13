//go:build windows

package builtin

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func validateBashLaunchPlatform() error { return validateProcessContainment() }

func openPinnedBashFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func bindPinnedBashImage(command *exec.Cmd, image *os.File) error {
	if command == nil || image == nil {
		return fmt.Errorf("builtin: invalid pinned Bash launch")
	}
	// openPinnedBashFile deliberately omitted FILE_SHARE_WRITE and
	// FILE_SHARE_DELETE. Holding image until CreateProcess succeeds binds the
	// verified directory entry identity across process creation.
	return nil
}

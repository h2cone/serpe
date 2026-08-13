//go:build windows

package builtin

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func replaceFile(tempPath, targetPath string) error {
	temp, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	result, _, callErr := replaceFileW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(unsafe.Pointer(temp)),
		0,
		1, // REPLACEFILE_WRITE_THROUGH
		0,
		0,
	)
	if result == 0 {
		return fmt.Errorf("ReplaceFileW: %w", callErr)
	}
	return nil
}

// Windows does not expose a directory fsync equivalent through Go's portable
// file API. Atomic visibility is guaranteed; crash durability follows the
// volume's write-through semantics used by replaceFile.
func syncDirectory(string) error { return nil }

//go:build windows

package builtin

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func platformFileIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("nil file handle")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:v1:%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

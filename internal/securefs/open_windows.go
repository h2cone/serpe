//go:build windows

package securefs

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// OpenRegular opens the final path component itself, rejects reparse points,
// validates the opened handle as a regular file, and optionally applies the
// private owner/access policy.
func OpenRegular(path string, private bool) (*os.File, error) {
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
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("securefs: wrap file handle")
	}
	info, statErr := file.Stat()
	var handleInfo windows.ByHandleFileInformation
	identityErr := windows.GetFileInformationByHandle(handle, &handleInfo)
	if statErr == nil && (!info.Mode().IsRegular() || identityErr != nil || handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0) {
		statErr = fmt.Errorf("securefs: path is not a regular non-reparse file")
	}
	if statErr == nil && private {
		statErr = ValidatePrivate(file, info)
	}
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	return file, nil
}

// OpenDirectory opens the final directory entry itself, rejects reparse
// points, and optionally applies the private owner/access policy.
func OpenDirectory(path string, private bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_LIST_DIRECTORY|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("securefs: wrap directory handle")
	}
	info, statErr := file.Stat()
	var handleInfo windows.ByHandleFileInformation
	identityErr := windows.GetFileInformationByHandle(handle, &handleInfo)
	if statErr == nil && (!info.IsDir() || identityErr != nil || handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0) {
		statErr = fmt.Errorf("securefs: path is not a non-reparse directory")
	}
	if statErr == nil && private {
		statErr = ValidatePrivate(file, info)
	}
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	return file, nil
}

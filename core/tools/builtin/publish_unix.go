//go:build !windows

package builtin

import (
	"os"
)

func replaceFile(tempPath, targetPath string) error {
	return os.Rename(tempPath, targetPath)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

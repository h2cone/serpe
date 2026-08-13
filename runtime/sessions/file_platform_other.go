//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package sessions

import (
	"fmt"
	"os"
)

func storeFileIdentity(*os.File) (string, error)       { return "", fmt.Errorf("unsupported platform") }
func validateStoreRoot(*os.File, os.FileInfo) error    { return fmt.Errorf("unsupported platform") }
func validateStoreRegular(*os.File, os.FileInfo) error { return fmt.Errorf("unsupported platform") }
func lockStoreFile(*os.File) (func() error, error)     { return nil, fmt.Errorf("unsupported platform") }
func syncStoreDirectory(*os.File) error                { return fmt.Errorf("unsupported platform") }
func validateStorePlatform() error                     { return fmt.Errorf("unsupported platform") }
func openStoreDirectory(string) (*os.File, error)      { return nil, fmt.Errorf("unsupported platform") }

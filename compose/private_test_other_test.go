//go:build !windows

package compose_test

import (
	"os"
	"testing"
)

func privateComposeTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

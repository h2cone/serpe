//go:build !windows

package sessions

import (
	"os"
	"testing"
)

func restrictTestStoreDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func makeTestStorePathUnsafe(t *testing.T, path string, directory bool) {
	t.Helper()
	mode := os.FileMode(0o644)
	if directory {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindBashSkipsEmptyAndRelativeEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	name := "bash"
	if runtime.GOOS == "windows" {
		name = "bash.exe"
	}
	want := filepath.Join(dir, name)
	if err := os.WriteFile(want, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathValue := string(os.PathListSeparator) + "rel" + string(os.PathListSeparator) + dir
	if got := findBash(pathValue); got != want {
		t.Fatalf("findBash=%q, want %q", got, want)
	}
}

func TestFindBashEmptyPATH(t *testing.T) {
	t.Parallel()
	if got := findBash(""); got != "" {
		t.Fatalf("empty PATH=%q", got)
	}
}

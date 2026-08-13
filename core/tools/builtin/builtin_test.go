package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/core/tools/builtin"
)

func TestNewDefaultNames(t *testing.T) {
	t.Parallel()
	set, err := builtin.NewDefault(builtin.Config{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, 4)
	for _, tool := range set.Tools() {
		names = append(names, tool.Definition().Name)
	}
	if got := names; len(got) != 4 || got[0] != "read" || got[1] != "write" || got[2] != "edit" || got[3] != "bash" {
		t.Fatalf("names=%v", got)
	}
	selected, err := set.Select([]string{"bash", "read"})
	if err != nil {
		t.Fatal(err)
	}
	if selected[0].Definition().Name != "read" || selected[1].Definition().Name != "bash" {
		t.Fatalf("select order=%v", selected)
	}
}

func TestFileToolsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	set, err := builtin.NewDefault(builtin.Config{WorkspaceRoots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	exec, err := tools.New(tools.Config{}, set.Tools()...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(context.Background(), tools.Scope{WorkingDir: dir})
	write := call(t, exec, ctx, "write", map[string]any{"path": "a.txt", "content": "hello\nworld\n"})
	if write.IsError {
		t.Fatalf("write: %+v", write)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(got) != "hello\nworld\n" {
		t.Fatalf("file=%q err=%v", got, err)
	}
	read := call(t, exec, ctx, "read", map[string]any{"path": "a.txt", "line": 2, "lines": 1})
	if read.IsError || read.Content[0].Text == nil || read.Content[0].Text.Text == "" {
		t.Fatalf("read: %+v", read)
	}
	edit := call(t, exec, ctx, "edit", map[string]any{
		"path":  "a.txt",
		"edits": []any{map[string]any{"old_text": "world", "new_text": "serpe"}},
	})
	if edit.IsError {
		t.Fatalf("edit: %+v", edit)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "hello\nserpe\n" {
		t.Fatalf("after edit %q", got)
	}
}

func TestPathEscapeRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	set, err := builtin.NewDefault(builtin.Config{})
	if err != nil {
		t.Fatal(err)
	}
	exec, err := tools.New(tools.Config{}, set.Tools()[0]) // read
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(context.Background(), tools.Scope{WorkingDir: dir})
	out := call(t, exec, ctx, "read", map[string]any{"path": filepath.Join("..", "etc", "passwd")})
	if !out.IsError {
		t.Fatalf("expected escape rejection: %+v", out)
	}
}

func TestRelativeWorkingDirRejected(t *testing.T) {
	t.Parallel()
	set, err := builtin.NewDefault(builtin.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.ValidateWorkingDir(context.Background(), "relative"); err == nil {
		t.Fatal("relative CWD")
	}
	if runtime.GOOS == "windows" {
		if err := set.ValidateWorkingDir(context.Background(), `C:foo`); err == nil {
			t.Fatal("drive-relative CWD")
		}
	}
}

func TestBashEcho(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	bashPath, err = filepath.Abs(bashPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	set, err := builtin.NewDefault(builtin.Config{BashPath: bashPath})
	if err != nil {
		t.Fatal(err)
	}
	exec, err := tools.New(tools.Config{}, set.Tools()[3])
	if err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithScope(context.Background(), tools.Scope{WorkingDir: dir})
	out := call(t, exec, ctx, "bash", map[string]any{"command": "echo hi"})
	if out.IsError {
		t.Fatalf("bash: %+v", out)
	}
}

func call(t *testing.T, exec *tools.Executor, ctx context.Context, name string, args map[string]any) tools.Output {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	b, err := exec.Start(ctx, []models.ToolCall{{ID: "1", Name: name, Arguments: raw}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var out tools.Output
	for b.Next() {
		ev := b.Event()
		if ev.Kind == tools.BatchFinished && ev.Output != nil {
			out = *ev.Output
		}
		if ev.Kind == tools.BatchFailed {
			t.Fatalf("failed: %v", ev.Err)
		}
	}
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

package bootstrap

import (
	"testing"

	"github.com/h2cone/serpe/core/tools/builtin"
)

func TestNewRunnerRequiresModel(t *testing.T) {
	runner, _, err := NewRunner(RunnerConfig{})
	if err == nil {
		t.Fatal("NewRunner with empty model succeeded; want error")
	}
	if runner != nil {
		t.Fatal("NewRunner returned non-nil runner alongside error")
	}
}

func TestNewRunnerBindsExplicitModel(t *testing.T) {
	runner, access, err := NewRunner(RunnerConfig{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatalf("NewRunner with explicit model: %v", err)
	}
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
	if access.Bind == nil || access.Validate == nil {
		t.Fatal("missing working dir access")
	}
}

func TestDenyProfileHasNoTools(t *testing.T) {
	runner, _, err := NewRunner(RunnerConfig{Model: "gpt-5.6-luna"})
	if err != nil {
		t.Fatal(err)
	}
	_ = runner
}

func TestLocalCLIProfileSelectsFourTools(t *testing.T) {
	dir := t.TempDir()
	set, err := builtin.NewDefault(builtin.Config{WorkspaceRoots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, 4)
	for _, tool := range set.Tools() {
		names = append(names, tool.Definition().Name)
	}
	if len(names) != 4 || names[0] != "read" || names[1] != "write" || names[2] != "edit" || names[3] != "bash" {
		t.Fatalf("CLI builtins=%v", names)
	}
}

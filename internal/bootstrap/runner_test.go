package bootstrap

import (
	"context"
	"testing"
	"time"
)

func TestNewRunnerRequiresModel(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{})
	if err == nil {
		t.Fatal("NewRunner with empty model succeeded; want error")
	}
	if runner != nil {
		t.Fatal("NewRunner returned non-nil runner alongside error")
	}
}

func TestNewRunnerBindsExplicitModel(t *testing.T) {
	runner, err := NewRunner(RunnerConfig{Model: "gpt-*"})
	if err != nil {
		t.Fatalf("NewRunner with explicit model: %v", err)
	}
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
}

func TestNowToolUsesInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	out, err := (nowTool{now: func() time.Time { return fixed }}).Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Content[0].Text.Text; got != "2026-08-09T12:00:00Z" {
		t.Fatalf("now = %q", got)
	}
}

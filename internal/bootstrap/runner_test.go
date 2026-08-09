package bootstrap

import (
	"context"
	"testing"
	"time"
)

func TestNewRunnerAppliesSharedDefaults(t *testing.T) {
	fixed := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	runner, err := NewRunner(RunnerConfig{Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
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

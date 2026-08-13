package httpapi_test

import (
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/httpapi"
	"github.com/h2cone/serpe/runtime/loops"
	"github.com/h2cone/serpe/runtime/sessions"
)

func TestNewRequiresOneRunnerManagerCompositionRoot(t *testing.T) {
	runner, err := loops.New(loops.Config{
		Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessions.NewManager(sessions.NewMemoryStore(), sessions.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	invalid := []httpapi.Config{
		{Manager: manager, CWD: t.TempDir()},
		{Runner: runner, CWD: t.TempDir()},
		{Runner: runner, Manager: manager, CWD: "  "},
	}
	for i, cfg := range invalid {
		if _, err := httpapi.New(cfg); err == nil {
			t.Fatalf("invalid config %d succeeded", i)
		}
	}
	if _, err := httpapi.New(httpapi.Config{Runner: runner, Manager: manager, CWD: t.TempDir(), AllowInsecureNoAuth: true}); err != nil {
		t.Fatalf("valid config failed: %v", err)
	}
}

package httpapi_test

import (
	"testing"

	"github.com/h2cone/serpe/agent"
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/sessions"
	"github.com/h2cone/serpe/internal/httpapi"
)

func TestNewRequiresOneRunnerManagerCompositionRoot(t *testing.T) {
	runner, err := agent.NewRunner(agent.Config{
		Model: &scriptedModel{responses: []*models.Response{textResponse("ok")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessions.NewManager(sessions.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}

	invalid := []httpapi.Config{
		{Manager: manager, CWD: "/tmp"},
		{Runner: runner, CWD: "/tmp"},
		{Runner: runner, Manager: manager, CWD: "  "},
	}
	for i, cfg := range invalid {
		if _, err := httpapi.New(cfg); err == nil {
			t.Fatalf("invalid config %d succeeded", i)
		}
	}
	if _, err := httpapi.New(httpapi.Config{Runner: runner, Manager: manager, CWD: "/tmp"}); err != nil {
		t.Fatalf("valid config failed: %v", err)
	}
}

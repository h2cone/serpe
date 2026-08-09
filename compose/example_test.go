package compose_test

import (
	"context"
	"fmt"
	"log"

	"github.com/h2cone/serpe/agent"
	"github.com/h2cone/serpe/compose"
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/sessions"
)

// Example demonstrates create session → Send → assert committed suffix.
func Example() {
	model := &scriptedModel{responses: []*models.Response{textResponse("Let me look.")}}
	runner, err := agent.NewRunner(agent.Config{Model: model})
	if err != nil {
		log.Fatal(err)
	}
	mgr, err := sessions.NewManager(sessions.NewMemoryStore())
	if err != nil {
		log.Fatal(err)
	}
	svc, err := compose.New(compose.Config{Runner: runner, Manager: mgr})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if _, err := mgr.Create(ctx, sessions.New("sess-1", "/work")); err != nil {
		log.Fatal(err)
	}

	result, committed, err := svc.Send(ctx, "sess-1", "What is in this repo?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Completed())
	fmt.Println(len(committed.Messages))
	fmt.Println(committed.Messages[0].Content[0].PlainText())
	fmt.Println(committed.Messages[1].Content[0].PlainText())
	// Output:
	// true
	// 2
	// What is in this repo?
	// Let me look.
}

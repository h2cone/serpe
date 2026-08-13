package sessions_test

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/runtime/sessions"
)

// Example demonstrates the minimal flow: create an empty session, append
// user/assistant messages, fork it, and read back the snapshot.
func Example() {
	manager, err := sessions.NewManager(sessions.NewMemoryStore(), sessions.Limits{})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	ctx := context.Background()

	cwd, err := filepath.Abs("work")
	if err != nil {
		log.Fatal(err)
	}
	created, err := manager.Create(ctx, sessions.New("sess-1", cwd))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(created.ID, len(created.Messages))

	if _, err := manager.Append(ctx, "sess-1",
		models.NewUserMessage(models.Text("What is in this repo?")),
		models.NewAssistantMessage(models.Text("Let me look.")),
	); err != nil {
		log.Fatal(err)
	}

	forked, err := manager.Fork(ctx, "sess-1", "sess-1-fork")
	if err != nil {
		log.Fatal(err)
	}
	got, err := manager.Get(ctx, forked.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(forked.ParentID, len(got.Messages), got.Messages[0].Content[0].Text.Text)

	// Output:
	// sess-1 0
	// sess-1 2 What is in this repo?
}

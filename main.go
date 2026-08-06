// Command ouro assembles a provider model and agent.Runner, then renders
// run-level events. The model–tool loop lives in package agent.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/providers"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	provider := must(providers.New(providers.Config{
		Protocol: providers.OpenAIResponses,
		APIKey:   os.Getenv("OPENAI_API_KEY"),
		BaseURL:  os.Getenv("OPENAI_BASE_URL"),
	}))
	model := must(provider.Model("glm-5.2"))

	runner := must(agent.NewRunner(agent.Config{
		Model: model,
		Tools: []agent.Tool{nowTool{}},
	}))

	prompt := "What time is it?"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	stream := must(runner.Stream(ctx, models.NewTextRequest(prompt)))
	defer stream.Close()

	for stream.Next() {
		render(stream.Event())
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
	result := stream.Result()
	if result.Completed() {
		return
	}
	fmt.Fprintf(os.Stderr, "stopped: %s\n", result.StopReason)
}

type nowTool struct{}

func (nowTool) Definition() models.Tool {
	return models.NewTool("now", "Current wall-clock time in RFC 3339.", json.RawMessage(`{"type":"object","properties":{}}`))
}

func (nowTool) Execute(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
	return agent.TextResult(time.Now().Format(time.RFC3339)), nil
}

func render(ev agent.Event) {
	switch ev.Kind {
	case agent.EventModel:
		fmt.Print(ev.Model.DisplayText())
	case agent.EventToolStart:
		if ev.ToolCall != nil {
			fmt.Fprintf(os.Stderr, "\n[tool %s]\n", ev.ToolCall.Name)
		}
	case agent.EventRunEnd:
		if ev.StopReason == agent.StopCompleted {
			fmt.Println()
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		log.Fatal(err)
	}
	return v
}

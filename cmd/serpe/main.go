// Command serpe assembles a provider model and agent.Runner, then renders
// run-level events. The model–tool loop lives in package agent.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/h2cone/serpe/agent"
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/bootstrap"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runner := must(bootstrap.NewRunner(bootstrap.RunnerConfigFromEnv()))

	prompt := "What time is it?"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	stream := must(runner.NewStream(ctx, models.NewTextRequest(prompt)))
	defer stream.Close()

	// model_start: after model.Stream returns (HTTP headers ready).
	// first_token: first non-empty DisplayText delta.
	var sawModelStart, sawFirstToken bool
	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case agent.EventModelStart:
			if !sawModelStart {
				log.Print("model_start")
				sawModelStart = true
			}
		case agent.EventModel:
			if !sawFirstToken && ev.Model.DisplayText() != "" {
				log.Print("first_token")
				sawFirstToken = true
			}
		case agent.EventToolStart:
			if ev.ToolCall != nil {
				log.Printf("tool %s", ev.ToolCall.Name)
			}
		}
		render(ev)
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
	log.Print("done")
	result := stream.Result()
	if result.Completed() {
		return
	}
	log.Printf("stopped: %s", result.StopReason)
}

func render(ev agent.Event) {
	switch ev.Kind {
	case agent.EventModel:
		fmt.Print(ev.Model.DisplayText())
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

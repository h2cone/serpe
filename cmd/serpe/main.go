// Command serpe assembles a provider model and loops.Runner, then renders
// run-level events. The model–tool loop lives in package loops.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/core/tools/builtin"
	"github.com/h2cone/serpe/internal/bootstrap"
	"github.com/h2cone/serpe/runtime/loops"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cwd := must(filepath.Abs(must(os.Getwd())))
	cfg := bootstrap.RunnerConfigFromEnv()
	cfg.Tools = must(builtin.NewDefault()).Tools()
	runner := must(bootstrap.NewRunner(cfg))
	ctx = tools.WithScope(ctx, tools.Scope{WorkingDir: cwd})

	prompt := "Read README.md and summarize this repository in three bullets."
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	stream := must(runner.Stream(ctx, models.NewTextRequest(prompt)))
	defer stream.Close()

	// model_start: after model.Stream returns (HTTP headers ready).
	// first_token: first non-empty DisplayText delta.
	var sawModelStart, sawFirstToken bool
	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case loops.EventModelStart:
			if !sawModelStart {
				log.Print("model_start")
				sawModelStart = true
			}
		case loops.EventModel:
			if !sawFirstToken && ev.Model.DisplayText() != "" {
				log.Print("first_token")
				sawFirstToken = true
			}
		case loops.EventToolStart:
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

func render(ev loops.Event) {
	switch ev.Kind {
	case loops.EventModel:
		fmt.Print(ev.Model.DisplayText())
	case loops.EventRunEnd:
		if ev.StopReason == loops.StopCompleted {
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

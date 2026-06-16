package main

import (
	"context"
	"fmt"
	"os"

	"github.com/tw8ap/ouro/internal/agent"
)

const defaultInstructions = "You are a helpful assistant. Be concise."

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cli, err := agent.ParseCLI(args)
	if err != nil {
		return err
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is not set")
	}

	baseURL := os.Getenv("OPENAI_BASE_URL")
	transport := agent.NewHTTPTransport(baseURL, apiKey, nil)

	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}

	runner := agent.New(agent.Config{
		Transport:    transport,
		Tools:        agent.NewToolExecutor(workingDir),
		Model:        cli.Model,
		Instructions: defaultInstructions,
	})

	text, err := runner.Run(ctx, cli.Task)
	if err != nil {
		return err
	}

	fmt.Println(text)
	return nil
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tw8ap/ouro/internal/agent"
	"github.com/tw8ap/ouro/internal/codec"
	"github.com/tw8ap/ouro/internal/provider"
)

const defaultInstructions = "You are a helpful assistant. Be concise."

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	protocolName := fs.String("protocol", codec.OpenAIResponses.Name, "upstream protocol: openai-responses, openai-chat, or anthropic-messages")
	baseURLFlag := fs.String("base-url", "", "upstream API version root; defaults to the selected protocol")
	apiKeyFlag := fs.String("api-key", "", "upstream API key; prefer env vars for normal use")
	apiKeyEnvFlag := fs.String("api-key-env", "", "environment variable containing the upstream API key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	proto, ok := codec.Lookup(*protocolName)
	if !ok {
		return fmt.Errorf("unknown protocol %q (valid: %s)", *protocolName, protocolNames())
	}

	cli, err := parseCLI(fs.Args())
	if err != nil {
		return err
	}

	apiKey := *apiKeyFlag
	if apiKey == "" {
		envName := *apiKeyEnvFlag
		if envName == "" {
			envName = defaultAPIKeyEnv(proto.Name)
		}
		apiKey = os.Getenv(envName)
		if apiKey == "" {
			return fmt.Errorf("%s is not set", envName)
		}
	}

	baseURL := *baseURLFlag
	if baseURL == "" {
		baseURL = os.Getenv(defaultBaseURLEnv(proto.Name))
	}
	upstream := provider.NewHTTPProvider(proto, baseURL, apiKey, nil)

	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}

	runner := agent.New(agent.Config{
		Provider:     upstream,
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

func defaultAPIKeyEnv(protocolName string) string {
	if protocolName == codec.AnthropicMessages.Name {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

func defaultBaseURLEnv(protocolName string) string {
	if protocolName == codec.AnthropicMessages.Name {
		return "ANTHROPIC_BASE_URL"
	}
	return "OPENAI_BASE_URL"
}

func protocolNames() string {
	protocols := codec.Protocols()
	names := make([]string, 0, len(protocols))
	for _, proto := range protocols {
		names = append(names, proto.Name)
	}
	return strings.Join(names, ", ")
}

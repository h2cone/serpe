// Package bootstrap owns the shared application wiring used by Serpe entry
// points. Commands keep argument parsing and presentation; provider/model/tool
// construction lives here so one configuration decision has one owner.
package bootstrap

import (
	"fmt"
	"os"

	"github.com/h2cone/serpe/core/providers"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/runtime/loops"
)

// RunnerConfig contains the provider settings shared by command entrypoints.
// Tools are already constructed by the composition root; an empty list
// registers no local tools.
type RunnerConfig struct {
	APIKey       string
	BaseURL      string
	Model        string
	Tools        []tools.Tool
	ToolExecutor tools.Config
}

// RunnerConfigFromEnv reads the Serpe OpenAI environment contract.
func RunnerConfigFromEnv() RunnerConfig {
	return RunnerConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		Model:   os.Getenv("OPENAI_DEFAULT_MODEL"),
	}
}

// NewRunner constructs the provider model and optional tool executor.
func NewRunner(cfg RunnerConfig) (*loops.Runner, error) {
	provider, err := providers.New(providers.Config{
		Protocol: providers.OpenAIResponses,
		APIKey:   cfg.APIKey,
		BaseURL:  cfg.BaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap provider: %w", err)
	}
	model, err := provider.ResolveModel(cfg.Model)
	if err != nil {
		return nil, fmt.Errorf("bootstrap model %q: %w", cfg.Model, err)
	}
	var exec *tools.Executor
	if len(cfg.Tools) > 0 {
		exec, err = tools.New(cfg.ToolExecutor, cfg.Tools...)
		if err != nil {
			return nil, fmt.Errorf("bootstrap tools: %w", err)
		}
	}
	runner, err := loops.New(loops.Config{
		Model: model,
		Tools: exec,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap runner: %w", err)
	}
	return runner, nil
}

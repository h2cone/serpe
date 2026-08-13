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
type RunnerConfig struct {
	APIKey           string
	BaseURL          string
	Model            string
	ToolProfile      *ToolProfile
	ContributedTools []tools.Tool
	ToolExecutor     tools.Config
}

// RunnerConfigFromEnv reads the Serpe OpenAI environment contract.
func RunnerConfigFromEnv() RunnerConfig {
	return RunnerConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		Model:   os.Getenv("OPENAI_DEFAULT_MODEL"),
	}
}

// NewRunner constructs the provider model and shared application tools.
func NewRunner(cfg RunnerConfig) (*loops.Runner, WorkingDirAccess, error) {
	provider, err := providers.New(providers.Config{
		Protocol: providers.OpenAIResponses,
		APIKey:   cfg.APIKey,
		BaseURL:  cfg.BaseURL,
	})
	if err != nil {
		return nil, WorkingDirAccess{}, fmt.Errorf("bootstrap provider: %w", err)
	}
	model, err := provider.ResolveModel(cfg.Model)
	if err != nil {
		return nil, WorkingDirAccess{}, fmt.Errorf("bootstrap model %q: %w", cfg.Model, err)
	}
	exec, access, err := buildTools(cfg.ToolProfile, cfg.ContributedTools, cfg.ToolExecutor)
	if err != nil {
		return nil, WorkingDirAccess{}, err
	}
	runner, err := loops.New(loops.Config{
		Model: model,
		Tools: exec,
	})
	if err != nil {
		return nil, WorkingDirAccess{}, fmt.Errorf("bootstrap runner: %w", err)
	}
	return runner, access, nil
}

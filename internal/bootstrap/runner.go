// Package bootstrap owns the shared application wiring used by Serpe entry
// points. Commands keep argument parsing and presentation; provider/model/tool
// construction lives here so one configuration decision has one owner.
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/h2cone/serpe/runtime"
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers"
)

// RunnerConfig contains the provider settings shared by command entrypoints.
// Now is an optional clock seam for tests and embeddings.
type RunnerConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Now     func() time.Time
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
func NewRunner(cfg RunnerConfig) (*runtime.Runner, error) {
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
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	runner, err := runtime.NewRunner(runtime.Config{
		Model: model,
		Tools: []runtime.Tool{nowTool{now: now}},
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap runner: %w", err)
	}
	return runner, nil
}

type nowTool struct {
	now func() time.Time
}

func (nowTool) Definition() models.Tool {
	return models.NewTool("now", "Current wall-clock time in RFC 3339.", json.RawMessage(`{"type":"object","properties":{}}`))
}

func (t nowTool) Execute(_ context.Context, _ json.RawMessage) (runtime.ToolOutput, error) {
	return runtime.TextResult(t.now().Format(time.RFC3339)), nil
}

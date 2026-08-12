package runtime

import (
	"fmt"

	"github.com/h2cone/serpe/core/models"
)

// Config constructs an immutable Runner.
type Config struct {
	Model  models.Model
	Tools  []Tool
	Limits Limits
}

// Limits bounds a single run. Zero values for MaxModelTurns, MaxToolCalls, and
// MaxIdenticalSteps use safe defaults and cannot disable those bounds.
// MaxObservedTokens zero disables the observed-token limit.
type Limits struct {
	MaxModelTurns     int
	MaxToolCalls      int
	MaxObservedTokens int64
	MaxIdenticalSteps int
}

const (
	defaultMaxModelTurns     = 32
	defaultMaxToolCalls      = 128
	defaultMaxIdenticalSteps = 3
)

// Runner executes agent runs against a fixed model and tool set.
// After construction it is immutable and safe for concurrent use.
type Runner struct {
	model  models.Model
	tools  toolSet
	limits Limits
}

// NewRunner validates config and builds a concurrent-safe Runner.
func NewRunner(config Config) (*Runner, error) {
	if config.Model == nil {
		return nil, fmt.Errorf("%w: model is required", ErrInvalidConfig)
	}
	limits, err := normalizeLimits(config.Limits)
	if err != nil {
		return nil, err
	}
	tools, err := registerTools(config.Tools)
	if err != nil {
		return nil, err
	}
	return &Runner{model: config.Model, tools: tools, limits: limits}, nil
}

func normalizeLimits(limits Limits) (Limits, error) {
	if limits.MaxModelTurns < 0 || limits.MaxToolCalls < 0 || limits.MaxObservedTokens < 0 || limits.MaxIdenticalSteps < 0 {
		return Limits{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidConfig)
	}
	if limits.MaxModelTurns == 0 {
		limits.MaxModelTurns = defaultMaxModelTurns
	}
	if limits.MaxToolCalls == 0 {
		limits.MaxToolCalls = defaultMaxToolCalls
	}
	if limits.MaxIdenticalSteps == 0 {
		limits.MaxIdenticalSteps = defaultMaxIdenticalSteps
	}
	return limits, nil
}

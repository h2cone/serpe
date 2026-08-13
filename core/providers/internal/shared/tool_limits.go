package shared

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
)

const (
	hardMaxToolCalls          = 128
	hardMaxCallIDBytes        = int64(1 << 10)
	hardMaxToolNameBytes      = int64(1 << 10)
	hardMaxArgumentsBytes     = int64(16 << 20)
	hardMaxBatchArgumentBytes = int64(16 << 20)
)

// ToolCallLimits are protocol-decoder ceilings. They are applied before a
// source grows a builder or publishes normalized events to models.Stream.
type ToolCallLimits struct {
	MaxCalls              int
	MaxCallIDBytes        int64
	MaxToolNameBytes      int64
	MaxArgumentsBytes     int64
	MaxBatchArgumentBytes int64
}

// EffectiveToolCallLimits intersects provider transport ceilings with the
// per-run stream ceilings supplied by runtime. Zero stream fields keep the
// provider value, whose Config has already been normalized.
func EffectiveToolCallLimits(provider Limits, stream models.StreamLimits) ToolCallLimits {
	limits := ToolCallLimits{
		MaxCalls:              positiveInt(provider.MaxToolCalls, hardMaxToolCalls),
		MaxCallIDBytes:        positiveInt64(provider.MaxCallIDBytes, hardMaxCallIDBytes),
		MaxToolNameBytes:      positiveInt64(provider.MaxToolNameBytes, hardMaxToolNameBytes),
		MaxArgumentsBytes:     positiveInt64(provider.MaxArgumentsBytes, hardMaxArgumentsBytes),
		MaxBatchArgumentBytes: positiveInt64(provider.MaxBatchArgumentBytes, hardMaxBatchArgumentBytes),
	}
	limits.MaxCalls = tighterInt(limits.MaxCalls, stream.MaxToolCalls)
	limits.MaxCallIDBytes = tighterInt64(limits.MaxCallIDBytes, stream.MaxCallIDBytes)
	limits.MaxToolNameBytes = tighterInt64(limits.MaxToolNameBytes, stream.MaxToolNameBytes)
	limits.MaxArgumentsBytes = tighterInt64(limits.MaxArgumentsBytes, stream.MaxArgumentsBytes)
	limits.MaxBatchArgumentBytes = tighterInt64(limits.MaxBatchArgumentBytes, stream.MaxBatchArgumentBytes)
	return limits
}

// DefaultToolCallLimits supplies hard ceilings to protocol fixtures that do
// not pass an explicit normalized provider configuration.
func DefaultToolCallLimits() ToolCallLimits {
	return EffectiveToolCallLimits(Limits{}, models.StreamLimits{})
}

func positiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func positiveInt64(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func tighterInt(current, candidate int) int {
	if candidate > 0 && candidate < current {
		return candidate
	}
	return current
}

func tighterInt64(current, candidate int64) int64 {
	if candidate > 0 && candidate < current {
		return candidate
	}
	return current
}

// ToolCallGuard accounts decoded calls and arguments before protocol-owned
// buffers grow. A key identifies one logical wire call within one response.
type ToolCallGuard struct {
	limits        ToolCallLimits
	calls         map[string]*guardedCall
	argumentBytes int64
}

type guardedCall struct {
	argumentBytes int64
}

// NewToolCallGuard creates a fresh turn guard.
func NewToolCallGuard(limits ToolCallLimits) *ToolCallGuard {
	if limits.MaxCalls <= 0 {
		limits = DefaultToolCallLimits()
	}
	return &ToolCallGuard{limits: limits, calls: make(map[string]*guardedCall)}
}

// Start admits a new logical call and its currently known identity. Reusing a
// key is a protocol error because it would merge independent calls.
func (g *ToolCallGuard) Start(key, id, name string) error {
	if g == nil {
		return fmt.Errorf("tool-call guard is nil")
	}
	if _, exists := g.calls[key]; exists {
		return fmt.Errorf("duplicate decoded tool call")
	}
	if len(g.calls) >= g.limits.MaxCalls {
		return fmt.Errorf("decoded tool call count exceeds %d", g.limits.MaxCalls)
	}
	if err := validateWireIdentity(id, g.limits.MaxCallIDBytes, true); err != nil {
		return fmt.Errorf("decoded call ID: %w", err)
	}
	if err := validateWireIdentity(name, g.limits.MaxToolNameBytes, true); err != nil {
		return fmt.Errorf("decoded tool name: %w", err)
	}
	g.calls[key] = &guardedCall{}
	return nil
}

// Identity validates a later full ID/name update for an admitted call.
func (g *ToolCallGuard) Identity(key, id, name string) error {
	if _, exists := g.calls[key]; !exists {
		return fmt.Errorf("decoded tool call identity before start")
	}
	if err := validateWireIdentity(id, g.limits.MaxCallIDBytes, true); err != nil {
		return fmt.Errorf("decoded call ID: %w", err)
	}
	if err := validateWireIdentity(name, g.limits.MaxToolNameBytes, true); err != nil {
		return fmt.Errorf("decoded tool name: %w", err)
	}
	return nil
}

// AddArguments reserves bytes before a caller appends them to a builder or
// normalized event. Failed reservations do not mutate the counters.
func (g *ToolCallGuard) AddArguments(key string, size int) error {
	call := g.calls[key]
	if call == nil {
		return fmt.Errorf("decoded tool arguments before start")
	}
	addition := int64(size)
	if addition < 0 || addition > g.limits.MaxArgumentsBytes-call.argumentBytes {
		return fmt.Errorf("decoded tool arguments exceed per-call limit")
	}
	if addition > g.limits.MaxBatchArgumentBytes-g.argumentBytes {
		return fmt.Errorf("decoded tool arguments exceed aggregate limit")
	}
	call.argumentBytes += addition
	g.argumentBytes += addition
	return nil
}

func validateWireIdentity(value string, limit int64, allowEmpty bool) error {
	if value == "" && !allowEmpty {
		return fmt.Errorf("is empty")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("is not valid UTF-8")
	}
	if int64(len(value)) > limit {
		return fmt.Errorf("exceeds %d bytes", limit)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("contains a control character")
		}
	}
	return nil
}

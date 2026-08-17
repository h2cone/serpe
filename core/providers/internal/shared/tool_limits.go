package shared

import (
	"fmt"

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
// Identity and argument grammar use the same hard ceilings as tools.InputLimits.
type ToolCallLimits struct {
	MaxCalls              int
	MaxCallIDBytes        int64
	MaxToolNameBytes      int64
	MaxArgumentsBytes     int64
	MaxBatchArgumentBytes int64
}

// ToolCallLimitsFromStream uses the turn's MaxToolCalls and the package
// tool-call grammar ceilings. Zero or negative MaxToolCalls keeps the hard
// call ceiling.
func ToolCallLimitsFromStream(stream models.StreamLimits) ToolCallLimits {
	calls := stream.MaxToolCalls
	if calls <= 0 {
		calls = hardMaxToolCalls
	}
	return ToolCallLimits{
		MaxCalls:              calls,
		MaxCallIDBytes:        hardMaxCallIDBytes,
		MaxToolNameBytes:      hardMaxToolNameBytes,
		MaxArgumentsBytes:     hardMaxArgumentsBytes,
		MaxBatchArgumentBytes: hardMaxBatchArgumentBytes,
	}
}

// DefaultToolCallLimits supplies hard ceilings to protocol fixtures that do
// not pass an explicit stream envelope.
func DefaultToolCallLimits() ToolCallLimits {
	return ToolCallLimitsFromStream(models.StreamLimits{})
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
	if err := models.BoundedIdentity(id, g.limits.MaxCallIDBytes, true); err != nil {
		return fmt.Errorf("decoded call ID: %w", err)
	}
	if err := models.BoundedIdentity(name, g.limits.MaxToolNameBytes, true); err != nil {
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
	if err := models.BoundedIdentity(id, g.limits.MaxCallIDBytes, true); err != nil {
		return fmt.Errorf("decoded call ID: %w", err)
	}
	if err := models.BoundedIdentity(name, g.limits.MaxToolNameBytes, true); err != nil {
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

package sessions

import "fmt"

// Limits bounds Manager-owned session projections. Zero values use defaults;
// positive values may only tighten package ceilings.
type Limits struct {
	MaxSessionMessageJSONBytes int64
}

const (
	defaultMaxSessionMessageJSONBytes = int64(47 << 20)
	minSessionMessageJSONBytes        = int64(4 << 10)
)

func normalizeLimits(limits Limits) (Limits, error) {
	value := limits.MaxSessionMessageJSONBytes
	if value < 0 {
		return Limits{}, fmt.Errorf("%w: MaxSessionMessageJSONBytes must not be negative", ErrInvalidSession)
	}
	if value == 0 {
		value = defaultMaxSessionMessageJSONBytes
	}
	if value < minSessionMessageJSONBytes || value > defaultMaxSessionMessageJSONBytes {
		return Limits{}, fmt.Errorf("%w: MaxSessionMessageJSONBytes must be between %d and %d", ErrInvalidSession, minSessionMessageJSONBytes, defaultMaxSessionMessageJSONBytes)
	}
	limits.MaxSessionMessageJSONBytes = value
	return limits, nil
}

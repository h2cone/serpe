package agent

import "errors"

// Sentinel errors classify configuration and runtime failures.
var (
	// ErrInvalidConfig reports a rejected Runner configuration.
	ErrInvalidConfig = errors.New("agent: invalid config")
	// ErrToolExecution reports a fatal tool infrastructure failure.
	ErrToolExecution = errors.New("agent: tool execution failed")
	// ErrInvalidModelResponse reports a response the runtime cannot continue from.
	ErrInvalidModelResponse = errors.New("agent: invalid model response")
	// ErrModelResponse reports a valid model terminal that is not safe to
	// commit as completed or continue through tool execution.
	ErrModelResponse = errors.New("agent: model response did not complete")
)

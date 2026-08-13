package tools

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// Sentinel errors classify construction, input, output, and execution
// failures. Callers should use errors.Is.
var (
	ErrInvalidConfig = errors.New("tools: invalid config")
	ErrInputLimit    = errors.New("tools: input limit exceeded")
	ErrOutputLimit   = errors.New("tools: output limit exceeded")
	ErrExecution     = errors.New("tools: execution failed")
)

// CallError is a model-recoverable refusal from Plan or Activate. It is not
// a legal Execute/Run failure: those paths must return Output{IsError:true}.
type CallError struct {
	Message string
}

// Error implements error.
func (e *CallError) Error() string {
	if e == nil {
		return "tools: call rejected"
	}
	return e.Message
}

const maxCallErrorBytes = 4 << 10

// Reject constructs a CallError. message must be non-empty valid UTF-8 of at
// most 4 KiB. Direct CallError literals are re-validated when observed.
func Reject(message string) error {
	if err := validateCallMessage(message); err != nil {
		return fmt.Errorf("%w: %v", ErrExecution, err)
	}
	return &CallError{Message: message}
}

func validateCallMessage(message string) error {
	if message == "" {
		return fmt.Errorf("call error message is required")
	}
	if !utf8.ValidString(message) {
		return fmt.Errorf("call error message is not valid UTF-8")
	}
	if len(message) > maxCallErrorBytes {
		return fmt.Errorf("call error message exceeds %d bytes", maxCallErrorBytes)
	}
	return nil
}

func asCallError(err error) (*CallError, bool) {
	var call *CallError
	if !errors.As(err, &call) || call == nil {
		return nil, false
	}
	if validateCallMessage(call.Message) != nil {
		return nil, false
	}
	return call, true
}

func wrapExecution(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrExecution}, args...)...)
}

func wrapConfig(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidConfig}, args...)...)
}

func wrapInput(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInputLimit}, args...)...)
}

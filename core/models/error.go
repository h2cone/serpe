package models

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrorKind is a stable error category.
type ErrorKind string

const (
	ErrorInvalidRequest     ErrorKind = "invalid_request"
	ErrorAuthentication     ErrorKind = "authentication"
	ErrorPermission         ErrorKind = "permission"
	ErrorNotFound           ErrorKind = "not_found"
	ErrorConflict           ErrorKind = "conflict"
	ErrorRateLimited        ErrorKind = "rate_limited"
	ErrorTimeout            ErrorKind = "timeout"
	ErrorUnavailable        ErrorKind = "unavailable"
	ErrorUnsupportedFeature ErrorKind = "unsupported_feature"
	ErrorProtocol           ErrorKind = "protocol"
	ErrorCancelled          ErrorKind = "cancelled"
	ErrorUnknown            ErrorKind = "unknown"
)

// Error is a normalized, safely displayable provider or protocol error.
type Error struct {
	Kind       ErrorKind
	Provider   string
	Operation  string
	HTTPStatus int
	Code       string
	Message    string
	RequestID  string
	RetryAfter time.Duration
	Retryable  bool
	Cause      error
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	prefix := string(e.Kind)
	if e.Provider != "" {
		prefix = e.Provider + "/" + prefix
	}
	if e.Operation != "" {
		prefix += " during " + e.Operation
	}
	if e.Message != "" {
		return prefix + ": " + e.Message
	}
	if e.Cause != nil {
		return prefix + ": " + e.Cause.Error()
	}
	return prefix
}

// Unwrap returns the transport or context cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is preserves errors.Is classification for cancellation and deadlines even
// when a transport did not retain the original context error as its cause.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == context.Canceled && e.Kind == ErrorCancelled {
		return true
	}
	if target == context.DeadlineExceeded && e.Kind == ErrorTimeout {
		return true
	}
	return e.Cause != nil && errors.Is(e.Cause, target)
}

// NewError creates a normalized error.
func NewError(kind ErrorKind, provider, operation, message string, cause error) *Error {
	return &Error{Kind: kind, Provider: provider, Operation: operation, Message: message, Cause: cause}
}

// ContextError normalizes a context cancellation or deadline. A nil error
// returns nil.
func ContextError(provider, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrorTimeout, Provider: provider, Operation: operation, Message: "deadline exceeded", Retryable: true, Cause: context.DeadlineExceeded}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: ErrorCancelled, Provider: provider, Operation: operation, Message: "operation cancelled", Cause: context.Canceled}
	}
	return &Error{Kind: ErrorUnknown, Provider: provider, Operation: operation, Message: fmt.Sprintf("%v", err), Cause: err}
}

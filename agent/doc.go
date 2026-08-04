// Package agent provides a reusable agent runtime over provider-neutral models.
//
// A Runner binds a models.Model to registered Tools and executes one run at a
// time through a pull-based Stream. Runner itself is immutable and safe for
// concurrent use: each Run/Stream owns its transcript, counters, active model
// stream, and stop state. The package depends only on core/models, the
// module-private JSON value leaf, and the Go standard library.
//
// Ownership: every request, transcript message, event, and result returned to
// the caller is defensively copied. Callers may mutate those values without
// affecting an in-flight or completed run. Runtime never retains caller-owned
// request, tool definition, or tool result backing storage.
//
// Side effects occur only through Tool.Execute. Unknown tools yield a model-
// recoverable error result; fatal tool, provider, and context errors terminate
// the run. Incomplete, failed, truncated, and non-refusal filtered model
// terminals return ErrModelResponse with a partial Result and are never treated
// as completed. Budget and stall stops return a Result with a stable StopReason
// and a nil error. The only committable outcome is a nil error together with
// Result.Completed reporting true.
package agent

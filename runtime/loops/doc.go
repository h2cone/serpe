// Package loops is the model–tool loop: one Runner binds a models.Model to a
// tools.Executor and executes a single run through a pull-based Stream.
// Persistable conversation snapshots live in package sessions, not here.
//
// Runner itself is immutable and safe for concurrent use: each Run/Stream
// owns its transcript, counters, active model stream, and stop state. Tool
// registration, scheduling, and output bounding live in core/tools.
//
// # Stream semantics
//
// loops.Stream is a run-level lifecycle stream, not a decorator or alias of
// models.Stream: it emits run_start / model_start / model_event / model_end /
// tool_start / tool_end / run_end events and owns the model-tool loop.
//
// Ownership and copying: defensive copies happen at exactly three boundaries:
//
//   - the request entering Model.Stream,
//   - events and results leaving Stream,
//   - tool definitions snapshotted at Runner construction.
//
// Inside a run, values are borrowed under the stream lock; no other copies
// are contractual. Callers may mutate any returned value without affecting an
// in-flight or completed run.
//
// Tool business side effects occur only through Tool.Execute or an
// Activator-provided Activation.Run; Plan and Activate only validate and
// resolve resources. Unknown tools yield a model-recoverable error result;
// fatal tool, provider, and context errors terminate the run. Incomplete,
// failed, truncated, and non-refusal filtered model terminals return
// ErrModelResponse with a partial Result and are never treated as completed.
//
// # Outcome contract
//
// Every run ends in exactly one outcome; Result and Err are consistent:
//
//	err == nil && Result.Completed()      committable final answer
//	err == nil && Result.StopReason is a budget/stall reason
//	                                     controlled stop (no error)
//	err != nil (agent sentinel, *models.Error, or context error)
//	                                     failure; Result is partial
//	errors.Is(err, context.Canceled)     cancelled (caller ctx or Close)
//
// Request-level validation errors are *models.Error with kind
// ErrorInvalidRequest; Runner configuration errors are ErrInvalidConfig.
// The full table is locked by the outcome contract tests.
package loops

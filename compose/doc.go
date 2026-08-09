// Package compose combines agent.Runner with sessions.Manager into a reusable
// turn boundary: one deep transaction that loads history, runs the agent, and
// commits the transcript suffix only when the run completed successfully.
//
// Commit policy: only err == nil && result.Completed() submits the new
// transcript suffix via Manager.AppendAt (optimistic length CAS). Budget
// stops, stalls, cancellation, fatal errors, request validation failures, and
// concurrent-turn conflicts leave the stored transcript untouched.
//
// Concurrency (V1): callers must not run concurrent turns on the same session
// (UI disables input while streaming; CLI is sequential). AppendAt makes
// accidental concurrent commits fail with ErrConcurrentTurn (sessions.ErrConflict)
// instead of silently forking the transcript.
//
// Stream terminal errors: before publishing run_end, Turn commits (or decides
// not to commit), so observing that event no longer requires one extra Next.
// Turn.Err() is the singular outcome to check (inner stream error, else commit
// failure). Session() is non-nil only on successful commit. CommitErr is
// optional diagnostics only.
//
// Session creation and ID generation are the caller's responsibility. This
// package holds no cross-turn runner state; the Manager (and its Store) is the
// only process-local session state.
package compose

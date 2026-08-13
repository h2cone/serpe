package loops

import (
	"context"
	"errors"
	"testing"
)

// TestRunEndSurvivesCloseAfterControlledStop reproduces the race in which
// Close runs after a policy stop has been decided (phaseRunEnd + stopReason)
// but before Next emits run_end. The stream contract requires run_end to still
// be delivered. This is a white-box test because the decision and the emission
// happen within a single Next call, so the window cannot be widened or timed
// through the public API.
func TestRunEndSurvivesCloseAfterControlledStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &runStateMachine{
		ctx:    ctx,
		cancel: cancel,
		phase:  phaseRunStart,
	}
	// Place the stream in the controlled-stop-ready state a completed or
	// budget-limited run reaches: a policy stop is decided (phaseRunEnd +
	// stopReason) but run_end has not yet been emitted.
	s.record.setStopReason(StopCompleted)
	s.phase = phaseRunEnd

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("Close did not cancel the run context")
	}

	var last EventKind
	for s.Next() {
		last = s.Event().Kind
	}
	if last != EventRunEnd {
		t.Fatalf("last event=%q, want run_end (Close suppressed a controlled stop)", last)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("Err=%v, want nil for a controlled stop", err)
	}
	if got := s.Result().StopReason; got != StopCompleted {
		t.Fatalf("StopReason=%s, want completed", got)
	}
}

// TestCloseMidRunSuppressesRunEnd is the complement: a Close with no decided
// policy stop is a cancellation, which must NOT synthesize run_end and must
// surface a cancelled result.
func TestCloseMidRunSuppressesRunEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &runStateMachine{
		ctx:    ctx,
		cancel: cancel,
		phase:  phaseRunStart,
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var last EventKind
	for s.Next() {
		last = s.Event().Kind
	}
	if last == EventRunEnd {
		t.Fatal("Close mid-run synthesized run_end")
	}
	if err := s.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err=%v, want context.Canceled", err)
	}
	if got := s.Result().StopReason; got != StopCancelled {
		t.Fatalf("StopReason=%s, want cancelled", got)
	}
}

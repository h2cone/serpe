package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/h2cone/ouro/core/models"
)

// Stream is a synchronous, pull-based sequence of run-level events. Controlled
// outcomes emit run_end before Next reports exhaustion; failures expose Err
// without synthesizing run_end. A concurrent Close cannot suppress the run_end
// of an already-decided controlled stop. Result returns a defensive snapshot
// at any time. A stream has one reader. Close is idempotent and may run
// concurrently with Next.
type Stream interface {
	Next() bool
	Event() Event
	Err() error
	Result() *Result
	Close() error
}

type phase int

// phase orders one run as a pull state machine. The two decision phases emit
// no events: they apply policy decisions (terminal classification, budgets,
// stall detection) and schedule the next step.
const (
	phaseRunStart phase = iota
	phaseModelStart
	phaseModelStream
	phaseModelEnd
	phaseDecideAfterModel
	phaseToolStart
	phaseToolExec
	phaseAfterTools
	phaseRunEnd
	phaseDone
)

// runStream is the pull scheduler. It owns the phase order, the lock
// protocol, Close, and the events published to the caller. Run policy
// (terminal classification, tool choice, budgets, stall detection) lives in
// policy.go and run_state.go; this type applies decisions, never re-derives
// them.
//
// Lock protocol invariants:
//   - mu guards every mutable field; runner, ctx, and cancel are immutable
//     for the stream's lifetime.
//   - No blocking I/O (model Stream calls, Tool.Execute) runs under mu.
//   - Close may run concurrently with Next. Close cancels ctx and closes the
//     active model stream through turn; steps re-check closed before
//     publishing events.
//   - stopReason is written only through runRecord.setStopReason; the first
//     writer wins so Close cannot overwrite a policy stop.
type runStream struct {
	runner *Runner
	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	conversation *conversation
	record       runRecord
	budget       runBudget
	ledger       callLedger
	// current is the event most recently produced by step. Its pointer
	// payloads (Response, ToolCall, ToolOutput) are borrowed from the run
	// record or from stack locals that escape to the heap; they are detached
	// from mutable state only when Event returns a defensive clone. Readers
	// must go through Event rather than reading current directly.
	current      Event
	err          error
	closed       bool
	finished     bool
	phase        phase

	turn           modelTurnHandle
	turnToolChoice models.ToolChoice
	batch          *toolBatch
}

func (s *runStream) Next() bool {
	s.mu.Lock()
	// A controlled stop that has been decided (phaseRunEnd/phaseDone) must
	// still emit its run_end even if a concurrent Close set closed/finished;
	// only failures and non-terminal closes short-circuit here.
	ready := s.phase == phaseRunEnd || s.phase == phaseDone
	if s.err != nil || ((s.finished || s.closed) && !ready) {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	for {
		if err := s.ctx.Err(); err != nil && !s.isControlledStopReady() {
			s.fail(err)
			return false
		}

		event, cont, err := s.step()
		if err != nil {
			s.fail(err)
			return false
		}
		if cont {
			continue
		}
		if event == nil {
			s.mu.Lock()
			s.finished = true
			s.mu.Unlock()
			return false
		}

		s.mu.Lock()
		// Suppress non-terminal events after Close, but always deliver the
		// terminal run_end of a controlled stop (see the top-of-Next guard).
		if s.closed && event.Kind != EventRunEnd {
			s.mu.Unlock()
			return false
		}
		s.current = *event
		if s.phase == phaseDone {
			s.finished = true
		}
		s.mu.Unlock()
		return true
	}
}

func (s *runStream) isControlledStopReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase == phaseRunEnd || s.phase == phaseDone
}

func (s *runStream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		if err != nil && !errors.Is(s.err, err) {
			s.err = errors.Join(s.err, err)
		}
		return
	}
	s.err = err
	if errors.Is(err, context.Canceled) {
		s.record.setStopReason(StopCancelled)
	} else {
		s.record.setStopReason(StopFailed)
	}
	_ = s.turn.close()
	s.finished = true
}

// step returns (event, continueWithoutEvent, err).
// When continueWithoutEvent is true, Next should call step again.
// When event is nil and continue is false, the stream is exhausted.
func (s *runStream) step() (*Event, bool, error) {
	s.mu.Lock()
	p := s.phase
	s.mu.Unlock()

	switch p {
	case phaseRunStart:
		s.mu.Lock()
		s.phase = phaseModelStart
		s.mu.Unlock()
		return newRunStartEvent(), false, nil

	case phaseModelStart:
		return s.beginModelTurn()

	case phaseModelStream:
		return s.consumeModelEvent()

	case phaseModelEnd:
		return s.emitModelEnd()

	case phaseDecideAfterModel:
		return nil, true, s.applyAfterModel()

	case phaseToolStart:
		return s.emitToolStart()

	case phaseToolExec:
		return s.executeTool()

	case phaseAfterTools:
		return nil, true, s.finishToolBatch()

	case phaseRunEnd:
		s.mu.Lock()
		reason := s.record.stopReason
		s.phase = phaseDone
		s.mu.Unlock()
		return newRunEndEvent(reason), false, nil

	case phaseDone:
		return nil, false, nil

	default:
		return nil, false, fmt.Errorf("%w: unknown phase %d", ErrInvalidModelResponse, p)
	}
}

func (s *runStream) beginModelTurn() (*Event, bool, error) {
	s.mu.Lock()
	turn, ok := s.budget.beginModelTurn()
	if !ok {
		s.record.setStopReason(StopMaxModelTurns)
		s.phase = phaseRunEnd
		s.mu.Unlock()
		return nil, true, nil
	}
	choice := resolveToolChoice(s.conversation.toolChoice(), s.budget.completedToolBatches())
	s.turnToolChoice = choice
	req := s.conversation.request(choice)
	s.mu.Unlock()

	stream, err := s.runner.model.Stream(s.ctx, req)
	if err != nil {
		if stream != nil {
			err = errors.Join(err, stream.Close())
		}
		return nil, false, err
	}
	if stream == nil {
		return nil, false, fmt.Errorf("%w: model returned a nil stream", ErrInvalidModelResponse)
	}

	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		ctxErr := s.ctx.Err()
		if ctxErr == nil {
			ctxErr = context.Canceled
		}
		s.mu.Unlock()
		if closeErr := stream.Close(); closeErr != nil {
			return nil, false, errors.Join(ctxErr, closeErr)
		}
		return nil, false, ctxErr
	}
	s.turn.attach(stream)
	s.phase = phaseModelStream
	s.mu.Unlock()

	return newModelStartEvent(turn), false, nil
}

func (s *runStream) consumeModelEvent() (*Event, bool, error) {
	s.mu.Lock()
	ms := s.turn.active()
	turn := s.budget.modelTurns
	s.mu.Unlock()

	if ms == nil {
		return nil, false, fmt.Errorf("%w: model stream missing", ErrInvalidModelResponse)
	}

	if ms.Next() {
		return newModelEvent(turn, ms.Event()), false, nil
	}
	if err := ms.Err(); err != nil {
		s.detachTurn()
		_ = ms.Close()
		return nil, false, err
	}

	resp := ms.Response()
	s.detachTurn()
	_ = ms.Close()

	if resp == nil {
		return nil, false, fmt.Errorf("%w: nil response", ErrInvalidModelResponse)
	}
	s.mu.Lock()
	owned := s.record.appendResponse(resp)
	usageErr := s.budget.observeTokens(owned.Usage)
	if usageErr == nil {
		s.phase = phaseModelEnd
	}
	s.mu.Unlock()
	if usageErr != nil {
		return nil, false, usageErr
	}

	return nil, true, nil
}

// detachTurn clears the active model turn stream under the lock so a
// concurrent close path cannot double-close the stream the caller is
// finishing. The caller closes the detached stream exactly once.
func (s *runStream) detachTurn() {
	s.mu.Lock()
	s.turn.detach()
	s.mu.Unlock()
}

func (s *runStream) emitModelEnd() (*Event, bool, error) {
	s.mu.Lock()
	turn := s.budget.modelTurns
	resp := s.record.lastResponse()
	s.phase = phaseDecideAfterModel
	s.mu.Unlock()
	return newModelEndEvent(turn, resp), false, nil
}

// applyAfterModel applies the policy decision for the finished model turn:
// commit the assistant turn, stop, or schedule tool execution. All business
// decisions come from policy.decideAfterModel and runBudget.
func (s *runStream) applyAfterModel() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	decision, err := decideAfterModel(s.record.lastResponse(), s.turnToolChoice)
	if err != nil {
		return err
	}
	if decision.action == afterModelComplete {
		s.conversation.appendAssistant(decision.assistant)
		s.record.setStopReason(StopCompleted)
		s.phase = phaseRunEnd
		return nil
	}
	calls := decision.calls

	// Validate every call before recording the assistant turn or performing a
	// side effect. IDs are unique across the entire run, and arguments must be
	// canonicalizable JSON objects for stall detection.
	if err := s.ledger.accept(calls); err != nil {
		return err
	}
	s.conversation.appendAssistant(decision.assistant)

	// Budget preflight is atomic: execute none unless the complete batch fits
	// and a subsequent model turn can consume its results.
	if reason := s.budget.stopBeforeTools(len(calls)); reason != "" {
		s.record.setStopReason(reason)
		s.phase = phaseRunEnd
		return nil
	}

	s.batch = newToolBatch(calls)
	s.phase = phaseToolStart
	return nil
}

func (s *runStream) emitToolStart() (*Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	call, ok := s.batch.current()
	if !ok {
		s.phase = phaseAfterTools
		return nil, true, nil
	}
	turn := s.budget.modelTurns
	idx := s.batch.index
	s.phase = phaseToolExec
	return newToolStartEvent(turn, idx, &call), false, nil
}

func (s *runStream) executeTool() (*Event, bool, error) {
	s.mu.Lock()
	call, ok := s.batch.current()
	if !ok {
		s.phase = phaseAfterTools
		s.mu.Unlock()
		return nil, true, nil
	}
	turn := s.budget.modelTurns
	idx := s.batch.index
	s.mu.Unlock()

	result, content, err := s.invokeTool(call)
	if err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	s.batch.append(result, content)
	s.record.appendToolOutput(content)
	s.budget.recordToolCall()
	// More tools remain → tool_start; otherwise finish batch.
	if _, more := s.batch.current(); more {
		s.phase = phaseToolStart
	} else {
		s.phase = phaseAfterTools
	}
	s.mu.Unlock()

	return newToolEndEvent(turn, idx, &call, &result), false, nil
}

func (s *runStream) invokeTool(call models.ToolCall) (ToolOutput, models.Content, error) {
	reg, ok := s.runner.tools.lookup(call.Name)
	if !ok {
		result := ErrorResult(fmt.Sprintf("unknown tool %q", call.Name))
		content, err := normalizeToolOutput(call, result)
		return result, content, err
	}
	args := append(json.RawMessage(nil), call.Arguments...)
	result, err := reg.tool.Execute(s.ctx, args)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ToolOutput{}, models.Content{}, err
		}
		return ToolOutput{}, models.Content{}, fmt.Errorf("%w: tool %q turn %d index %d: %v",
			ErrToolExecution, call.Name, s.budget.modelTurns, s.batch.index, err)
	}
	result = result.clone()
	content, err := normalizeToolOutput(call, result)
	if err != nil {
		return ToolOutput{}, models.Content{}, fmt.Errorf("%w: tool %q turn %d index %d: %v",
			ErrToolExecution, call.Name, s.budget.modelTurns, s.batch.index, err)
	}
	return result, content, nil
}

func (s *runStream) finishToolBatch() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Append one user message with all canonical tool results. conversation is
	// the only owner that mutates the transcript.
	s.conversation.appendToolResults(s.batch.contents)

	fp, err := stepFingerprint(s.batch.calls, s.batch.results)
	if err != nil {
		return err
	}
	if s.budget.recordStep(fp) {
		s.record.setStopReason(StopStalled)
		s.phase = phaseRunEnd
		return nil
	}

	// A completed batch relaxes a forced tool choice for the next turn.
	s.budget.recordToolBatch()
	s.batch = nil
	s.phase = phaseModelStart
	return nil
}

func (s *runStream) Event() Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.clone()
}

func (s *runStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *runStream) Result() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record.snapshot(s.conversation, &s.budget)
}

func (s *runStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	closeErr := s.turn.close()
	if !s.finished && s.err == nil && s.record.stopReason == "" {
		s.err = context.Canceled
		s.record.setStopReason(StopCancelled)
	}
	s.finished = true
	s.mu.Unlock()
	return closeErr
}

// modelTurnHandle owns the active model stream for the current turn. All
// access happens under runStream.mu. close is idempotent and detaches, so
// concurrent close paths (fail, Close, turn end) cannot double-close.
type modelTurnHandle struct {
	stream models.Stream
}

// attach replaces the active stream, closing any previous one.
func (h *modelTurnHandle) attach(stream models.Stream) {
	if h.stream != nil {
		_ = h.stream.Close()
	}
	h.stream = stream
}

// active returns the active stream without detaching it.
func (h *modelTurnHandle) active() models.Stream { return h.stream }

// detach clears the active stream without closing it; the caller closes the
// detached stream exactly once.
func (h *modelTurnHandle) detach() { h.stream = nil }

// close closes and detaches the active stream. It is idempotent.
func (h *modelTurnHandle) close() error {
	if h.stream == nil {
		return nil
	}
	err := h.stream.Close()
	h.stream = nil
	return err
}

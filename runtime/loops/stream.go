package loops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/internal/sessionwire"
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
	phaseToolDrain
	phaseAfterTools
	phaseRunEnd
	phaseDone
)

// runStateMachine drives one run as a pull-based state machine. The phase
// field is the state; step is the transition function. It owns the lock
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
type runStateMachine struct {
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
	current  Event
	err      error
	closed   bool
	finished bool
	phase    phase

	turn           modelTurnHandle
	turnToolChoice models.ToolChoice
	batch          toolBatchHandle
}

func (s *runStateMachine) Next() bool {
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

func (s *runStateMachine) isControlledStopReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase == phaseRunEnd || s.phase == phaseDone
}

func (s *runStateMachine) fail(err error) {
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
	_ = s.batch.close()
	s.finished = true
}

// step returns (event, continueWithoutEvent, err).
// When continueWithoutEvent is true, Next should call step again.
// When event is nil and continue is false, the stream is exhausted.
func (s *runStateMachine) step() (*Event, bool, error) {
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

	case phaseToolDrain:
		return s.drainToolBatch()

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

func (s *runStateMachine) beginModelTurn() (*Event, bool, error) {
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
	s.mu.Unlock()
	req, _, requestErr := s.runner.prepareModelRequest(s.ctx, s.conversation, choice)
	if requestErr != nil {
		return nil, false, requestErr
	}

	limits := s.runner.modelStreamLimits()
	var stream models.Stream
	var err error
	if acceptor, ok := s.runner.model.(models.StreamLimitAcceptor); ok {
		stream, err = acceptor.StreamWithLimits(s.ctx, req, limits)
	} else {
		stream, err = s.runner.model.Stream(s.ctx, req)
	}
	if err != nil {
		if stream != nil {
			err = errors.Join(err, stream.Close())
		}
		return nil, false, err
	}
	if stream == nil {
		return nil, false, fmt.Errorf("%w: model returned a nil stream", ErrInvalidModelResponse)
	}
	// Repeat the effective limits outside the model. For built-in models this
	// is defense in depth; for a trusted custom model it bounds Serpe's own
	// reducer even though the custom implementation may already have copied
	// the event before returning it.
	stream = models.NewStream(s.ctx, &runtimeModelSource{inner: stream},
		models.WithStreamLimits(limits))

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

type runtimeModelSource struct{ inner models.Stream }

func (s *runtimeModelSource) Next() (models.Event, error) {
	if s.inner.Next() {
		return s.inner.Event(), nil
	}
	if err := s.inner.Err(); err != nil {
		return models.Event{}, err
	}
	return models.Event{}, io.EOF
}

func (s *runtimeModelSource) Close() error { return s.inner.Close() }

func (s *runStateMachine) consumeModelEvent() (*Event, bool, error) {
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
		var modelErr *models.Error
		if errors.As(err, &modelErr) && modelErr.Kind == models.ErrorProtocol && modelErr.Code == "response_limit" {
			return nil, false, fmt.Errorf("%w: %w", ErrInvalidModelResponse, err)
		}
		return nil, false, err
	}

	resp := ms.Response()
	s.detachTurn()
	_ = ms.Close()

	if resp == nil {
		return nil, false, fmt.Errorf("%w: nil response", ErrInvalidModelResponse)
	}
	if assistant, messageErr := resp.AssistantMessage(0); messageErr == nil {
		size, sizeErr := sessionwire.MessageFragmentSize(assistant)
		if sizeErr != nil {
			return nil, false, fmt.Errorf("%w: assistant message cannot be encoded: %v", ErrInvalidModelResponse, sizeErr)
		}
		if size > s.runner.limits.MaxSessionMessageJSONBytes {
			return nil, false, fmt.Errorf("%w: assistant message exceeds %d JSON bytes", ErrRunLimit, s.runner.limits.MaxSessionMessageJSONBytes)
		}
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
func (s *runStateMachine) detachTurn() {
	s.mu.Lock()
	s.turn.detach()
	s.mu.Unlock()
}

func (s *runStateMachine) emitModelEnd() (*Event, bool, error) {
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
func (s *runStateMachine) applyAfterModel() error {
	s.mu.Lock()
	decision, err := decideAfterModel(s.record.lastResponse(), s.turnToolChoice)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if decision.action == afterModelComplete {
		s.conversation.appendAssistant(decision.assistant)
		s.record.setStopReason(StopCompleted)
		s.phase = phaseRunEnd
		s.mu.Unlock()
		return nil
	}
	calls := decision.calls
	if len(calls) > 1 && s.runner.capabilitiesKnown && !s.runner.capabilities.Has(models.CapabilityParallelTools) {
		s.mu.Unlock()
		return fmt.Errorf("%w: model returned parallel tool calls without capability", ErrInvalidModelResponse)
	}
	var argumentContextBytes int64
	for i := range calls {
		next, ok := safeAddBytes(argumentContextBytes, int64(len(calls[i].Arguments)))
		if !ok || next > s.runner.limits.Context.MaxToolCallArgumentContextBytes {
			s.mu.Unlock()
			return fmt.Errorf("%w: current tool arguments exceed request context budget", ErrRunLimit)
		}
		argumentContextBytes = next
	}
	assistantBytes, err := sessionwire.MessageFragmentSize(decision.assistant)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: assistant message cannot be encoded", ErrInvalidModelResponse)
	}
	_, minimumResultBytes, err := minimumToolResultMessage(calls)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: cannot frame tool results", ErrInvalidModelResponse)
	}
	fixedReserve, ok := safeAddBytes(minimumResultBytes, int64(len(calls))*(1<<10))
	if !ok || fixedReserve > s.runner.limits.MaxSessionMessageJSONBytes {
		s.mu.Unlock()
		return fmt.Errorf("%w: tool result message cannot fit session message ceiling", ErrRunLimit)
	}
	retainedRemaining, canonicalRemaining := s.budget.remainingToolBytes()
	minimumGroup, ok := safeAddBytes(assistantBytes, fixedReserve)
	if !ok || minimumGroup > retainedRemaining || minimumGroup > canonicalRemaining {
		s.mu.Unlock()
		return fmt.Errorf("%w: tool exchange cannot fit remaining run budget", ErrRunLimit)
	}
	if err := s.ledger.accept(calls); err != nil {
		s.mu.Unlock()
		return err
	}
	if reason := s.budget.stopBeforeTools(len(calls)); reason != "" {
		s.conversation.appendAssistant(decision.assistant)
		s.record.setStopReason(reason)
		s.phase = phaseRunEnd
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if s.runner.tools == nil {
		return fmt.Errorf("%w: model returned tool calls but no executor is configured", ErrInvalidModelResponse)
	}
	_, executorOutput := s.runner.tools.Limits()
	rawQuota := (s.runner.limits.MaxSessionMessageJSONBytes - fixedReserve) / 6
	if remaining := retainedRemaining - minimumGroup; rawQuota > remaining {
		rawQuota = remaining
	}
	if remaining := canonicalRemaining - minimumGroup; rawQuota > remaining {
		rawQuota = remaining
	}
	if rawQuota > executorOutput.MaxBatchFramedBytes {
		rawQuota = executorOutput.MaxBatchFramedBytes
	}
	minimumRaw := int64(len(calls)) * (1 << 10)
	if s.runner.requestBudget != nil {
		nextChoice := resolveToolChoice(s.conversation.toolChoice(), s.budget.completedToolBatches()+1)
		skeleton := providerSkeletonToolResultMessage(calls)
		_, skeletonSize, projectionErr := s.runner.prepareModelRequest(s.ctx, s.conversation, nextChoice, decision.assistant, skeleton)
		if projectionErr != nil {
			return projectionErr
		}
		headroom := s.runner.maxEncodedRequestBytes - skeletonSize
		providerQuota, ok := safeAddBytes(minimumRaw, headroom/6)
		if !ok {
			return fmt.Errorf("%w: provider continuation quota overflow", ErrRunLimit)
		}
		if rawQuota > providerQuota {
			rawQuota = providerQuota
		}
	}
	if rawQuota < minimumRaw {
		return fmt.Errorf("%w: insufficient quota for fixed tool results", ErrRunLimit)
	}
	batch, err := s.runner.tools.Start(s.ctx, calls, tools.WithMaxBatchFramedBytes(rawQuota))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrToolExecution, err)
	}
	s.mu.Lock()
	s.conversation.appendAssistant(decision.assistant)
	s.batch = toolBatchHandle{
		batch: batch, calls: append([]models.ToolCall(nil), calls...),
		adapted: make([]tools.Output, len(calls)), adaptedSet: make([]bool, len(calls)),
		assistantBytes: assistantBytes,
	}
	s.phase = phaseToolDrain
	s.mu.Unlock()
	return nil
}

func (s *runStateMachine) drainToolBatch() (*Event, bool, error) {
	if s.batch.batch == nil {
		s.mu.Lock()
		s.phase = phaseAfterTools
		s.mu.Unlock()
		return nil, true, nil
	}
	if !s.batch.batch.Next() {
		if err := s.batch.batch.Err(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, false, err
			}
			return nil, false, fmt.Errorf("%w: %v", ErrToolExecution, err)
		}
		s.mu.Lock()
		s.phase = phaseAfterTools
		s.mu.Unlock()
		return nil, true, nil
	}
	ev := s.batch.batch.Event()
	s.mu.Lock()
	turn := s.budget.modelTurns
	s.mu.Unlock()
	call := ev.Call
	switch ev.Kind {
	case tools.BatchStarted:
		return newToolStartEvent(turn, ev.Index, &call), false, nil
	case tools.BatchFinished:
		if ev.Output == nil {
			return nil, false, fmt.Errorf("%w: missing tool output", ErrToolExecution)
		}
		out, err := s.runner.adaptToolOutput(*ev.Output)
		if err != nil {
			_ = s.commitCompletedTools()
			return nil, false, fmt.Errorf("%w: tool image adaptation: %w", ErrToolExecution, err)
		}
		if ev.Index < 0 || ev.Index >= len(s.batch.adapted) {
			return nil, false, fmt.Errorf("%w: invalid finished tool index %d", ErrToolExecution, ev.Index)
		}
		s.mu.Lock()
		s.batch.adapted[ev.Index] = out
		s.batch.adaptedSet[ev.Index] = true
		s.mu.Unlock()
		return newToolEndEvent(turn, ev.Index, &call, &out), false, nil
	case tools.BatchFailed:
		if err := s.commitCompletedTools(); err != nil {
			return nil, false, err
		}
		if ev.Err != nil && (errors.Is(ev.Err, context.Canceled) || errors.Is(ev.Err, context.DeadlineExceeded)) {
			return nil, false, ev.Err
		}
		return nil, false, fmt.Errorf("%w: tool %q turn %d index %d: %v",
			ErrToolExecution, call.Name, turn, ev.Index, ev.Err)
	default:
		return nil, false, fmt.Errorf("%w: unknown batch event", ErrToolExecution)
	}
}

func (s *runStateMachine) commitCompletedTools() error {
	if s.batch.batch == nil {
		return nil
	}
	results := s.batch.batch.Results()
	for i, result := range results {
		if result.State != tools.ResultFinished || result.Output == nil {
			continue
		}
		s.mu.Lock()
		alreadyAdapted := i < len(s.batch.adaptedSet) && s.batch.adaptedSet[i]
		s.mu.Unlock()
		if alreadyAdapted {
			continue
		}
		adapted, err := s.runner.adaptToolOutput(*result.Output)
		if err != nil {
			return fmt.Errorf("%w: tool image adaptation: %w", ErrToolExecution, err)
		}
		s.mu.Lock()
		if i >= len(s.batch.adapted) {
			s.mu.Unlock()
			return fmt.Errorf("%w: invalid completed tool index %d", ErrToolExecution, i)
		}
		s.batch.adapted[i] = adapted
		s.batch.adaptedSet[i] = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.batch.contents != nil {
		return nil
	}
	s.batch.contents = make([]models.Content, 0, len(results))
	s.batch.results = make([]tools.Output, 0, len(results))
	for i, r := range results {
		if r.State != tools.ResultFinished || r.Output == nil || i >= len(s.batch.adaptedSet) || !s.batch.adaptedSet[i] {
			continue
		}
		call := s.batch.calls[i]
		adapted := s.batch.adapted[i]
		content, err := normalizeToolOutput(call, adapted)
		if err != nil {
			return fmt.Errorf("%w: invalid adapted tool output: %w", ErrToolExecution, err)
		}
		s.batch.contents = append(s.batch.contents, content)
		s.batch.results = append(s.batch.results, adapted)
		s.record.appendToolOutput(content)
		s.budget.recordToolCall()
	}
	return nil
}

func (s *runStateMachine) finishToolBatch() error {
	if err := s.commitCompletedTools(); err != nil {
		return err
	}
	s.mu.Lock()
	resultMessage := models.NewUserMessage(s.batch.contents...)
	resultBytes, err := sessionwire.MessageFragmentSize(resultMessage)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: final tool result message cannot be encoded", ErrInvalidModelResponse)
	}
	if resultBytes > s.runner.limits.MaxSessionMessageJSONBytes {
		s.mu.Unlock()
		return fmt.Errorf("%w: final tool result exceeds session message ceiling", ErrRunLimit)
	}
	retained, ok := safeAddBytes(s.batch.assistantBytes, resultBytes)
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: tool exchange size overflow", ErrRunLimit)
	}
	assistant := s.conversation.messages[len(s.conversation.messages)-1]
	canonical, err := canonicalToolExchangeBytes(assistant, resultMessage)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	retainedRemaining, canonicalRemaining := s.budget.remainingToolBytes()
	if retained > retainedRemaining || canonical > canonicalRemaining {
		s.mu.Unlock()
		return fmt.Errorf("%w: finalized tool exchange exceeded its reserved budget", ErrRunLimit)
	}
	fp, err := stepFingerprint(s.batch.calls, s.batch.results)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	nextChoice := resolveToolChoice(s.conversation.toolChoice(), s.budget.completedToolBatches()+1)
	s.mu.Unlock()
	_, _, projectionErr := s.runner.prepareModelRequest(s.ctx, s.conversation, nextChoice, resultMessage)
	if projectionErr != nil {
		return projectionErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.budget.recordToolBytes(retained, canonical)

	// Append one user message with all canonical tool results. conversation is
	// the only owner that mutates the transcript.
	s.conversation.messages = append(s.conversation.messages, resultMessage)

	if s.budget.recordStep(fp) {
		s.record.setStopReason(StopStalled)
		s.phase = phaseRunEnd
		return nil
	}

	// A completed batch relaxes a forced tool choice for the next turn.
	s.budget.recordToolBatch()
	_ = s.batch.close()
	s.batch = toolBatchHandle{}
	s.phase = phaseModelStart
	return nil
}

func (s *runStateMachine) Event() Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.clone()
}

func (s *runStateMachine) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *runStateMachine) Result() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record.snapshot(s.conversation, &s.budget)
}

func (s *runStateMachine) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	closeErr := s.turn.close()
	if batchErr := s.batch.close(); closeErr == nil {
		closeErr = batchErr
	}
	if !s.finished && s.err == nil && s.record.stopReason == "" {
		s.err = context.Canceled
		s.record.setStopReason(StopCancelled)
	}
	s.finished = true
	s.mu.Unlock()
	return closeErr
}

// modelTurnHandle owns the active model stream for the current turn. All
// access happens under runStateMachine.mu. close is idempotent and detaches, so
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

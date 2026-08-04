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
// without synthesizing run_end. Result returns a defensive snapshot at any
// time. A stream has one reader. Close is idempotent and may run concurrently
// with Next.
type Stream interface {
	Next() bool
	Event() Event
	Err() error
	Result() *Result
	Close() error
}

type phase int

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

type runStream struct {
	runner *Runner
	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	conversation *conversation
	record       runRecord
	budget       runBudget
	ledger       callLedger
	current      Event
	err          error
	closed       bool
	finished     bool
	phase        phase

	forcedRelaxed  bool
	turnToolChoice models.ToolChoice
	batch          *toolBatch

	modelStream models.Stream
}

func (s *runStream) Next() bool {
	s.mu.Lock()
	if s.finished || s.err != nil || s.closed {
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
		if s.closed {
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
	if s.record.stopReason == "" {
		if errors.Is(err, context.Canceled) {
			s.record.stopReason = StopCancelled
		} else {
			s.record.stopReason = StopFailed
		}
	}
	if s.modelStream != nil {
		_ = s.modelStream.Close()
		s.modelStream = nil
	}
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
		return &Event{Kind: EventRunStart}, false, nil

	case phaseModelStart:
		return s.beginModelTurn()

	case phaseModelStream:
		return s.consumeModelEvent()

	case phaseModelEnd:
		return s.emitModelEnd()

	case phaseDecideAfterModel:
		return nil, true, s.decideAfterModel()

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
		return &Event{Kind: EventRunEnd, StopReason: reason}, false, nil

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
		s.record.stopReason = StopMaxModelTurns
		s.phase = phaseRunEnd
		s.mu.Unlock()
		return nil, true, nil
	}
	choice := s.conversation.toolChoice()
	// After the first tool batch, forced tool choice is relaxed.
	if s.forcedRelaxed && choice.Kind != models.ToolChoiceNone {
		choice = models.ToolChoice{Kind: models.ToolChoiceAuto}
	}
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
	s.modelStream = stream
	s.phase = phaseModelStream
	s.mu.Unlock()

	return &Event{Kind: EventModelStart, ModelTurn: turn}, false, nil
}

func (s *runStream) consumeModelEvent() (*Event, bool, error) {
	s.mu.Lock()
	ms := s.modelStream
	turn := s.budget.modelTurns
	s.mu.Unlock()

	if ms == nil {
		return nil, false, fmt.Errorf("%w: model stream missing", ErrInvalidModelResponse)
	}

	if ms.Next() {
		ev := ms.Event()
		return &Event{Kind: EventModel, ModelTurn: turn, Model: ev}, false, nil
	}
	if err := ms.Err(); err != nil {
		_ = ms.Close()
		s.mu.Lock()
		s.modelStream = nil
		s.mu.Unlock()
		return nil, false, err
	}

	resp := ms.Response()
	_ = ms.Close()

	if resp == nil {
		s.mu.Lock()
		s.modelStream = nil
		s.mu.Unlock()
		return nil, false, fmt.Errorf("%w: nil response", ErrInvalidModelResponse)
	}
	s.mu.Lock()
	s.modelStream = nil
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

func (s *runStream) emitModelEnd() (*Event, bool, error) {
	s.mu.Lock()
	turn := s.budget.modelTurns
	resp := s.record.lastResponse()
	s.phase = phaseDecideAfterModel
	s.mu.Unlock()
	return &Event{Kind: EventModelEnd, ModelTurn: turn, Response: resp}, false, nil
}

func (s *runStream) decideAfterModel() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	classified, err := classifyModelResponse(s.record.lastResponse())
	if err != nil {
		return err
	}
	if err := validateToolChoice(s.turnToolChoice, classified.calls); err != nil {
		return err
	}
	if classified.action == afterModelComplete {
		s.conversation.appendAssistant(classified.assistant)
		s.record.stopReason = StopCompleted
		s.phase = phaseRunEnd
		return nil
	}
	calls := classified.calls

	// Validate every call before recording the assistant turn or performing a
	// side effect. IDs are unique across the entire run, and arguments must be
	// canonicalizable JSON objects for stall detection.
	if err := s.ledger.accept(calls); err != nil {
		return err
	}
	s.conversation.appendAssistant(classified.assistant)

	// Budget preflight is atomic: execute none unless the complete batch fits
	// and a subsequent model turn can consume its results.
	if reason := s.budget.stopBeforeTools(len(calls)); reason != "" {
		s.record.stopReason = reason
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
	return &Event{
		Kind:      EventToolStart,
		ModelTurn: turn,
		ToolIndex: idx,
		ToolCall:  &call,
	}, false, nil
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
	s.record.appendToolResult(content)
	s.budget.recordToolCall()
	// More tools remain → tool_start; otherwise finish batch.
	if _, more := s.batch.current(); more {
		s.phase = phaseToolStart
	} else {
		s.phase = phaseAfterTools
	}
	s.mu.Unlock()

	return &Event{
		Kind:       EventToolEnd,
		ModelTurn:  turn,
		ToolIndex:  idx,
		ToolCall:   &call,
		ToolResult: &result,
	}, false, nil
}

func (s *runStream) invokeTool(call models.ToolCall) (ToolResult, models.Content, error) {
	reg, ok := s.runner.lookupTool(call.Name)
	if !ok {
		result := ErrorResult(fmt.Sprintf("unknown tool %q", call.Name))
		content, err := normalizeToolOutput(call, result)
		return result, content, err
	}
	args := append(json.RawMessage(nil), call.Arguments...)
	result, err := reg.tool.Execute(s.ctx, args)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ToolResult{}, models.Content{}, err
		}
		return ToolResult{}, models.Content{}, fmt.Errorf("%w: tool %q turn %d index %d: %v",
			ErrToolExecution, call.Name, s.budget.modelTurns, s.batch.index, err)
	}
	result = result.clone()
	content, err := normalizeToolOutput(call, result)
	if err != nil {
		return ToolResult{}, models.Content{}, fmt.Errorf("%w: tool %q turn %d index %d: %v",
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
		s.record.stopReason = StopStalled
		s.phase = phaseRunEnd
		return nil
	}

	// Relax forced tool choice after the first completed tool batch.
	s.forcedRelaxed = true
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
	var closeErr error
	if s.modelStream != nil {
		closeErr = s.modelStream.Close()
		s.modelStream = nil
	}
	if !s.finished && s.err == nil && s.record.stopReason == "" {
		s.err = context.Canceled
		s.record.stopReason = StopCancelled
	}
	s.finished = true
	s.mu.Unlock()
	return closeErr
}

func candidateZero(resp *models.Response) (models.Candidate, bool) {
	if resp == nil {
		return models.Candidate{}, false
	}
	for i := range resp.Candidates {
		if resp.Candidates[i].Index == 0 {
			return resp.Candidates[i], true
		}
	}
	return models.Candidate{}, false
}

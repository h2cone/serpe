package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
)

// BatchEventKind classifies one pull event from a Batch.
type BatchEventKind uint8

const (
	BatchStarted BatchEventKind = iota + 1
	BatchFinished
	BatchFailed
)

// BatchEvent is one start, finish, or fail notice.
type BatchEvent struct {
	Kind     BatchEventKind
	Index    int
	Call     models.ToolCall
	Executed bool
	Output   *Output
	Err      error
}

// BatchResultState is the per-index terminal or in-flight state.
type BatchResultState uint8

const (
	ResultPending BatchResultState = iota
	ResultRunning
	ResultFinished
	ResultFailed
	ResultSkipped
)

// BatchResult is the per-index outcome. Results() is always call-ordered.
type BatchResult struct {
	State  BatchResultState
	Output *Output
	Err    error
}

type preparedCall struct {
	index        int
	call         models.ToolCall
	tool         registered
	limits       OutputLimits
	static       []Claim
	rejected     *Output
	hasActivator bool
	ticket       uint64
}

// Batch is a pull stream over one planned tool batch.
type Batch struct {
	exec   *Executor
	parent context.Context
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	started bool
	closed  bool
	slots   []BatchResult
	calls   []preparedCall
	current BatchEvent
	err     error
	fatals  []batchFatal

	events chan BatchEvent
	wg     sync.WaitGroup
}

type batchFatal struct {
	index int
	err   error
}

// Start preflights a batch and returns a Batch that is not yet registered
// with the coordinator. The first Next assigns tickets and starts work.
func (e *Executor) Start(ctx context.Context, calls []models.ToolCall, opts ...StartOption) (*Batch, error) {
	if e == nil {
		return nil, wrapExecution("nil executor")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if executorFrom(ctx) == e {
		return nil, wrapExecution("reentrant Start on the same executor")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkEnvelope(calls, e.input); err != nil {
		return nil, err
	}
	limits, err := applyStartOptions(opts, e.output, len(calls))
	if err != nil {
		return nil, err
	}
	quotas, err := allocateCallQuotas(limits, len(calls))
	if err != nil {
		return nil, err
	}
	copied := make([]models.ToolCall, len(calls))
	for i := range calls {
		copied[i] = models.ToolCall{
			ID:        calls[i].ID,
			Name:      calls[i].Name,
			Arguments: append(json.RawMessage(nil), calls[i].Arguments...),
		}
	}
	scope := scopeFrom(ctx)
	prepared := make([]preparedCall, len(copied))
	for i, call := range copied {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pc := preparedCall{index: i, call: call, limits: quotas[i]}
		if !utf8.ValidString(call.Name) || !validToolName(call.Name) {
			out := e.recoverable(pc.limits, fmt.Sprintf("unknown tool %s", sanitizeDiagnostic(call.Name, 256)))
			pc.rejected = &out
			prepared[i] = pc
			continue
		}
		reg, ok := e.lookup(call.Name)
		if !ok {
			out := e.recoverable(pc.limits, fmt.Sprintf("unknown tool %s", sanitizeDiagnostic(call.Name, 256)))
			pc.rejected = &out
			prepared[i] = pc
			continue
		}
		pc.tool = reg
		args, err := parseArguments(ctx, call.Arguments)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			out := e.recoverable(pc.limits, "invalid tool arguments")
			pc.rejected = &out
			prepared[i] = pc
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := reg.schema.validate(ctx, args, &evalBudget{}); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			msg := "arguments do not match tool schema"
			if err.Error() == "schema evaluation budget exceeded" {
				msg = err.Error()
			}
			out := e.recoverable(pc.limits, msg)
			pc.rejected = &out
			prepared[i] = pc
			continue
		}
		inv := Invocation{Arguments: append(json.RawMessage(nil), call.Arguments...), Scope: scope, OutputLimits: pc.limits}
		if reg.planner != nil {
			plan, err := e.callPlan(ctx, reg, inv)
			if err != nil {
				if callErr, ok := asCallError(err); ok {
					out := e.recoverable(pc.limits, callErr.Message)
					pc.rejected = &out
					prepared[i] = pc
					continue
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				if !errors.Is(err, ErrExecution) {
					err = fmt.Errorf("%w: Plan: %w", ErrExecution, err)
				}
				return nil, err
			}
			claims, err := normalizeClaims(append([]Claim{rootRead()}, plan.Claims...))
			if err != nil {
				return nil, err
			}
			pc.static = claims
			pc.hasActivator = reg.activator != nil
		} else {
			pc.static = []Claim{rootWrite()}
		}
		prepared[i] = pc
	}
	runCtx, cancel := context.WithCancel(ctx)
	b := &Batch{
		exec:   e,
		parent: ctx,
		ctx:    runCtx,
		cancel: cancel,
		calls:  prepared,
		slots:  make([]BatchResult, len(prepared)),
		events: make(chan BatchEvent, 2*len(prepared)+1),
	}
	return b, nil
}

func (e *Executor) callPlan(ctx context.Context, reg registered, inv Invocation) (plan Plan, err error) {
	if err := e.coord.acquirePermit(ctx); err != nil {
		return Plan{}, err
	}
	defer e.coord.releasePermit()
	ctx = withExecutor(ctx, e)
	defer func() {
		if rec := recover(); rec != nil {
			err = wrapExecution("Plan panic: %v", rec)
		}
	}()
	plan, err = reg.planner.Plan(ctx, cloneInvocation(inv))
	return plan, err
}

func (e *Executor) recoverable(limits OutputLimits, msg string) Output {
	out, err := e.finalize(Error(sanitizeDiagnostic(msg, maxCallErrorBytes)), limits, true)
	if err != nil {
		return Error(budgetErrorText)
	}
	return out
}

// Next reports whether an event is available.
func (b *Batch) Next() bool {
	b.mu.Lock()
	if !b.started {
		if b.closed {
			b.mu.Unlock()
			return false
		}
		if err := b.ctx.Err(); err != nil {
			b.skipAllLocked()
			b.err = err
			b.started = true
			close(b.events)
			b.mu.Unlock()
			return false
		}
		b.started = true
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.dispatch()
		}()
	}
	b.mu.Unlock()
	ev, ok := <-b.events
	if !ok {
		return false
	}
	b.mu.Lock()
	b.current = ev
	b.mu.Unlock()
	return true
}

func (b *Batch) dispatch() {
	tickets, err := b.exec.coord.registerBatch(b.calls)
	if err != nil {
		b.mu.Lock()
		b.err = err
		b.skipAllLocked()
		b.mu.Unlock()
		close(b.events)
		return
	}
	for i := range b.calls {
		b.calls[i].ticket = tickets[i]
	}
	var workers sync.WaitGroup
	for i := range b.calls {
		pc := b.calls[i]
		if pc.rejected != nil {
			b.emitRejected(pc)
			continue
		}
		workers.Add(1)
		go func(pc preparedCall) {
			defer workers.Done()
			b.runCall(pc)
		}(pc)
	}
	workers.Wait()
	b.mu.Lock()
	if parentErr := b.parent.Err(); parentErr != nil {
		b.err = joinBatchErrors(parentErr, b.fatals)
	} else if b.err == nil && len(b.fatals) > 0 {
		b.err = pickFatal(b.fatals)
	}
	for i := range b.slots {
		if b.slots[i].State == ResultPending {
			b.slots[i] = BatchResult{State: ResultSkipped}
		}
	}
	b.mu.Unlock()
	close(b.events)
}

func pickFatal(fatals []batchFatal) error {
	chosen := -1
	for i, fatal := range fatals {
		if fatal.err == nil || errors.Is(fatal.err, context.Canceled) || errors.Is(fatal.err, context.DeadlineExceeded) {
			continue
		}
		if chosen < 0 || fatal.index < fatals[chosen].index {
			chosen = i
		}
	}
	if chosen < 0 {
		for i, fatal := range fatals {
			if fatal.err != nil && (chosen < 0 || fatal.index < fatals[chosen].index) {
				chosen = i
			}
		}
	}
	if chosen < 0 {
		return nil
	}
	errs := make([]error, 0, len(fatals))
	errs = append(errs, fatals[chosen].err)
	for i, fatal := range fatals {
		if i != chosen && fatal.err != nil {
			errs = append(errs, fatal.err)
		}
	}
	return errors.Join(errs...)
}

func joinBatchErrors(first error, fatals []batchFatal) error {
	errs := []error{first}
	for _, fatal := range fatals {
		if fatal.err != nil {
			errs = append(errs, fatal.err)
		}
	}
	return errors.Join(errs...)
}

func (b *Batch) emitRejected(pc preparedCall) {
	b.setSlot(pc.index, BatchResult{State: ResultFinished, Output: clonePtr(pc.rejected)})
	b.send(BatchEvent{Kind: BatchStarted, Index: pc.index, Call: cloneCall(pc.call), Executed: false})
	out := cloneOutput(*pc.rejected)
	b.send(BatchEvent{Kind: BatchFinished, Index: pc.index, Call: cloneCall(pc.call), Output: &out})
}

func (b *Batch) runCall(pc preparedCall) {
	if b.ctx.Err() != nil {
		b.setSlot(pc.index, BatchResult{State: ResultSkipped})
		b.exec.coord.release(pc.ticket)
		return
	}
	if err := b.exec.coord.acquire(b.ctx, pc.ticket, pc.static); err != nil {
		b.setSlot(pc.index, BatchResult{State: ResultSkipped})
		b.exec.coord.release(pc.ticket)
		return
	}
	inv := Invocation{
		Arguments:    append(json.RawMessage(nil), pc.call.Arguments...),
		Scope:        scopeFrom(b.ctx),
		OutputLimits: pc.limits,
	}
	ctx := withExecutor(b.ctx, b.exec)
	var started bool
	var out Output
	var runErr error
	if pc.hasActivator {
		act, activateErr := b.activate(ctx, pc, inv)
		runErr = activateErr
		if runErr != nil {
			if callErr, ok := asCallError(runErr); ok {
				rej := b.exec.recoverable(pc.limits, callErr.Message)
				pc.rejected = &rej
				b.emitRejected(pc)
				b.exec.coord.release(pc.ticket)
				return
			}
			if (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) && b.ctx.Err() != nil {
				b.cancelled(pc, false, runErr)
				return
			}
			if !errors.Is(runErr, ErrExecution) {
				runErr = fmt.Errorf("%w: Activate: %w", ErrExecution, runErr)
			}
			b.fail(pc, false, runErr)
			return
		}
		closed := false
		closeActivation := func() error {
			if closed || act.Close == nil {
				closed = true
				return nil
			}
			closed = true
			return safeClose(act.Close)
		}
		if act.Run == nil {
			runErr = wrapExecution("Activation.Run is nil")
			if closeErr := closeActivation(); closeErr != nil {
				runErr = errors.Join(runErr, closeErr)
			}
			b.fail(pc, false, runErr)
			return
		}
		finalClaims, err := extendClaims(pc.static, act.Claims)
		if err != nil {
			if closeErr := closeActivation(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			b.fail(pc, false, err)
			return
		}
		if err := b.exec.coord.discover(pc.ticket, finalClaims); err != nil {
			if closeErr := closeActivation(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			b.fail(pc, false, err)
			return
		}
		if err := b.exec.coord.extend(ctx, pc.ticket, finalClaims); err != nil {
			if closeErr := closeActivation(); closeErr != nil {
				b.fail(pc, false, errors.Join(err, closeErr))
				return
			}
			b.cancelled(pc, false, err)
			return
		}
		started = true
		b.setSlot(pc.index, BatchResult{State: ResultRunning})
		b.send(BatchEvent{Kind: BatchStarted, Index: pc.index, Call: cloneCall(pc.call), Executed: true})
		out, runErr = safeRun(act.Run, ctx)
		if closeErr := closeActivation(); closeErr != nil {
			if runErr != nil {
				runErr = errors.Join(runErr, closeErr)
			} else {
				runErr = closeErr
			}
		}
	} else {
		started = true
		b.setSlot(pc.index, BatchResult{State: ResultRunning})
		b.send(BatchEvent{Kind: BatchStarted, Index: pc.index, Call: cloneCall(pc.call), Executed: true})
		out, runErr = b.execute(ctx, pc, inv)
	}
	if runErr != nil {
		if (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) && b.isClosing() {
			b.cancelled(pc, started, runErr)
			return
		}
		if !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) && !errors.Is(runErr, ErrExecution) {
			runErr = fmt.Errorf("%w: %w", ErrExecution, runErr)
		}
		b.fail(pc, started, runErr)
		return
	}
	final, err := b.exec.finalize(out, pc.limits, false)
	if err != nil {
		if errors.Is(err, ErrOutputLimit) {
			final, err = b.exec.budgetOutput(pc.limits, out.IsError)
		}
		if err != nil {
			b.fail(pc, started, err)
			return
		}
	}
	b.setSlot(pc.index, BatchResult{State: ResultFinished, Output: clonePtr(&final)})
	b.send(BatchEvent{Kind: BatchFinished, Index: pc.index, Call: cloneCall(pc.call), Output: clonePtr(&final)})
	b.exec.coord.release(pc.ticket)
}

func (b *Batch) activate(ctx context.Context, pc preparedCall, inv Invocation) (act Activation, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = wrapExecution("Activate panic: %v", rec)
		}
	}()
	return pc.tool.activator.Activate(ctx, cloneInvocation(inv))
}

func (b *Batch) execute(ctx context.Context, pc preparedCall, inv Invocation) (out Output, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = wrapExecution("Execute panic: %v", rec)
		}
	}()
	return pc.tool.tool.Execute(ctx, cloneInvocation(inv))
}

func safeRun(run func(context.Context) (Output, error), ctx context.Context) (out Output, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = wrapExecution("Run panic: %v", rec)
		}
	}()
	return run(ctx)
}

func safeClose(fn func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = wrapExecution("Close panic: %v", rec)
		}
	}()
	return fn()
}

func (b *Batch) fail(pc preparedCall, started bool, err error) {
	b.cancel()
	b.mu.Lock()
	b.fatals = append(b.fatals, batchFatal{index: pc.index, err: err})
	b.mu.Unlock()
	b.setSlot(pc.index, BatchResult{State: ResultFailed, Err: err})
	b.send(BatchEvent{Kind: BatchFailed, Index: pc.index, Call: cloneCall(pc.call), Err: err})
	b.exec.coord.release(pc.ticket)
}

func (b *Batch) cancelled(pc preparedCall, started bool, err error) {
	parentCancelled := b.parent.Err() != nil
	if parentCancelled {
		b.mu.Lock()
		b.fatals = append(b.fatals, batchFatal{index: pc.index, err: err})
		b.mu.Unlock()
	}
	if started && (parentCancelled || !b.isClosing()) {
		b.setSlot(pc.index, BatchResult{State: ResultFailed, Err: err})
		b.send(BatchEvent{Kind: BatchFailed, Index: pc.index, Call: cloneCall(pc.call), Err: err})
		b.exec.coord.release(pc.ticket)
		return
	}
	b.setSlot(pc.index, BatchResult{State: ResultSkipped})
	b.exec.coord.release(pc.ticket)
}

func (b *Batch) isClosing() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *Batch) send(ev BatchEvent) {
	select {
	case b.events <- ev:
	case <-b.ctx.Done():
		select {
		case b.events <- ev:
		default:
		}
	}
}

func (b *Batch) setSlot(i int, r BatchResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.slots[i] = r
}

func (b *Batch) skipAllLocked() {
	for i := range b.slots {
		if b.slots[i].State == ResultPending {
			b.slots[i] = BatchResult{State: ResultSkipped}
		}
	}
}

// Event returns a defensive copy of the current event.
func (b *Batch) Event() BatchEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	ev := b.current
	ev.Call = cloneCall(ev.Call)
	if ev.Output != nil {
		cp := cloneOutput(*ev.Output)
		ev.Output = &cp
	}
	return ev
}

// Results returns a defensive copy of per-index results.
func (b *Batch) Results() []BatchResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BatchResult, len(b.slots))
	for i := range b.slots {
		out[i] = b.slots[i]
		if b.slots[i].Output != nil {
			cp := cloneOutput(*b.slots[i].Output)
			out[i].Output = &cp
		}
	}
	return out
}

// Err returns the batch-level fatal error, if any.
func (b *Batch) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Close cancels and drains the batch. It is idempotent.
func (b *Batch) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.wg.Wait()
		return b.Err()
	}
	b.closed = true
	started := b.started
	if !started {
		b.skipAllLocked()
		b.started = true
		close(b.events)
		b.mu.Unlock()
		b.cancel()
		return nil
	}
	b.mu.Unlock()
	b.cancel()
	b.wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func cloneCall(c models.ToolCall) models.ToolCall {
	c.Arguments = append(json.RawMessage(nil), c.Arguments...)
	return c
}

func clonePtr(o *Output) *Output {
	if o == nil {
		return nil
	}
	cp := cloneOutput(*o)
	return &cp
}

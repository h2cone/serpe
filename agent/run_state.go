package agent

import (
	"fmt"

	"github.com/h2cone/ouro/core/models"
)

// runBudget is the sole owner of counters and limits used by run policy.
type runBudget struct {
	limits         Limits
	modelTurns     int
	toolCalls      int
	observedTokens int64
	tokenOverflow  bool
	identicalSteps int
	lastStep       string
}

func newRunBudget(limits Limits) runBudget { return runBudget{limits: limits} }

func (b *runBudget) beginModelTurn() (int, bool) {
	if b.modelTurns >= b.limits.MaxModelTurns {
		return b.modelTurns, false
	}
	b.modelTurns++
	return b.modelTurns, true
}

func (b *runBudget) observeTokens(usage models.Usage) error {
	if !usage.TotalTokens.Set {
		return nil
	}
	next, overflow, ok := accumulateObservedTokens(b.observedTokens, usage.TotalTokens.Value)
	if !ok {
		return fmt.Errorf("%w: negative reported total tokens", ErrInvalidModelResponse)
	}
	b.observedTokens = next
	b.tokenOverflow = b.tokenOverflow || overflow
	return nil
}

func (b *runBudget) stopBeforeTools(callCount int) StopReason {
	if b.modelTurns >= b.limits.MaxModelTurns {
		return StopMaxModelTurns
	}
	if b.limits.MaxObservedTokens > 0 && (b.tokenOverflow || b.observedTokens > b.limits.MaxObservedTokens) {
		return StopMaxObservedTokens
	}
	if callCount > b.limits.MaxToolCalls-b.toolCalls {
		return StopMaxToolCalls
	}
	return ""
}

func (b *runBudget) recordToolCall() { b.toolCalls++ }

func (b *runBudget) recordStep(fingerprint string) bool {
	if fingerprint == b.lastStep {
		b.identicalSteps++
	} else {
		b.identicalSteps = 1
		b.lastStep = fingerprint
	}
	return b.identicalSteps >= b.limits.MaxIdenticalSteps
}

// callLedger validates a complete batch before committing any call IDs.
type callLedger struct {
	seen map[string]struct{}
}

func newCallLedger() callLedger {
	return callLedger{seen: make(map[string]struct{})}
}

func (l *callLedger) accept(calls []models.ToolCall) error {
	batch := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.ID == "" {
			return fmt.Errorf("%w: empty tool call id", ErrInvalidModelResponse)
		}
		if _, err := canonicalJSONObject(call.Arguments); err != nil {
			return fmt.Errorf("%w: tool call %q has invalid arguments", ErrInvalidModelResponse, call.ID)
		}
		if _, exists := batch[call.ID]; exists {
			return fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidModelResponse, call.ID)
		}
		if _, exists := l.seen[call.ID]; exists {
			return fmt.Errorf("%w: duplicate tool call id %q", ErrInvalidModelResponse, call.ID)
		}
		batch[call.ID] = struct{}{}
	}
	for id := range batch {
		l.seen[id] = struct{}{}
	}
	return nil
}

type toolBatch struct {
	calls    []models.ToolCall
	results  []ToolResult
	contents []models.Content
	index    int
}

func newToolBatch(calls []models.ToolCall) *toolBatch {
	return &toolBatch{
		calls:    calls,
		results:  make([]ToolResult, 0, len(calls)),
		contents: make([]models.Content, 0, len(calls)),
	}
}

func (b *toolBatch) current() (models.ToolCall, bool) {
	if b == nil || b.index >= len(b.calls) {
		return models.ToolCall{}, false
	}
	return b.calls[b.index], true
}

func (b *toolBatch) append(result ToolResult, content models.Content) {
	b.results = append(b.results, result)
	b.contents = append(b.contents, content)
	b.index++
}

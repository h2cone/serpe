package loops

import (
	"fmt"
	"math"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

// This file owns run accounting: the budget counters and limits, the call-ID
// ledger, and the tool batch under execution. Run policy that consumes these
// values lives in policy.go; the scheduler lives in stream.go.

// runBudget is the sole owner of counters and limits used by run policy.
// Counters here are incremented only by the scheduler; decisions derived
// from them (stop reasons, tool-choice relaxation) live in policy.go.
type runBudget struct {
	limits             Limits
	modelTurns         int
	toolCalls          int
	toolBatches        int
	observedTokens     int64
	tokenOverflow      bool
	identicalSteps     int
	lastStep           string
	retainedToolBytes  int64
	canonicalToolBytes int64
}

func newRunBudget(limits Limits, canonicalToolBytes int64) runBudget {
	return runBudget{limits: limits, canonicalToolBytes: canonicalToolBytes}
}

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

// completedToolBatches reports how many tool batches have finished. The
// first completed batch relaxes a forced tool choice (policy.resolveToolChoice).
func (b *runBudget) completedToolBatches() int { return b.toolBatches }

// recordToolBatch records one completed tool batch.
func (b *runBudget) recordToolBatch() { b.toolBatches++ }

func (b *runBudget) remainingToolBytes() (retained, canonical int64) {
	return b.limits.MaxRetainedToolBytes - b.retainedToolBytes,
		b.limits.MaxCanonicalToolBytes - b.canonicalToolBytes
}

func (b *runBudget) recordToolBytes(retained, canonical int64) {
	b.retainedToolBytes += retained
	b.canonicalToolBytes += canonical
}

// accumulateObservedTokens is the bounded token accumulator: negative
// reported values are rejected, and overflow saturates at MaxInt64.
func accumulateObservedTokens(total, reported int64) (next int64, overflow, ok bool) {
	if reported < 0 {
		return total, false, false
	}
	if total > math.MaxInt64-reported {
		return math.MaxInt64, true, true
	}
	return total + reported, false, true
}

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

func newCallLedger(messages []models.Message) (callLedger, error) {
	ledger := callLedger{seen: make(map[string]struct{})}
	for messageIndex := range messages {
		for _, call := range toolCallsOf(messages[messageIndex]) {
			if call.ID == "" {
				return callLedger{}, fmt.Errorf("%w: historical tool call at message %d has an empty id", ErrInvalidModelResponse, messageIndex)
			}
			if _, exists := ledger.seen[call.ID]; exists {
				return callLedger{}, fmt.Errorf("%w: duplicate historical tool call id %q", ErrInvalidModelResponse, call.ID)
			}
			ledger.seen[call.ID] = struct{}{}
		}
	}
	return ledger, nil
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

type toolBatchHandle struct {
	batch          *tools.Batch
	calls          []models.ToolCall
	adapted        []tools.Output
	adaptedSet     []bool
	results        []tools.Output
	contents       []models.Content
	assistantBytes int64
	pending        pendingExchange
}

func (h *toolBatchHandle) close() error {
	if h == nil || h.batch == nil {
		return nil
	}
	err := h.batch.Close()
	h.batch = nil
	return err
}

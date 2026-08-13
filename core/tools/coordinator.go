package tools

import (
	"context"
	"math"
	"sync"
)

// coordinator is shared by every Batch created by an Executor. It owns the
// global submission order, callback permits, and live resource claims.
//
// Waiting uses a generation channel instead of spawning a goroutine around a
// sync.Cond wait. Cancellation therefore cannot leave helper goroutines
// behind, and every state transition wakes all waiters to re-evaluate their
// (small and bounded) predicates.
type coordinator struct {
	mu sync.Mutex

	next      uint64 // next assignable ticket; math.MaxUint64 is exhausted
	frontier  uint64 // first ticket whose activation has not been discovered
	exhausted bool

	pending  map[uint64]struct{} // activators whose final claims are unknown
	final    map[uint64][]Claim  // complete static+activation claims
	released map[uint64]struct{}
	held     map[uint64][]Claim

	parallel int
	inUse    int
	waitSeq  uint64
	waiters  []*coordWaiter
	notify   chan struct{}
}

type coordWaiter struct {
	seq       uint64
	ticket    uint64
	hasTicket bool
	claims    []Claim
}

func newCoordinator(parallel int) *coordinator {
	return &coordinator{
		pending:  make(map[uint64]struct{}),
		final:    make(map[uint64][]Claim),
		released: make(map[uint64]struct{}),
		held:     make(map[uint64][]Claim),
		parallel: parallel,
		notify:   make(chan struct{}),
	}
}

// registerBatch is the Batch.Next linearization point. Ticket allocation and
// classification of every call happen under one lock, so another batch can
// never observe an allocated-but-unclassified hole in the discovery frontier.
func (c *coordinator) registerBatch(calls []preparedCall) ([]uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := uint64(len(calls))
	if n == 0 {
		return nil, wrapExecution("cannot register an empty batch")
	}
	if c.exhausted || c.next > math.MaxUint64-n {
		return nil, wrapExecution("tool submission ticket space exhausted")
	}

	first := c.next
	tickets := make([]uint64, len(calls))
	for i := range calls {
		ticket := first + uint64(i)
		tickets[i] = ticket
		switch {
		case calls[i].rejected != nil:
			// A local rejection has no resource-discovery phase. Marking it
			// released here is essential: otherwise an early rejected call
			// would permanently pin the global frontier.
			c.final[ticket] = nil
			c.released[ticket] = struct{}{}
		case calls[i].hasActivator:
			c.pending[ticket] = struct{}{}
		case true:
			c.final[ticket] = append([]Claim(nil), calls[i].static...)
		}
	}
	c.next += n
	if c.next == math.MaxUint64 {
		c.exhausted = true
	}
	c.advanceFrontierLocked()
	c.signalLocked()
	return tickets, nil
}

// discover records an Activator's complete claim set before it waits for a
// conflicting claim extension. This advances the discovery frontier and lets
// later unrelated work proceed without allowing a later conflicting call to
// overtake it.
func (c *coordinator) discover(ticket uint64, claims []Claim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pending[ticket]; !ok {
		return wrapExecution("activation ticket %d is not pending", ticket)
	}
	c.final[ticket] = append([]Claim(nil), claims...)
	delete(c.pending, ticket)
	c.advanceFrontierLocked()
	c.signalLocked()
	return nil
}

func (c *coordinator) advanceFrontierLocked() {
	for c.frontier < c.next {
		if _, pending := c.pending[c.frontier]; pending {
			return
		}
		c.frontier++
	}
}

func (c *coordinator) acquirePermit(ctx context.Context) error {
	return c.acquireWaiter(ctx, &coordWaiter{})
}

func (c *coordinator) acquire(ctx context.Context, ticket uint64, claims []Claim) error {
	return c.acquireWaiter(ctx, &coordWaiter{
		ticket:    ticket,
		hasTicket: true,
		claims:    append([]Claim(nil), claims...),
	})
}

func (c *coordinator) acquireWaiter(ctx context.Context, waiter *coordWaiter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.waitSeq++
	waiter.seq = c.waitSeq
	c.waiters = append(c.waiters, waiter)
	c.signalLocked()
	for {
		if c.canGrantLocked(waiter) && !c.earlierGrantableLocked(waiter) {
			c.removeWaiterLocked(waiter)
			c.inUse++
			if waiter.hasTicket {
				c.held[waiter.ticket] = append([]Claim(nil), waiter.claims...)
			}
			c.signalLocked()
			c.mu.Unlock()
			return nil
		}
		ch := c.notify
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			c.mu.Lock()
			c.removeWaiterLocked(waiter)
			c.signalLocked()
			c.mu.Unlock()
			return ctx.Err()
		case <-ch:
			c.mu.Lock()
		}
	}
}

// earlierGrantableLocked gives permit requests FIFO fairness without forcing
// an unrelated ready call to queue behind an earlier resource-blocked call.
func (c *coordinator) earlierGrantableLocked(waiter *coordWaiter) bool {
	for _, prior := range c.waiters {
		if prior == waiter {
			return false
		}
		if prior.seq < waiter.seq && c.canGrantLocked(prior) {
			return true
		}
	}
	return false
}

func (c *coordinator) canGrantLocked(waiter *coordWaiter) bool {
	if c.inUse >= c.parallel {
		return false
	}
	if !waiter.hasTicket {
		return true
	}
	if waiter.ticket > c.frontier {
		return false
	}
	return c.claimsFreeExceptLocked(waiter.ticket, waiter.claims)
}

func (c *coordinator) removeWaiterLocked(target *coordWaiter) {
	for i, waiter := range c.waiters {
		if waiter != target {
			continue
		}
		copy(c.waiters[i:], c.waiters[i+1:])
		c.waiters[len(c.waiters)-1] = nil
		c.waiters = c.waiters[:len(c.waiters)-1]
		return
	}
}

func (c *coordinator) releasePermit() {
	c.mu.Lock()
	if c.inUse > 0 {
		c.inUse--
	}
	c.signalLocked()
	c.mu.Unlock()
}

// extend atomically replaces the held static set with the complete set. The
// complete set must already have been registered with discover.
func (c *coordinator) extend(ctx context.Context, ticket uint64, claims []Claim) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		if c.claimsFreeExceptLocked(ticket, claims) {
			c.held[ticket] = append([]Claim(nil), claims...)
			c.signalLocked()
			c.mu.Unlock()
			return nil
		}
		ch := c.notify
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (c *coordinator) release(ticket uint64) {
	c.mu.Lock()
	if _, ok := c.held[ticket]; ok {
		delete(c.held, ticket)
		if c.inUse > 0 {
			c.inUse--
		}
	}
	delete(c.pending, ticket)
	if _, known := c.final[ticket]; !known {
		c.final[ticket] = nil
	}
	c.released[ticket] = struct{}{}
	c.advanceFrontierLocked()
	c.signalLocked()
	c.mu.Unlock()
}

func (c *coordinator) claimsFreeExceptLocked(ticket uint64, claims []Claim) bool {
	for other, held := range c.held {
		if other == ticket {
			continue
		}
		if claimsConflict(held, claims) {
			return false
		}
	}
	// A complete earlier claim set reserves ordering even while that earlier
	// call is waiting for a permit. This is what prevents a later conflicting
	// call from overtaking it.
	for other, final := range c.final {
		if other >= ticket {
			continue
		}
		if _, done := c.released[other]; done {
			continue
		}
		if claimsConflict(final, claims) {
			return false
		}
	}
	return true
}

func (c *coordinator) signalLocked() {
	close(c.notify)
	c.notify = make(chan struct{})
}

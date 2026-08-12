package agent

import (
	"context"

	"github.com/h2cone/serpe/core/models"
)

// Run executes a complete agent run by draining Stream.
func (r *Runner) Run(ctx context.Context, req *models.Request) (*Result, error) {
	stream, err := r.NewStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Next() {
	}
	return stream.Result(), stream.Err()
}

// NewStream starts a pull-based agent run. The returned Stream is the sole
// state machine; Run drains it to completion.
func (r *Runner) NewStream(ctx context.Context, req *models.Request) (Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conversation, err := r.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	rs := &runStream{
		runner:       r,
		ctx:          runCtx,
		cancel:       cancel,
		conversation: conversation,
		budget:       newRunBudget(r.limits),
		ledger:       newCallLedger(),
		phase:        phaseRunStart,
	}
	return rs, nil
}

func (r *Runner) prepareRequest(req *models.Request) (*conversation, error) {
	return newConversation(req, r.tools.definitions())
}

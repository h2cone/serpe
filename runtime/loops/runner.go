package loops

import (
	"context"

	"github.com/h2cone/serpe/core/models"
)

// Run executes a complete agent run by draining Stream.
func (r *Runner) Run(ctx context.Context, req *models.Request) (*Result, error) {
	stream, err := r.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Next() {
	}
	return stream.Result(), stream.Err()
}

// Stream starts a pull-based agent run. The returned Stream is the sole
// state machine; Run drains it to completion.
func (r *Runner) Stream(ctx context.Context, req *models.Request) (Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conversation, err := r.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	ledger, err := newCallLedger(conversation.messages)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	rs := &runStateMachine{
		runner:       r,
		ctx:          runCtx,
		cancel:       cancel,
		conversation: conversation,
		budget:       newRunBudget(r.limits, conversation.canonicalToolBytes),
		ledger:       ledger,
		phase:        phaseRunStart,
	}
	return rs, nil
}

func (r *Runner) prepareRequest(req *models.Request) (*conversation, error) {
	var defs []models.Tool
	if r.tools != nil {
		defs = r.tools.Definitions()
	}
	return newConversation(req, defs, r.limits, r.toolResultPolicy, r.toolResultPolicyKnown)
}

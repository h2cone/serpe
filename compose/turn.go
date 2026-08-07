package compose

import (
	"context"
	"fmt"
	"sync"

	"github.com/h2cone/ouro/agent"
	"github.com/h2cone/ouro/core/models"
	"github.com/h2cone/ouro/core/sessions"
)

// ErrConcurrentTurn reports that another turn modified the session transcript
// between Get and commit. It is an alias of sessions.ErrConflict so callers
// can use either sentinel with errors.Is.
var ErrConcurrentTurn = sessions.ErrConflict

// Config constructs a TurnService.
type Config struct {
	Runner  *agent.Runner
	Manager *sessions.Manager
}

// TurnService runs one conversational turn against a session: load history,
// invoke the runner, and commit the transcript suffix only on success.
//
// Internally each turn is one transaction (load → run → decide commit →
// CAS-append). Callers see Send/Stream only; commit policy and CAS length
// stay inside the package.
type TurnService struct {
	runner *agent.Runner
	mgr    *sessions.Manager
}

// New validates config and returns a TurnService.
func New(cfg Config) (*TurnService, error) {
	if cfg.Runner == nil {
		return nil, fmt.Errorf("compose: runner is required")
	}
	if cfg.Manager == nil {
		return nil, fmt.Errorf("compose: manager is required")
	}
	return &TurnService{runner: cfg.Runner, mgr: cfg.Manager}, nil
}

// Send runs a blocking turn. On success it returns the run result and the
// committed session snapshot. See package doc for commit policy and return
// shapes on failure paths.
func (s *TurnService) Send(ctx context.Context, sessionID, prompt string) (*agent.Result, *sessions.Session, error) {
	tx, err := s.begin(ctx, sessionID, prompt)
	if err != nil {
		return nil, nil, err
	}
	result, err := s.runner.Run(ctx, tx.req)
	if err != nil {
		// Request validation / construction failure: no partial result.
		// Fatal/cancel after start: partial result, no commit.
		return result, nil, err
	}
	committed, commitErr := tx.commit(ctx, result)
	if commitErr != nil {
		return result, nil, commitErr
	}
	return result, committed, nil
}

// Stream starts a streaming turn. The returned Turn wraps the inner
// agent.Stream and runs the same commit transaction once when the stream is
// naturally exhausted and the run completed. Close does not commit.
//
// Stream commit reuses the caller's context while it is still live (deadlines
// and cancellation apply). If the context is already canceled when commit
// runs—common after a successful drain when the caller canceled the stream
// ctx—commit uses context.WithoutCancel so a clean run is still persisted.
//
// After Next returns false, Err() is the singular terminal check (inner error
// else commit failure). Session() is non-nil only when commit succeeded.
func (s *TurnService) Stream(ctx context.Context, sessionID, prompt string) (*Turn, error) {
	tx, err := s.begin(ctx, sessionID, prompt)
	if err != nil {
		return nil, err
	}
	inner, err := s.runner.Stream(ctx, tx.req)
	if err != nil {
		return nil, err
	}
	return &Turn{
		inner: inner,
		tx:    tx,
		ctx:   ctx,
	}, nil
}

// turnTxn holds the load/build snapshot for one turn's commit decision.
// Knowledge of pre-length, session id, and commit policy lives here rather
// than as free-floating steps on TurnService.
type turnTxn struct {
	svc *TurnService
	id  string
	pre int
	req *models.Request
}

func (s *TurnService) begin(ctx context.Context, sessionID, prompt string) (*turnTxn, error) {
	session, err := s.mgr.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &turnTxn{
		svc: s,
		id:  sessionID,
		pre: len(session.Messages),
		req: projectRequest(session, prompt),
	}, nil
}

func (tx *turnTxn) commit(ctx context.Context, result *agent.Result) (*sessions.Session, error) {
	suffix, err := suffixToCommit(result, tx.pre)
	if err != nil {
		return nil, err
	}
	if suffix == nil {
		return nil, nil
	}
	return tx.svc.mgr.AppendAt(ctx, tx.id, tx.pre, suffix...)
}

// Turn decorates agent.Stream with session commit on natural exhaustion.
// One Turn has one drain loop (same single-reader rule as agent.Stream).
//
// Prefer Err() after drain for the singular outcome. CommitErr is a diagnostic
// side-channel only (when both inner and commit fail, Err prefers the inner
// error).
type Turn struct {
	inner agent.Stream
	tx    *turnTxn
	ctx   context.Context

	mu        sync.Mutex
	done      bool
	committed *sessions.Session
	commitErr error
}

// Next advances the inner stream. On the first false from a natural exhaust,
// it runs the commit decision once.
func (t *Turn) Next() bool {
	if t.inner.Next() {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return false
	}
	t.done = true
	t.tryCommitLocked()
	return false
}

// tryCommitLocked runs the commit decision. Caller must hold t.mu.
func (t *Turn) tryCommitLocked() {
	if t.inner.Err() != nil {
		return
	}
	committed, err := t.tx.commit(streamCommitContext(t.ctx), t.inner.Result())
	if err != nil {
		t.commitErr = err
		return
	}
	t.committed = committed
}

// streamCommitContext keeps the caller's deadlines/cancel while live; if the
// stream context was already canceled after a successful run, detach cancel
// so the write is not spuriously aborted.
func streamCommitContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if ctx.Err() == nil {
		return ctx
	}
	return context.WithoutCancel(ctx)
}

// Event returns the current inner event.
func (t *Turn) Event() agent.Event { return t.inner.Event() }

// Err returns the terminal turn error: the inner stream error if any,
// otherwise a commit failure. After Next returns false, Err() != nil means
// nothing was committed. Result() remains available so callers can inspect
// the run or retry commit themselves.
func (t *Turn) Err() error {
	if err := t.inner.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commitErr
}

// Result returns the inner run result snapshot.
func (t *Turn) Result() *agent.Result { return t.inner.Result() }

// Close closes the inner stream without committing.
func (t *Turn) Close() error { return t.inner.Close() }

// Session returns the committed session snapshot after a successful commit,
// or nil otherwise.
func (t *Turn) Session() *sessions.Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.committed
}

// CommitErr returns the commit error after a commit was attempted, or nil.
// Prefer Err() for the singular terminal check; CommitErr isolates the commit
// failure when diagnosing a dual-failure path (inner error takes precedence
// in Err).
func (t *Turn) CommitErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commitErr
}

// suffixToCommit returns the transcript suffix to persist, or (nil, nil) when
// the run did not complete successfully. A completed run with a short
// transcript is an invariant violation.
func suffixToCommit(result *agent.Result, pre int) ([]models.Message, error) {
	if result == nil || !result.Completed() {
		return nil, nil
	}
	if len(result.Transcript) < pre+1 {
		return nil, fmt.Errorf("compose: transcript invariant violated (len=%d, pre=%d)",
			len(result.Transcript), pre)
	}
	return result.Transcript[pre:], nil
}

// projectRequest builds the agent request from a Manager.Get snapshot.
//
// Ownership: Get already returned an independent Session. Messages are
// transferred into the Request (no second full-transcript Clone). The agent
// clones at newConversation, so compose must not retain or mutate session
// after this call.
func projectRequest(session *sessions.Session, prompt string) *models.Request {
	msgs := make([]models.Message, 0, len(session.Messages)+1)
	msgs = append(msgs, session.Messages...)
	msgs = append(msgs, models.NewUserMessage(models.Text(prompt)))
	return &models.Request{Messages: msgs}
}

package agent

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/tw8ap/ouro/internal/canon"
)

// Session owns one interactive transcript. Shells create one Session per user
// interaction context: one for a CLI/TUI process, or one per Web UI session.
// Agent remains reusable and does not store shared conversation state.
type Session struct {
	mu      sync.Mutex
	agent   *Agent
	request canon.Request
}

// NewSession creates a session from an optional canonical request template.
// Model, sampling settings, system instructions, and an existing transcript are
// preserved across turns. A nil seed starts with Agent defaults.
func NewSession(agent *Agent, seed *canon.Request) (*Session, error) {
	if agent == nil {
		return nil, errors.New("nil agent")
	}
	if seed == nil {
		seed = &canon.Request{}
	}
	return &Session{
		agent:   agent,
		request: cloneRequest(seed),
	}, nil
}

// SendText appends one user text message and runs the next Agent turn.
func (s *Session) SendText(ctx context.Context, text string) (*TurnResult, error) {
	return s.Send(ctx, []canon.ContentBlock{&canon.TextBlock{Text: text}})
}

// Send appends one structured user message and runs the next Agent turn. The
// transcript is committed only after a successful turn, so a canceled or
// failed request can be retried without corrupting session state.
func (s *Session) Send(ctx context.Context, content []canon.ContentBlock) (*TurnResult, error) {
	if len(content) == 0 {
		return nil, errors.New("empty user content")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	request := cloneRequest(&s.request)
	request.Conversation.Messages = append(request.Conversation.Messages, canon.Message{
		Role:    canon.RoleUser,
		Content: cloneBlocks(content),
	})

	result, err := s.agent.RunTurn(ctx, &request)
	if err != nil {
		return nil, err
	}
	s.request.Conversation = cloneConversation(result.Conversation)
	return result, nil
}

// SendTextStream is the text convenience form of SendStream.
func (s *Session) SendTextStream(ctx context.Context, text string, w io.Writer) (*TurnResult, error) {
	return s.SendStream(ctx, []canon.ContentBlock{&canon.TextBlock{Text: text}}, w)
}

// SendStream is Send through the provider's streaming path. The writer is a
// shell adapter boundary; it may render to a terminal/TUI or forward bytes to a
// Web client. As with Send, state is committed only on success.
func (s *Session) SendStream(ctx context.Context, content []canon.ContentBlock, w io.Writer) (*TurnResult, error) {
	if len(content) == 0 {
		return nil, errors.New("empty user content")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	request := cloneRequest(&s.request)
	request.Conversation.Messages = append(request.Conversation.Messages, canon.Message{
		Role:    canon.RoleUser,
		Content: cloneBlocks(content),
	})

	result, err := s.agent.RunStreamTurn(ctx, &request, w)
	if err != nil {
		return nil, err
	}
	s.request.Conversation = cloneConversation(result.Conversation)
	return result, nil
}

// Snapshot returns a defensive copy suitable for persistence or rendering.
func (s *Session) Snapshot() canon.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRequest(&s.request)
}

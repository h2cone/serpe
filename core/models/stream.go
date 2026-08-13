package models

import (
	"context"
	"errors"
	"io"
	"sync"
)

// EventSource is the low-level extension point used by protocol adapters. Next
// must return io.EOF only after all canonical events have been produced. Close
// must be idempotent and unblock a concurrent Next.
type EventSource interface {
	Next() (Event, error)
	Close() error
}

// StreamOption configures a normalized stream wrapper.
type StreamOption func(*streamOptions)

type streamOptions struct {
	provider string
	limits   StreamLimits
	limitErr error
}

// WithStreamProvider supplies the provider name used for locally generated
// protocol and cancellation errors.
func WithStreamProvider(provider string) StreamOption {
	return func(options *streamOptions) { options.provider = provider }
}

// WithStreamLimits tightens the package hard limits for one normalized turn.
func WithStreamLimits(limits StreamLimits) StreamOption {
	return func(options *streamOptions) {
		options.limits, options.limitErr = normalizeStreamLimits(limits)
	}
}

// NewStream wraps a canonical EventSource with ordering validation and response
// reduction. It creates no goroutine or channel.
func NewStream(ctx context.Context, source EventSource, options ...StreamOption) Stream {
	if ctx == nil {
		ctx = context.Background()
	}
	configured := streamOptions{}
	for _, option := range options {
		if option != nil {
			option(&configured)
		}
	}
	if configured.limitErr == nil && configured.limits.MaxEventBytes == 0 {
		configured.limits, configured.limitErr = normalizeStreamLimits(StreamLimits{})
	}
	stream := &eventStream{ctx: ctx, source: source, reducer: newReducer(configured.provider), provider: configured.provider}
	if configured.limitErr == nil {
		stream.limiter = newStreamLimiter(configured.limits)
	} else {
		stream.err = responseLimitError(configured.provider, "invalid stream limits", configured.limitErr)
	}
	if source == nil {
		stream.err = &Error{Kind: ErrorInvalidRequest, Provider: configured.provider, Operation: "stream", Message: "event source is nil"}
	}
	return stream
}

type eventStream struct {
	mu           sync.Mutex
	ctx          context.Context
	source       EventSource
	reducer      *reducer
	limiter      *streamLimiter
	provider     string
	current      Event
	err          error
	response     *Response
	terminalSeen bool
	finished     bool
	closed       bool
	closeOnce    sync.Once
	closeErr     error
}

func (s *eventStream) Next() bool {
	s.mu.Lock()
	if s.finished || s.err != nil || s.closed {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	event, err := s.source.Next()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.err != nil {
		return false
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			if !s.terminalSeen {
				s.err = &Error{Kind: ErrorProtocol, Provider: s.provider, Operation: "stream_next", Code: "unexpected_eof", Message: "stream ended before response_end", Cause: io.ErrUnexpectedEOF}
				return false
			}
			s.finished = true
			s.response = s.reducer.result()
			return false
		}
		if ctxErr := s.ctx.Err(); ctxErr != nil {
			s.err = ContextError(s.provider, "stream_next", ctxErr)
		} else {
			var modelErr *Error
			if errors.As(err, &modelErr) {
				s.err = modelErr
			} else {
				s.err = &Error{Kind: ErrorProtocol, Provider: s.provider, Operation: "stream_next", Code: "read_error", Message: "failed to read model stream", Cause: err, Retryable: !s.terminalSeen}
			}
		}
		return false
	}
	if s.limiter == nil {
		s.err = responseLimitError(s.provider, "stream limiter is unavailable", nil)
		return false
	}
	if err := s.limiter.accept(event); err != nil {
		s.err = responseLimitError(s.provider, "model response exceeded a hard limit", err)
		return false
	}
	if err := s.reducer.apply(event); err != nil {
		s.err = err
		return false
	}
	s.current = event.Clone()
	if event.Kind == EventResponseEnd {
		s.terminalSeen = true
	}
	return true
}

func (s *eventStream) Event() Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.Clone()
}

func (s *eventStream) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.DisplayText()
}

func (s *eventStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *eventStream) Response() *Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil || (!s.finished && !s.closed) {
		return nil
	}
	if s.response == nil && s.terminalSeen {
		s.response = s.reducer.result()
	}
	return s.response.Clone()
}

func (s *eventStream) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if !s.terminalSeen && s.err == nil {
			cause := s.ctx.Err()
			if cause == nil {
				cause = context.Canceled
			}
			s.err = ContextError(s.provider, "stream_next", cause)
		} else if s.terminalSeen {
			s.finished = true
			s.response = s.reducer.result()
		}
	}
	s.mu.Unlock()
	s.closeOnce.Do(func() {
		if s.source != nil {
			s.closeErr = s.source.Close()
		}
	})
	return s.closeErr
}

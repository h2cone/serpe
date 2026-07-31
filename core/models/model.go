package models

import "context"

// Model is the minimal interface implemented by physical and logical models.
// Implementations must be safe for concurrent use.
type Model interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (Stream, error)
}

// Stream is a synchronous, pull-based sequence of normalized model events.
// A stream has one reader. Close is idempotent and may run concurrently with
// Next.
type Stream interface {
	Next() bool
	Event() Event
	Text() string
	Err() error
	Response() *Response
	Close() error
}

// CapabilityReporter is implemented by models that can report the protocol
// adapter's capability ceiling.
type CapabilityReporter interface {
	Capabilities() CapabilitySet
}

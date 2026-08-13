package models

import "context"

// Model is the minimal interface implemented by upstream models. A model alias
// may be registered against any implementation. Implementations must be safe
// for concurrent use.
type Model interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (Stream, error)
}

// StreamLimitAcceptor starts a stream with caller-supplied limits that are no
// weaker than the package hard ceilings. Built-in models implement this so
// protocol reduction is bounded before cloning or accumulating event data.
type StreamLimitAcceptor interface {
	StreamWithLimits(context.Context, *Request, StreamLimits) (Stream, error)
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

// ToolDefinitionValidator is implemented by models that can reject a tool
// definition set that their transport or protocol cannot express.
type ToolDefinitionValidator interface {
	ValidateToolDefinitions([]Tool) error
}

// RequestBudgetReporter reports the encoded request ceiling and a
// deterministic upper bound for a concrete request.
type RequestBudgetReporter interface {
	MaxEncodedRequestBytes() int64
	EncodedRequestSizeUpperBound(ctx context.Context, req *Request, stream bool) (int64, error)
}

// ToolHistoryPolicyReporter declares whether a request-only projector may
// remove old, complete tool-call/result exchange groups. Implementations that
// need one continuous opaque provider history return false.
type ToolHistoryPolicyReporter interface {
	AllowsToolHistoryGroupDeletion() bool
}

// ToolResultPolicy is a model-specific ceiling for tool-result images.
type ToolResultPolicy struct {
	InlineImages     bool
	MIMETypes        []string
	ImageDetails     []ImageDetail
	MaxRawImageBytes int64
	MaxImages        int
	MaxWidth         int
	MaxHeight        int
	MaxPixels        int64
}

// ToolResultPolicyReporter is implemented by models that declare a tool-result
// image policy. The getter must be immediate and have no I/O.
type ToolResultPolicyReporter interface {
	ToolResultPolicy() (ToolResultPolicy, bool)
}

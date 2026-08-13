package tools

import (
	"context"
	"encoding/json"

	"github.com/h2cone/serpe/core/models"
)

// Tool is a registerable executable capability. Implementations must be safe
// for concurrent use. Definition must be an immediate, deterministic getter
// with no I/O.
type Tool interface {
	Definition() models.Tool
	Execute(context.Context, Invocation) (Output, error)
}

// Planner is optional static resource planning. Tools that omit it take the
// Executor-wide root write lock and run serially with every other call.
// Plan must not observe mutable external state.
type Planner interface {
	Plan(context.Context, Invocation) (Plan, error)
}

// Activator is optional runtime resource resolution. A tool that implements
// Activator must also implement Planner; New rejects the other combination.
// Activate may read state but must not perform the business write.
type Activator interface {
	Activate(context.Context, Invocation) (Activation, error)
}

// Invocation is the defensive copy of one call's inputs. Mutations do not
// affect later phases, events, or the transcript.
type Invocation struct {
	Arguments    json.RawMessage
	Scope        Scope
	OutputLimits OutputLimits
}

// Scope is immutable request-scoped data. V1 carries only an absolute
// working directory.
type Scope struct {
	WorkingDir string
}

// Plan is the conservative static claim set for one call.
type Plan struct {
	Claims []Claim
}

// Activation is the result of runtime resource discovery.
type Activation struct {
	Claims []Claim
	Run    func(context.Context) (Output, error)
	Close  func() error
}

// Claim names one resource and the access required.
type Claim struct {
	Resource string
	Access   Access
}

// Access is the conflict class of a claim.
type Access uint8

const (
	// AccessRead may run with other reads of the same resource.
	AccessRead Access = iota + 1
	// AccessWrite conflicts with every other claim on the same resource.
	AccessWrite
)

// Output is an unfinalized or sealed tool result. Executor overwrites Stats.
type Output struct {
	Content []models.Content
	IsError bool
	Stats   OutputStats
	receipt *collectorReceipt
	seal    *outputSeal
}

// OutputStats describes original-domain and retained-domain framed output.
type OutputStats struct {
	OriginalBytes int64
	KeptBytes     int64
	SHA256        string
	Truncated     bool
}

// Text returns a successful unfinalized text output.
func Text(text string) Output {
	return Output{Content: []models.Content{models.Text(text)}}
}

// Error returns a model-recoverable unfinalized text output.
func Error(text string) Output {
	return Output{Content: []models.Content{models.Text(text)}, IsError: true}
}

func cloneOutput(in Output) Output {
	out := Output{IsError: in.IsError, Stats: in.Stats, receipt: in.receipt, seal: in.seal}
	if in.Content != nil {
		out.Content = make([]models.Content, len(in.Content))
		for i := range in.Content {
			out.Content[i] = in.Content[i].Clone()
		}
	}
	return out
}

func cloneInvocation(in Invocation) Invocation {
	return Invocation{
		Arguments:    append(json.RawMessage(nil), in.Arguments...),
		Scope:        in.Scope,
		OutputLimits: in.OutputLimits,
	}
}

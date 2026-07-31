package models

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ResponseStatus is the normalized lifecycle status of a response.
type ResponseStatus string

const (
	// ResponseStatusCompleted means generation reached a normal protocol terminal.
	ResponseStatusCompleted ResponseStatus = "completed"
	// ResponseStatusIncomplete means generation ended without a completed result.
	ResponseStatusIncomplete ResponseStatus = "incomplete"
	// ResponseStatusFailed means the provider reported a failed response.
	ResponseStatusFailed ResponseStatus = "failed"
	// ResponseStatusCancelled means generation was cancelled.
	ResponseStatusCancelled ResponseStatus = "cancelled"
)

// FinishReason is a normalized candidate finish reason.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCall      FinishReason = "tool_call"
	FinishContentFilter FinishReason = "content_filter"
	FinishCancelled     FinishReason = "cancelled"
	FinishIncomplete    FinishReason = "incomplete"
	FinishError         FinishReason = "error"
	FinishUnknown       FinishReason = "unknown"
)

// Candidate is one provider response candidate with stable provider index.
type Candidate struct {
	Index           int
	Content         []Content
	FinishReason    FinishReason
	RawFinishReason string
	ProviderState   *ProviderState
}

// Response is a complete normalized model response.
type Response struct {
	Provider      string
	ID            string
	Model         string
	Status        ResponseStatus
	Candidates    []Candidate
	Usage         Usage
	ProviderState *ProviderState
	RequestID     string
	CreatedAt     time.Time
	Metadata      map[string]string
}

// Text concatenates displayable content in candidate zero.
func (r *Response) Text() string {
	if r == nil {
		return ""
	}
	candidate, ok := r.candidate(0)
	if !ok {
		return ""
	}
	var b strings.Builder
	for i := range candidate.Content {
		switch candidate.Content[i].Kind {
		case ContentText:
			b.WriteString(candidate.Content[i].Text.Text)
		case ContentReasoningSummary:
			b.WriteString(candidate.Content[i].ReasoningSummary.Text)
		case ContentRefusal:
			b.WriteString(candidate.Content[i].Refusal.Text)
		}
	}
	return b.String()
}

// ToolCalls returns copies of candidate zero's tool calls.
func (r *Response) ToolCalls() []ToolCall {
	if r == nil {
		return nil
	}
	candidate, ok := r.candidate(0)
	if !ok {
		return nil
	}
	var out []ToolCall
	for i := range candidate.Content {
		if candidate.Content[i].Kind == ContentToolCall {
			call := *candidate.Content[i].ToolCall
			call.Arguments = append([]byte(nil), call.Arguments...)
			out = append(out, call)
		}
	}
	return out
}

// AssistantMessage creates a continuation-safe assistant message for the
// candidate index.
func (r *Response) AssistantMessage(candidateIndex int) (Message, error) {
	if r == nil {
		return Message{}, fmt.Errorf("response: nil")
	}
	candidate, ok := r.candidate(candidateIndex)
	if !ok {
		return Message{}, fmt.Errorf("response: candidate %d not found", candidateIndex)
	}
	state := candidate.ProviderState
	if state == nil {
		state = r.ProviderState
	}
	return Message{Role: RoleAssistant, Content: cloneContents(candidate.Content), ProviderState: state.clone()}, nil
}

func (r *Response) candidate(index int) (Candidate, bool) {
	for i := range r.Candidates {
		if r.Candidates[i].Index == index {
			return r.Candidates[i], true
		}
	}
	return Candidate{}, false
}

// Clone returns a deep copy safe for the caller to retain and modify.
func (r *Response) Clone() *Response {
	if r == nil {
		return nil
	}
	out := *r
	out.Usage = r.Usage.clone()
	out.ProviderState = r.ProviderState.clone()
	if r.Candidates != nil {
		out.Candidates = make([]Candidate, len(r.Candidates))
		for i := range r.Candidates {
			out.Candidates[i] = r.Candidates[i]
			out.Candidates[i].Content = cloneContents(r.Candidates[i].Content)
			out.Candidates[i].ProviderState = r.Candidates[i].ProviderState.clone()
		}
	}
	if r.Metadata != nil {
		out.Metadata = make(map[string]string, len(r.Metadata))
		for k, v := range r.Metadata {
			out.Metadata[k] = v
		}
	}
	sort.Slice(out.Candidates, func(i, j int) bool { return out.Candidates[i].Index < out.Candidates[j].Index })
	return &out
}

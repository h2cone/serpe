package models

import (
	"encoding/json"
	"maps"
)

// InstructionRole identifies the precedence of an instruction.
type InstructionRole string

const (
	// InstructionSystem is a system-level instruction.
	InstructionSystem InstructionRole = "system"
	// InstructionDeveloper is a developer-level instruction.
	InstructionDeveloper InstructionRole = "developer"
)

// Instruction is an ordered textual model instruction.
type Instruction struct {
	Role InstructionRole
	Text string
}

// GenerationConfig contains optional sampling and output controls.
type GenerationConfig struct {
	MaxOutputTokens Optional[int]
	Temperature     Optional[float64]
	TopP            Optional[float64]
	Stop            []string
	Seed            Optional[int64]
	CandidateCount  Optional[int]
}

// Request is a complete, provider-independent model request. Model identity,
// authentication, and transport configuration are bound outside the request.
type Request struct {
	Instructions   []Instruction
	Messages       []Message
	Tools          []Tool
	ToolChoice     ToolChoice
	ResponseFormat ResponseFormat
	Generation     GenerationConfig
	RequestID      string
	Metadata       map[string]string
	Extensions     map[string]json.RawMessage
}

// NewTextRequest creates a single-turn user text request.
func NewTextRequest(text string) *Request {
	return &Request{Messages: []Message{NewUserMessage(Text(text))}}
}

// Validate checks provider-independent request invariants.
func (r *Request) Validate() error {
	return ValidateRequest(r)
}

// Clone returns a deep copy of the request, including nested media bytes,
// tool schemas, metadata, extensions, and provider state on messages.
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	out := *r
	if r.Instructions != nil {
		out.Instructions = append([]Instruction(nil), r.Instructions...)
	}
	if r.Messages != nil {
		out.Messages = make([]Message, len(r.Messages))
		for i := range r.Messages {
			out.Messages[i] = r.Messages[i].Clone()
		}
	}
	if r.Tools != nil {
		out.Tools = make([]Tool, len(r.Tools))
		for i := range r.Tools {
			out.Tools[i] = r.Tools[i].Clone()
		}
	}
	out.ResponseFormat = r.ResponseFormat
	out.ResponseFormat.Schema = append(json.RawMessage(nil), r.ResponseFormat.Schema...)
	if r.Generation.Stop != nil {
		out.Generation.Stop = append([]string(nil), r.Generation.Stop...)
	}
	out.Metadata = maps.Clone(r.Metadata)
	if r.Extensions != nil {
		out.Extensions = make(map[string]json.RawMessage, len(r.Extensions))
		for k, v := range r.Extensions {
			out.Extensions[k] = append(json.RawMessage(nil), v...)
		}
	}
	return &out
}

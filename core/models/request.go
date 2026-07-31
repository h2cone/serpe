package models

import "encoding/json"

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

package models

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ValidateRequest checks provider-independent structural invariants. Protocol
// adapters must perform their own capability validation afterward.
func ValidateRequest(req *Request) error {
	if req == nil {
		return &Error{Kind: ErrorInvalidRequest, Operation: "validate", Message: "request is nil"}
	}
	if len(req.Messages) == 0 {
		return &Error{Kind: ErrorInvalidRequest, Operation: "validate", Message: "at least one message is required"}
	}
	for i := range req.Instructions {
		if req.Instructions[i].Role != InstructionSystem && req.Instructions[i].Role != InstructionDeveloper {
			return invalid(fmt.Sprintf("instruction %d has invalid role %q", i, req.Instructions[i].Role))
		}
	}
	for i := range req.Messages {
		if err := req.Messages[i].Validate(); err != nil {
			return invalid(fmt.Sprintf("message %d: %v", i, err))
		}
	}
	seenTools := make(map[string]struct{}, len(req.Tools))
	for i := range req.Tools {
		if err := req.Tools[i].Validate(); err != nil {
			return invalid(fmt.Sprintf("tool %d: %v", i, err))
		}
		if _, exists := seenTools[req.Tools[i].Name]; exists {
			return invalid(fmt.Sprintf("duplicate tool name %q", req.Tools[i].Name))
		}
		seenTools[req.Tools[i].Name] = struct{}{}
	}
	switch req.ToolChoice.Kind {
	case "", ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		if req.ToolChoice.Name != "" {
			return invalid("tool choice name is only valid for a function choice")
		}
	case ToolChoiceFunction:
		if req.ToolChoice.Name == "" {
			return invalid("specific tool choice requires a name")
		}
		if _, ok := seenTools[req.ToolChoice.Name]; !ok {
			return invalid(fmt.Sprintf("specific tool %q is not defined", req.ToolChoice.Name))
		}
	default:
		return invalid(fmt.Sprintf("unknown tool choice %q", req.ToolChoice.Kind))
	}
	if len(req.Tools) == 0 && req.ToolChoice.Kind != "" && req.ToolChoice.Kind != ToolChoiceNone {
		return invalid("tool choice requires at least one tool")
	}
	if err := req.ResponseFormat.validate(); err != nil {
		return invalid(err.Error())
	}
	if req.Generation.MaxOutputTokens.Set && req.Generation.MaxOutputTokens.Value <= 0 {
		return invalid("max output tokens must be positive")
	}
	if req.Generation.CandidateCount.Set && req.Generation.CandidateCount.Value <= 0 {
		return invalid("candidate count must be positive")
	}
	if req.Generation.Temperature.Set && (math.IsNaN(req.Generation.Temperature.Value) || math.IsInf(req.Generation.Temperature.Value, 0) || req.Generation.Temperature.Value < 0) {
		return invalid("temperature must be finite and non-negative")
	}
	if req.Generation.TopP.Set && (math.IsNaN(req.Generation.TopP.Value) || math.IsInf(req.Generation.TopP.Value, 0) || req.Generation.TopP.Value < 0 || req.Generation.TopP.Value > 1) {
		return invalid("top-p must be between zero and one")
	}
	for i, stop := range req.Generation.Stop {
		if stop == "" {
			return invalid(fmt.Sprintf("stop sequence %d is empty", i))
		}
	}
	for namespace, raw := range req.Extensions {
		if namespace == "" || strings.TrimSpace(namespace) != namespace || !strings.Contains(namespace, ".") {
			return invalid(fmt.Sprintf("extension namespace %q is invalid", namespace))
		}
		if len(raw) == 0 || !json.Valid(raw) {
			return invalid(fmt.Sprintf("extension %q is not valid JSON", namespace))
		}
	}
	return nil
}

func invalid(message string) error {
	return &Error{Kind: ErrorInvalidRequest, Operation: "validate", Message: message}
}

// ValidateCapabilities rejects request features absent from the adapter's
// reported ceiling. Provider-specific semantic checks still run separately.
func ValidateCapabilities(req *Request, set CapabilitySet, provider string) error {
	if err := ValidateRequest(req); err != nil {
		if modelErr, ok := err.(*Error); ok && modelErr.Provider == "" {
			modelErr.Provider = provider
		}
		return err
	}
	require := func(capability Capability, feature string) error {
		if set.Has(capability) {
			return nil
		}
		return &Error{Kind: ErrorUnsupportedFeature, Provider: provider, Operation: "validate", Message: feature + " is not supported by this protocol adapter"}
	}
	for _, message := range req.Messages {
		for _, content := range message.Content {
			switch content.Kind {
			case ContentImage:
				if err := require(CapabilityImageInput, "image input"); err != nil {
					return err
				}
			case ContentToolCall, ContentToolResult:
				if err := require(CapabilityTools, "tools"); err != nil {
					return err
				}
			case ContentReasoningSummary:
				if err := require(CapabilityReasoningSummary, "reasoning summaries"); err != nil {
					return err
				}
			}
		}
	}
	if len(req.Tools) > 0 {
		if err := require(CapabilityTools, "tools"); err != nil {
			return err
		}
	}
	if req.ResponseFormat.Kind == ResponseFormatJSONObject {
		if err := require(CapabilityJSONOutput, "JSON object output"); err != nil {
			return err
		}
	}
	if req.ResponseFormat.Kind == ResponseFormatJSONSchema {
		if err := require(CapabilityJSONSchema, "JSON Schema output"); err != nil {
			return err
		}
	}
	if req.Generation.CandidateCount.Set && req.Generation.CandidateCount.Value > 1 {
		if err := require(CapabilityMultipleCandidates, "multiple candidates"); err != nil {
			return err
		}
	}
	return nil
}

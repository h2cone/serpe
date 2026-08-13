package loops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/h2cone/serpe/core/models"
)

// This file owns run policy: model terminal classification, tool-choice
// resolution and validation, and terminal model errors. Budget accounting and
// counters live in run_state.go; the pull scheduler lives in stream.go and
// only applies the decisions made here.

type afterModelAction int

const (
	afterModelComplete afterModelAction = iota
	afterModelRunTools
)

type afterModel struct {
	action    afterModelAction
	assistant models.Message
	calls     []models.ToolCall
}

// decideAfterModel is the single policy entry point for "what does this
// finished model turn mean": it classifies the response and validates it
// against the turn's tool choice. The scheduler applies the returned
// decision without re-deriving policy.
func decideAfterModel(resp *models.Response, choice models.ToolChoice) (afterModel, error) {
	classified, err := classifyModelResponse(resp)
	if err != nil {
		return afterModel{}, err
	}
	if err := validateToolChoice(choice, classified.calls); err != nil {
		return afterModel{}, err
	}
	return classified, nil
}

// classifyModelResponse accepts only response terminals that are safe either
// to commit as a final answer or to continue through tool execution.
func classifyModelResponse(resp *models.Response) (afterModel, error) {
	if resp == nil {
		return afterModel{}, fmt.Errorf("%w: nil response", ErrInvalidModelResponse)
	}

	switch resp.Status {
	case models.ResponseStatusIncomplete, models.ResponseStatusFailed:
		return afterModel{}, modelTerminalError(resp, false)
	case models.ResponseStatusCancelled:
		return afterModel{}, modelTerminalError(resp, true)
	case models.ResponseStatusCompleted:
		// Candidate-level classification below decides whether this completed
		// response is a final answer or a valid tool-call turn.
	default:
		return afterModel{}, fmt.Errorf("%w: unknown response status %q", ErrInvalidModelResponse, resp.Status)
	}

	candidate, ok := candidateZero(resp)
	if !ok {
		return afterModel{}, fmt.Errorf("%w: missing candidate zero", ErrInvalidModelResponse)
	}
	calls, err := candidateToolCalls(candidate)
	if err != nil {
		return afterModel{}, err
	}

	action := afterModelComplete
	switch candidate.FinishReason {
	case models.FinishStop:
		if len(calls) != 0 {
			return afterModel{}, fmt.Errorf("%w: stop finish contains tool calls", ErrInvalidModelResponse)
		}
	case models.FinishToolCall:
		if len(calls) == 0 {
			return afterModel{}, fmt.Errorf("%w: tool-call finish contains no tool calls", ErrInvalidModelResponse)
		}
		action = afterModelRunTools
	case models.FinishContentFilter:
		if len(calls) != 0 {
			return afterModel{}, modelTerminalError(resp, false)
		}
		if !candidateIsRefusal(candidate) {
			return afterModel{}, modelTerminalError(resp, false)
		}
	case models.FinishCancelled:
		return afterModel{}, modelTerminalError(resp, true)
	case models.FinishLength, models.FinishIncomplete, models.FinishError, models.FinishUnknown:
		return afterModel{}, modelTerminalError(resp, false)
	default:
		return afterModel{}, fmt.Errorf("%w: unknown finish reason %q", ErrInvalidModelResponse, candidate.FinishReason)
	}

	assistant, err := resp.AssistantMessage(0)
	if err != nil {
		return afterModel{}, fmt.Errorf("%w: %v", ErrInvalidModelResponse, err)
	}
	if err := assistant.Validate(); err != nil {
		return afterModel{}, fmt.Errorf("%w: assistant message: %v", ErrInvalidModelResponse, err)
	}
	return afterModel{action: action, assistant: assistant, calls: calls}, nil
}

func modelTerminalError(resp *models.Response, cancelled bool) error {
	finish := models.FinishUnknown
	if candidate, ok := candidateZero(resp); ok {
		finish = candidate.FinishReason
	}
	if cancelled {
		return fmt.Errorf("%w: status %q finish %q: %w", ErrModelResponse, resp.Status, finish, context.Canceled)
	}
	return fmt.Errorf("%w: status %q finish %q", ErrModelResponse, resp.Status, finish)
}

func candidateToolCalls(candidate models.Candidate) ([]models.ToolCall, error) {
	var calls []models.ToolCall
	for i := range candidate.Content {
		content := candidate.Content[i]
		if content.Kind != models.ContentToolCall {
			continue
		}
		if content.ToolCall == nil {
			return nil, fmt.Errorf("%w: tool-call content is missing its value", ErrInvalidModelResponse)
		}
		call := *content.ToolCall
		call.Arguments = append(json.RawMessage(nil), content.ToolCall.Arguments...)
		calls = append(calls, call)
	}
	return calls, nil
}

func candidateIsRefusal(candidate models.Candidate) bool {
	if strings.EqualFold(strings.TrimSpace(candidate.RawFinishReason), "refusal") {
		return true
	}
	for i := range candidate.Content {
		if candidate.Content[i].Kind == models.ContentRefusal && candidate.Content[i].Refusal != nil {
			return true
		}
	}
	return false
}

func validateToolChoice(choice models.ToolChoice, calls []models.ToolCall) error {
	switch choice.Kind {
	case "", models.ToolChoiceAuto:
		return nil
	case models.ToolChoiceNone:
		if len(calls) != 0 {
			return fmt.Errorf("%w: tool calls returned while tool choice is none", ErrInvalidModelResponse)
		}
	case models.ToolChoiceRequired:
		if len(calls) == 0 {
			return fmt.Errorf("%w: required tool call is missing", ErrInvalidModelResponse)
		}
	case models.ToolChoiceFunction:
		if len(calls) == 0 {
			return fmt.Errorf("%w: required tool %q was not called", ErrInvalidModelResponse, choice.Name)
		}
		for _, call := range calls {
			if call.Name != choice.Name {
				return fmt.Errorf("%w: required tool %q, got %q", ErrInvalidModelResponse, choice.Name, call.Name)
			}
		}
	default:
		return fmt.Errorf("%w: unknown tool choice %q", ErrInvalidModelResponse, choice.Kind)
	}
	return nil
}

// resolveToolChoice derives the tool choice for the next model turn. After
// the first completed tool batch, a forced choice is relaxed to auto so the
// model may answer directly; a none choice is never overridden.
func resolveToolChoice(base models.ToolChoice, completedToolBatches int) models.ToolChoice {
	if completedToolBatches > 0 && base.Kind != models.ToolChoiceNone {
		return models.ToolChoice{Kind: models.ToolChoiceAuto}
	}
	return base
}

func candidateZero(resp *models.Response) (models.Candidate, bool) {
	if resp == nil {
		return models.Candidate{}, false
	}
	for i := range resp.Candidates {
		if resp.Candidates[i].Index == 0 {
			return resp.Candidates[i], true
		}
	}
	return models.Candidate{}, false
}

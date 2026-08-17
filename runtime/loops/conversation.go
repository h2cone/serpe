package loops

import (
	"context"
	"fmt"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/sessionwire"
)

// conversation is the sole mutable owner of a run's message sequence. base is
// an immutable request template whose Messages field is always nil.
type conversation struct {
	base               *models.Request
	messages           []models.Message
	context            ContextLimits
	toolResultPolicy   models.ToolResultPolicy
	toolPolicyKnown    bool
	canonicalToolBytes int64
}

func newConversation(req *models.Request, tools []models.Tool, limits Limits, policy models.ToolResultPolicy, policyKnown bool) (*conversation, error) {
	if req == nil {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Operation: "runtime", Message: "request is nil"}
	}
	if len(req.Tools) > 0 {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Operation: "runtime", Message: "request tools must be empty; register tools on the Runner"}
	}
	if req.Generation.CandidateCount.Set && req.Generation.CandidateCount.Value != 1 {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Operation: "runtime", Message: "candidate count must be unset or 1"}
	}
	if len(req.Messages) == 0 {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Operation: "runtime", Message: "at least one message is required"}
	}
	if last := req.Messages[len(req.Messages)-1]; last.Role != models.RoleUser {
		return nil, &models.Error{Kind: models.ErrorInvalidRequest, Operation: "runtime", Message: "last message must be a user message"}
	}
	for i := range req.Messages {
		size, err := sessionwire.MessageFragmentSize(req.Messages[i])
		if err != nil {
			return nil, &models.Error{Kind: models.ErrorInvalidRequest, Operation: "runtime", Message: fmt.Sprintf("message %d is invalid", i), Cause: err}
		}
		if size > limits.MaxSessionMessageJSONBytes {
			return nil, fmt.Errorf("%w: request message %d exceeds %d JSON bytes", ErrRunLimit, i, limits.MaxSessionMessageJSONBytes)
		}
	}
	if err := validateToolHistory(req.Messages); err != nil {
		return nil, err
	}
	canonical, err := canonicalToolHistoryBytes(req.Messages)
	if err != nil {
		return nil, err
	}
	if canonical > limits.MaxCanonicalToolBytes {
		return nil, fmt.Errorf("%w: canonical tool history exceeds %d bytes", ErrRunLimit, limits.MaxCanonicalToolBytes)
	}
	snapshot := req.Clone()
	// tools is a fresh snapshot produced by toolSet and is transferred here.
	snapshot.Tools = tools
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	messages := snapshot.Messages
	snapshot.Messages = nil
	return &conversation{
		base: snapshot, messages: messages, context: limits.Context,
		toolResultPolicy: policy, toolPolicyKnown: policyKnown,
		canonicalToolBytes: canonical,
	}, nil
}

func (c *conversation) requestPlanned(ctx context.Context, choice models.ToolChoice, plan projectionPlan, additional ...models.Message) (*models.Request, projectionInfo, error) {
	req := c.base.Clone()
	req.ToolChoice = choice
	if len(req.Tools) == 0 && req.ToolChoice.Kind == models.ToolChoiceAuto {
		req.ToolChoice = models.ToolChoice{}
	}
	messages := c.snapshot()
	for i := range additional {
		messages = append(messages, additional[i].Clone())
	}
	projected, info, err := projectToolContextPlanned(ctx, messages, c.context, c.toolResultPolicy, c.toolPolicyKnown, plan)
	if err != nil {
		return nil, projectionInfo{}, err
	}
	req.Messages = projected
	if err := req.Validate(); err != nil {
		return nil, projectionInfo{}, fmt.Errorf("%w: projected model request is invalid: %v", ErrInvalidModelResponse, err)
	}
	return req, info, nil
}

func (c *conversation) toolChoice() models.ToolChoice {
	return c.base.ToolChoice
}

type pendingExchange struct {
	assistant models.Message
	results   []models.Content
}

func (c *conversation) commitAssistant(message models.Message) error {
	if err := message.Validate(); err != nil {
		return fmt.Errorf("%w: assistant message: %v", ErrInvalidModelResponse, err)
	}
	c.messages = append(c.messages, message)
	return nil
}

func (c *conversation) commitExchange(p pendingExchange) error {
	result := models.NewUserMessage(p.results...)
	if err := validateToolHistory([]models.Message{p.assistant, result}); err != nil {
		return err
	}
	c.messages = append(c.messages, p.assistant, result)
	return nil
}

func (c *conversation) snapshot() []models.Message {
	if c == nil || c.messages == nil {
		return nil
	}
	out := make([]models.Message, len(c.messages))
	for i := range c.messages {
		out[i] = c.messages[i].Clone()
	}
	return out
}

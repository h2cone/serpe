package runtime

import "github.com/h2cone/serpe/core/models"

// conversation is the sole mutable owner of a run's message sequence. base is
// an immutable request template whose Messages field is always nil.
type conversation struct {
	base     *models.Request
	messages []models.Message
}

func newConversation(req *models.Request, tools []models.Tool) (*conversation, error) {
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
	snapshot := req.Clone()
	// tools is a fresh snapshot produced by toolSet and is transferred here.
	snapshot.Tools = tools
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	messages := snapshot.Messages
	snapshot.Messages = nil
	return &conversation{base: snapshot, messages: messages}, nil
}

func (c *conversation) request(choice models.ToolChoice) *models.Request {
	req := c.base.Clone()
	req.ToolChoice = choice
	req.Messages = c.snapshot()
	return req
}

func (c *conversation) toolChoice() models.ToolChoice {
	return c.base.ToolChoice
}

func (c *conversation) appendAssistant(message models.Message) {
	c.messages = append(c.messages, message)
}

func (c *conversation) appendToolResults(results []models.Content) {
	c.messages = append(c.messages, models.NewUserMessage(results...))
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

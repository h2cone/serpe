package agent

import "github.com/h2cone/serpe/core/models"

// StopReason explains why a run ended.
type StopReason string

const (
	StopCompleted         StopReason = "completed"
	StopMaxModelTurns     StopReason = "max_model_turns"
	StopMaxToolCalls      StopReason = "max_tool_calls"
	StopMaxObservedTokens StopReason = "max_observed_tokens"
	StopStalled           StopReason = "stalled"
	StopFailed            StopReason = "failed"
	StopCancelled         StopReason = "cancelled"
)

// Step records one model turn and any tool results produced from it.
type Step struct {
	Index       int
	Response    *models.Response
	ToolResults []models.Content
}

// Result is the outcome of a single agent run.
type Result struct {
	Transcript          []models.Message
	Steps               []Step
	StopReason          StopReason
	ObservedTotalTokens int64
}

// Completed reports whether the run finished with a final model answer.
func (r *Result) Completed() bool {
	return r != nil && r.StopReason == StopCompleted
}

// LastResponse returns the response from the final model turn, if any.
func (r *Result) LastResponse() *models.Response {
	if r == nil || len(r.Steps) == 0 {
		return nil
	}
	return r.Steps[len(r.Steps)-1].Response
}

// Text returns LastResponse().Text only for a completed run.
func (r *Result) Text() string {
	if r == nil || !r.Completed() || r.LastResponse() == nil {
		return ""
	}
	return r.LastResponse().Text()
}

// runRecord owns the diagnostic response history and terminal outcome. Public
// Results are defensive projections combined with conversation and budget.
type runRecord struct {
	steps      []Step
	stopReason StopReason
}

// setStopReason is the only writer of the terminal stop reason. The first
// writer wins: a concurrent Close or fail cannot overwrite a policy stop,
// and a late policy stop cannot overwrite an earlier terminal outcome.
func (r *runRecord) setStopReason(reason StopReason) {
	if r == nil || reason == "" || r.stopReason != "" {
		return
	}
	r.stopReason = reason
}

func (r *runRecord) appendResponse(response *models.Response) *models.Response {
	owned := response.Clone()
	r.steps = append(r.steps, Step{Index: len(r.steps), Response: owned})
	return owned
}

func (r *runRecord) lastResponse() *models.Response {
	if r == nil || len(r.steps) == 0 {
		return nil
	}
	return r.steps[len(r.steps)-1].Response
}

func (r *runRecord) appendToolOutput(content models.Content) {
	if len(r.steps) == 0 {
		return
	}
	step := &r.steps[len(r.steps)-1]
	step.ToolResults = append(step.ToolResults, content)
}

func (r *runRecord) snapshot(conversation *conversation, budget *runBudget) *Result {
	result := &Result{
		Transcript:          conversation.snapshot(),
		StopReason:          r.stopReason,
		ObservedTotalTokens: budget.observedTokens,
	}
	if r.steps != nil {
		result.Steps = make([]Step, len(r.steps))
		for i := range r.steps {
			result.Steps[i] = r.steps[i].clone()
		}
	}
	return result
}

func (s Step) clone() Step {
	out := Step{Index: s.Index, Response: s.Response.Clone()}
	if s.ToolResults != nil {
		out.ToolResults = make([]models.Content, len(s.ToolResults))
		for i := range s.ToolResults {
			out.ToolResults[i] = s.ToolResults[i].Clone()
		}
	}
	return out
}

package agent

import (
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
)

// toolCallOutcome is one tool call's comparable fingerprint: tool name,
// normalized input JSON, and the text projection of the result. Image bytes are
// excluded (large and non-deterministic).
type toolCallOutcome struct {
	name   string
	input  string
	output string
}

// stepFingerprint captures one assistant turn's tool calls so the agent can
// detect a lack of semantic progress across consecutive turns.
type stepFingerprint struct {
	outcomes []toolCallOutcome
}

func (f *stepFingerprint) push(name, input string, r Result) {
	f.outcomes = append(f.outcomes, toolCallOutcome{
		name:   name,
		input:  input,
		output: textProjection(r),
	})
}

// textProjection joins the TextBlock content of a Result, ignoring image blocks.
func textProjection(r Result) string {
	var chunks []string
	for _, b := range r.Content {
		if t, ok := b.(*canon.TextBlock); ok {
			chunks = append(chunks, t.Text)
		}
	}
	return strings.Join(chunks, "\n")
}

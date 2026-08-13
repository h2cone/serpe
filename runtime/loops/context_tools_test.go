package loops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestProjectToolContextKeepsLatestExchanges(t *testing.T) {
	t.Parallel()
	var msgs []models.Message
	msgs = append(msgs, models.NewUserMessage(models.Text("start")))
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i%26))
		msgs = append(msgs, models.NewAssistantMessage(models.ToolCallContent(id, "f", json.RawMessage(`{}`))))
		msgs = append(msgs, models.NewUserMessage(models.ToolResultContent(id, "f", false, models.Text("ok"))))
	}
	out := projectToolContext(msgs, 16)
	if len(out) >= len(msgs) {
		t.Fatalf("projection did not shrink: %d vs %d", len(out), len(msgs))
	}
	if out[1].Role != models.RoleUser || !strings.Contains(out[1].Content[0].Text.Text, "[serpe-tool-history-summary:v1]") {
		t.Fatalf("missing summary: %+v", out[1])
	}
	// Canonical input is unchanged.
	if len(msgs) != 41 {
		t.Fatalf("canonical mutated? %d", len(msgs))
	}
}

func TestToolHistorySummaryDigestsCanonicalBodiesWithoutEchoingThem(t *testing.T) {
	t.Parallel()
	makeHistory := func(body string) []models.Message {
		return []models.Message{
			models.NewUserMessage(models.Text("start")),
			models.NewAssistantMessage(models.ToolCallContent("secret-call-id", "read", json.RawMessage(`{"path":"secret-path"}`))),
			models.NewUserMessage(models.ToolResultContent("secret-call-id", "read", false, models.Text(body))),
			{
				Role:          models.RoleAssistant,
				Content:       []models.Content{models.ToolCallContent("latest", "read", json.RawMessage(" { \"path\" : \"kept\" } "))},
				ProviderState: &models.ProviderState{Provider: "test", Data: json.RawMessage(" { \"state\" : 1 } ")},
			},
			models.NewUserMessage(models.ToolResultContent("latest", "read", false, models.Text("latest body"))),
		}
	}
	limits := ContextLimits{
		MaxToolCallArgumentContextBytes: 1 << 20,
		MaxToolExchanges:                1,
		MaxToolTextContextBytes:         16 << 10,
		MaxToolImageContextBytes:        1 << 20,
	}
	left, err := projectToolContextBounded(makeHistory("body alpha"), limits, models.ToolResultPolicy{}, false)
	if err != nil {
		t.Fatal(err)
	}
	right, err := projectToolContextBounded(makeHistory("body beta"), limits, models.ToolResultPolicy{}, false)
	if err != nil {
		t.Fatal(err)
	}
	leftSummary := left[1].Content[0].Text.Text
	rightSummary := right[1].Content[0].Text.Text
	if leftSummary == rightSummary {
		t.Fatal("rolling digest ignored a changed canonical result body")
	}
	for _, secret := range []string{"secret-call-id", "secret-path", "body alpha"} {
		if strings.Contains(leftSummary, secret) {
			t.Fatalf("summary echoed %q: %s", secret, leftSummary)
		}
	}
	kept := left[len(left)-2]
	if kept.ProviderState == nil || !bytes.Equal(kept.ProviderState.Data, []byte(" { \"state\" : 1 } ")) ||
		!bytes.Equal(kept.Content[0].ToolCall.Arguments, []byte(" { \"path\" : \"kept\" } ")) {
		t.Fatalf("retained assistant was rewritten: %+v", kept)
	}
}

func TestLatestParallelResultsShareTextBudgetFairly(t *testing.T) {
	t.Parallel()
	calls := []models.Content{
		models.ToolCallContent("a", "tool", json.RawMessage(`{}`)),
		models.ToolCallContent("b", "tool", json.RawMessage(`{}`)),
		models.ToolCallContent("c", "tool", json.RawMessage(`{}`)),
	}
	results := []models.Content{
		models.ToolResultContent("a", "tool", false, models.Text(strings.Repeat("a", 500))),
		models.ToolResultContent("b", "tool", true, models.Text(strings.Repeat("b", 500))),
		models.ToolResultContent("c", "tool", false, models.Text(strings.Repeat("c", 500))),
	}
	messages := []models.Message{
		models.NewUserMessage(models.Text("start")),
		models.NewAssistantMessage(calls...),
		models.NewUserMessage(results...),
	}
	projected, _, err := projectToolContextPlanned(context.Background(), messages, ContextLimits{
		MaxToolCallArgumentContextBytes: 1 << 20,
		MaxToolExchanges:                16,
		MaxToolTextContextBytes:         450,
		MaxToolImageContextBytes:        1 << 20,
	}, models.ToolResultPolicy{}, false, projectionPlan{allowGroupDeletion: true, detailedSummary: true})
	if err != nil {
		t.Fatal(err)
	}
	lengths := map[string]int{}
	for _, result := range toolResultsOf(projected[len(projected)-1]) {
		lengths[result.CallID] = len(result.Content[0].Text.Text)
	}
	if lengths["a"] != 150 || lengths["b"] != 150 || lengths["c"] != 150 {
		t.Fatalf("latest result budget was not fair by call index: %v", lengths)
	}
}

type projectionSizer struct {
	maximum int64
	calls   int
}

func (s *projectionSizer) MaxEncodedRequestBytes() int64 { return s.maximum }

func (s *projectionSizer) EncodedRequestSizeUpperBound(ctx context.Context, req *models.Request, stream bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !stream {
		return 0, fmt.Errorf("runtime must size the streaming request")
	}
	s.calls++
	size := int64(100 * len(req.Messages))
	for _, message := range req.Messages {
		for _, content := range message.Content {
			switch content.Kind {
			case models.ContentText:
				size += int64(len(content.Text.Text))
			case models.ContentToolResult:
				for _, child := range content.ToolResult.Content {
					if child.Kind == models.ContentText {
						size += int64(len(child.Text.Text))
					}
				}
			}
		}
	}
	return size, nil
}

func projectionConversation(t *testing.T, groups, bodyBytes int) *conversation {
	t.Helper()
	messages := []models.Message{models.NewUserMessage(models.Text("start"))}
	for index := 0; index < groups; index++ {
		id := fmt.Sprintf("call-%d", index)
		messages = append(messages,
			models.NewAssistantMessage(models.ToolCallContent(id, "tool", json.RawMessage(`{}`))),
			models.NewUserMessage(models.ToolResultContent(id, "tool", false, models.Text(strings.Repeat("x", bodyBytes)))),
		)
	}
	limits, err := normalizeLimits(Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := &models.Request{Messages: messages}
	conv, err := newConversation(request, nil, limits, models.ToolResultPolicy{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func TestEncodedRequestContractionSequence(t *testing.T) {
	t.Parallel()
	t.Run("older results first", func(t *testing.T) {
		sizer := &projectionSizer{maximum: 1800}
		runner := &Runner{requestBudget: sizer, maxEncodedRequestBytes: sizer.maximum, allowToolGroupDeletion: true}
		req, _, err := runner.prepareModelRequest(context.Background(), projectionConversation(t, 3, 1000), models.ToolChoice{})
		if err != nil {
			t.Fatal(err)
		}
		if sizer.calls != 2 {
			t.Fatalf("sizer calls=%d, want initial+older contraction", sizer.calls)
		}
		results := projectedToolResults(req.Messages)
		for _, ref := range results {
			text := req.Messages[ref.message].Content[ref.content].ToolResult.Content[0].Text.Text
			if ref.groupFromNewest == 0 && text != strings.Repeat("x", 1000) {
				t.Fatal("latest result was contracted before older results")
			}
			if ref.groupFromNewest > 0 && text != projectedFixedOmittedBody {
				t.Fatalf("older result not fixed-omitted: %q", text)
			}
		}
	})

	t.Run("deletes one old group at a time", func(t *testing.T) {
		sizer := &projectionSizer{maximum: 1100}
		runner := &Runner{requestBudget: sizer, maxEncodedRequestBytes: sizer.maximum, allowToolGroupDeletion: true}
		req, _, err := runner.prepareModelRequest(context.Background(), projectionConversation(t, 3, 500), models.ToolChoice{})
		if err != nil {
			t.Fatal(err)
		}
		if sizer.calls != 4 {
			t.Fatalf("sizer calls=%d, want initial, omit, and two group deletions", sizer.calls)
		}
		if refs := projectedToolResults(req.Messages); len(refs) != 1 || refs[0].groupFromNewest != 0 {
			t.Fatalf("expected only the latest exchange after contraction: %+v", refs)
		}
	})

	t.Run("latest result is final contraction", func(t *testing.T) {
		sizer := &projectionSizer{maximum: 350}
		runner := &Runner{requestBudget: sizer, maxEncodedRequestBytes: sizer.maximum, allowToolGroupDeletion: true}
		req, _, err := runner.prepareModelRequest(context.Background(), projectionConversation(t, 1, 500), models.ToolChoice{})
		if err != nil {
			t.Fatal(err)
		}
		refs := projectedToolResults(req.Messages)
		text := req.Messages[refs[0].message].Content[refs[0].content].ToolResult.Content[0].Text.Text
		if text != projectedFixedOmittedBody || sizer.calls != 2 {
			t.Fatalf("latest contraction=%q calls=%d", text, sizer.calls)
		}
	})

	t.Run("continuous history refuses deletion", func(t *testing.T) {
		sizer := &projectionSizer{maximum: 750}
		runner := &Runner{requestBudget: sizer, maxEncodedRequestBytes: sizer.maximum, allowToolGroupDeletion: false}
		if _, _, err := runner.prepareModelRequest(context.Background(), projectionConversation(t, 3, 500), models.ToolChoice{}); err == nil {
			t.Fatal("continuous-history model accepted an oversized request")
		}
		if sizer.calls > 20 {
			t.Fatalf("sizer calls exceeded contraction ceiling: %d", sizer.calls)
		}
	})
}

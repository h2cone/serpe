package httpapi

import (
	"encoding/json"
	"fmt"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

// messageDTO is the wire shape for a transcript message. Content uses the
// canonical models.ContentRecord table (same as sessions persistence).
type messageDTO struct {
	Role    string                 `json:"role"`
	Content []models.ContentRecord `json:"content"`
}

type toolResultDTO struct {
	Content   []models.ContentRecord `json:"content"`
	IsError   bool                   `json:"is_error,omitempty"`
	Truncated bool                   `json:"truncated,omitempty"`
	SHA256    string                 `json:"sha256,omitempty"`
	KeptBytes int64                  `json:"kept_bytes,omitempty"`
}

type usageDTO struct {
	InputTokens       *int64 `json:"input_tokens,omitempty"`
	OutputTokens      *int64 `json:"output_tokens,omitempty"`
	TotalTokens       *int64 `json:"total_tokens,omitempty"`
	CachedInputTokens *int64 `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   *int64 `json:"reasoning_tokens,omitempty"`
}

func contentsToRecords(in []models.Content) ([]models.ContentRecord, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]models.ContentRecord, len(in))
	for i := range in {
		rec, err := models.EncodeContent(in[i])
		if err != nil {
			return nil, fmt.Errorf("content %d: %w", i, err)
		}
		out[i] = rec
	}
	return out, nil
}

func messagesToDTO(msgs []models.Message) ([]messageDTO, error) {
	out := make([]messageDTO, len(msgs))
	for i := range msgs {
		content, err := contentsToRecords(msgs[i].Content)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		out[i] = messageDTO{
			Role:    string(msgs[i].Role),
			Content: content,
		}
	}
	return out, nil
}

// toolCallToRecord maps an agent tool call onto ContentRecord. Empty or
// non-object arguments become {} so the wire stays object-shaped without a
// second HTTP-layer args policy (_raw, etc.).
func toolCallToRecord(call *models.ToolCall) models.ContentRecord {
	if call == nil {
		return models.ContentRecord{
			Type:      string(models.ContentToolCall),
			Arguments: json.RawMessage(`{}`),
		}
	}
	args := call.Arguments
	if len(args) == 0 || !jsonvalue.IsObject(args) {
		args = json.RawMessage(`{}`)
	}
	rec, err := models.EncodeContent(models.ToolCallContent(call.ID, call.Name, args))
	if err != nil {
		return models.ContentRecord{
			Type:      string(models.ContentToolCall),
			ID:        call.ID,
			Name:      call.Name,
			Arguments: json.RawMessage(`{}`),
		}
	}
	return rec
}

func toolOutputToDTO(out *tools.Output) toolResultDTO {
	if out == nil {
		return toolResultDTO{Content: []models.ContentRecord{}}
	}
	content, err := contentsToRecords(out.Content)
	if err != nil {
		// Tool output should already be valid; fall back to empty body rather
		// than inventing a parallel codec for soft failures.
		return toolResultDTO{Content: []models.ContentRecord{}, IsError: out.IsError}
	}
	if content == nil {
		content = []models.ContentRecord{}
	}
	return toolResultDTO{
		Content:   content,
		IsError:   out.IsError,
		Truncated: out.Stats.Truncated,
		SHA256:    out.Stats.SHA256,
		KeptBytes: out.Stats.KeptBytes,
	}
}

func usageToDTO(u *models.Usage) *usageDTO {
	if u == nil {
		return nil
	}
	out := &usageDTO{}
	n := 0
	if u.InputTokens.Set {
		v := u.InputTokens.Value
		out.InputTokens = &v
		n++
	}
	if u.OutputTokens.Set {
		v := u.OutputTokens.Value
		out.OutputTokens = &v
		n++
	}
	if u.TotalTokens.Set {
		v := u.TotalTokens.Value
		out.TotalTokens = &v
		n++
	}
	if u.CachedInputTokens.Set {
		v := u.CachedInputTokens.Value
		out.CachedInputTokens = &v
		n++
	}
	if u.ReasoningTokens.Set {
		v := u.ReasoningTokens.Value
		out.ReasoningTokens = &v
		n++
	}
	if n == 0 {
		return nil
	}
	return out
}

func partKind(c models.Content) string {
	if c.Kind != "" {
		return string(c.Kind)
	}
	return "unknown"
}

func deltaKind(k models.DeltaKind) string {
	switch k {
	case models.DeltaText:
		return "text"
	case models.DeltaToolArguments:
		return "tool_arguments"
	case models.DeltaReasoningSummary:
		return "reasoning_summary"
	case models.DeltaRefusal:
		return "refusal"
	default:
		return string(k)
	}
}

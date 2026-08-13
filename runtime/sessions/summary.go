package sessions

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
)

// Summary is the bounded projection used by paged session listings.
type Summary struct {
	ID           string
	Title        string
	CWD          string
	ParentID     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	MessageCount int
	Preview      string
}

// ListSummariesPage loads at most one Store ID page and immediately discards
// each full decoded record after producing its bounded projection.
func (m *Manager) ListSummariesPage(ctx context.Context, afterID string, limit int) ([]Summary, string, error) {
	if err := m.enter(ctx); err != nil {
		return nil, "", err
	}
	defer m.leave()
	ids, next, err := m.store.ListIDsPage(ctx, afterID, limit)
	if err != nil {
		return nil, "", err
	}
	out := make([]Summary, 0, len(ids))
	for _, id := range ids {
		session, loadErr := m.load(ctx, id)
		if loadErr != nil {
			if errors.Is(loadErr, ErrNotFound) || errors.Is(loadErr, ErrInvalidSession) || errors.Is(loadErr, ErrRecordTooLarge) {
				continue
			}
			return nil, "", loadErr
		}
		out = append(out, summarize(session))
	}
	return out, next, nil
}

func summarize(session *Session) Summary {
	return Summary{
		ID: session.ID, Title: session.Metadata["title"], CWD: session.CWD,
		ParentID: session.ParentID, CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt, MessageCount: len(session.Messages),
		Preview: boundedPreview(session.Messages),
	}
}

func boundedPreview(messages []models.Message) string {
	for _, message := range messages {
		if message.Role != models.RoleUser {
			continue
		}
		for _, content := range message.Content {
			if content.Kind != models.ContentText || content.Text == nil || content.Text.Text == "" {
				continue
			}
			const max = 256
			text := content.Text.Text
			if len(text) <= max {
				return text
			}
			end := max - len("…")
			for end > 0 && !utf8.ValidString(text[:end]) {
				end--
			}
			return text[:end] + "…"
		}
	}
	return ""
}

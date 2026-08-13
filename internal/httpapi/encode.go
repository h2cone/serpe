package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/sessionwire"
	"github.com/h2cone/serpe/runtime/sessions"
)

const (
	maxListResponseBytes   = 8 << 20
	maxDetailResponseBytes = 48 << 20
	maxMutationAckBytes    = 256 << 10
)

type sessionSummary struct {
	ID           string `json:"id"`
	Title        string `json:"title,omitempty"`
	CWD          string `json:"cwd"`
	ParentID     string `json:"parent_id,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MessageCount int    `json:"message_count"`
	Preview      string `json:"preview,omitempty"`
}

// sessionDetail remains the contract-fixture projection. Live handlers use
// the bounded page encoder below and never materialize this whole slice.
type sessionDetail struct {
	sessionSummary
	Messages []messageDTO `json:"messages"`
}

func toDetail(session *sessions.Session) (sessionDetail, error) {
	if session == nil {
		return sessionDetail{}, sessions.ErrInvalidSession
	}
	messages, err := messagesToDTO(session.Messages)
	if err != nil {
		return sessionDetail{}, err
	}
	return sessionDetail{sessionSummary: summaryFromSession(session), Messages: messages}, nil
}

func summaryFromSession(session *sessions.Session) sessionSummary {
	return sessionSummary{
		ID: session.ID, Title: session.Metadata[metaKeyTitle], CWD: session.CWD,
		ParentID: session.ParentID, CreatedAt: session.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    session.UpdatedAt.UTC().Format(time.RFC3339Nano),
		MessageCount: len(session.Messages), Preview: previewMessages(session.Messages),
	}
}

func summaryFromProjection(summary sessions.Summary) sessionSummary {
	return sessionSummary{
		ID: summary.ID, Title: summary.Title, CWD: summary.CWD, ParentID: summary.ParentID,
		CreatedAt:    summary.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    summary.UpdatedAt.UTC().Format(time.RFC3339Nano),
		MessageCount: summary.MessageCount, Preview: summary.Preview,
	}
}

func previewMessages(messages []models.Message) string {
	for _, message := range messages {
		if message.Role != models.RoleUser {
			continue
		}
		for _, content := range message.Content {
			if content.Kind != models.ContentText || content.Text == nil || content.Text.Text == "" {
				continue
			}
			text := content.Text.Text
			if len(text) <= 256 {
				return text
			}
			end := 256 - len("…")
			for end > 0 && !utf8.ValidString(text[:end]) {
				end--
			}
			return text[:end] + "…"
		}
	}
	return ""
}

func encodeNoHTML(value any, limit int) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.Grow(min(limit, 64<<10))
	encoder := json.NewEncoder(&limitedBuffer{buffer: &buffer, limit: limit})
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := buffer.Bytes()
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	return append([]byte(nil), data...), nil
}

type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.buffer.Len() {
		return 0, errors.New("HTTP JSON response exceeds hard limit")
	}
	return w.buffer.Write(data)
}

func (s *Server) writeJSONBytes(w http.ResponseWriter, status int, data []byte) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeAPIError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + strconv.Quote(code) + `}`))
}

func (s *Server) writeJSONValue(w http.ResponseWriter, status int, value any, limit int) {
	data, err := encodeNoHTML(value, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "response_too_large")
		return
	}
	s.writeJSONBytes(w, status, data)
}

func (s *Server) buildDetail(session *sessions.Session, cursor string, cursorPresent bool, limit int) ([]byte, error) {
	if session == nil {
		return nil, sessions.ErrInvalidSession
	}
	before, snapshot := len(session.Messages), len(session.Messages)
	if cursorPresent {
		var err error
		before, snapshot, err = s.cursors.decodeDetail(cursor, session.ID, len(session.Messages))
		if err != nil {
			return nil, err
		}
	}
	summaryJSON, err := encodeNoHTML(summaryFromSession(session), maxMutationAckBytes)
	if err != nil || len(summaryJSON) == 0 || summaryJSON[len(summaryJSON)-1] != '}' {
		return nil, fmt.Errorf("detail summary encoding failed")
	}
	// Reserve exact fixed fields plus the maximum cursor wire representation.
	used := int64(len(summaryJSON) - 1 + len(`,"messages":[],"message_start":,"snapshot_length":,"next_before":""}`) + maxCursorWireBytes + 64)
	start := before
	count := 0
	for start > 0 && count < limit {
		candidate := start - 1
		size, sizeErr := sessionwire.MessageFragmentSize(session.Messages[candidate])
		if sizeErr != nil {
			return nil, sessions.ErrInvalidSession
		}
		separator := int64(0)
		if count != 0 {
			separator = 1
		}
		if size > int64(maxDetailResponseBytes)-used-separator {
			break
		}
		used += size + separator
		start = candidate
		count++
	}
	if start == before && before > 0 {
		return nil, sessions.ErrRecordTooLarge
	}
	next := ""
	if start > 0 {
		next, err = s.cursors.encodeDetail(session.ID, start, snapshot)
		if err != nil {
			return nil, err
		}
	}
	var buffer bytes.Buffer
	buffer.Grow(int(min64(used, int64(maxDetailResponseBytes))))
	buffer.Write(summaryJSON[:len(summaryJSON)-1])
	buffer.WriteString(`,"messages":[`)
	for index := start; index < before; index++ {
		if index > start {
			buffer.WriteByte(',')
		}
		if err := sessionwire.WriteMessageFragment(&limitedBuffer{buffer: &buffer, limit: maxDetailResponseBytes}, session.Messages[index]); err != nil {
			return nil, err
		}
	}
	buffer.WriteString(`],"message_start":`)
	buffer.WriteString(strconv.Itoa(start))
	buffer.WriteString(`,"snapshot_length":`)
	buffer.WriteString(strconv.Itoa(snapshot))
	if next != "" {
		buffer.WriteString(`,"next_before":`)
		buffer.WriteString(strconv.Quote(next))
	}
	buffer.WriteByte('}')
	if buffer.Len() > maxDetailResponseBytes {
		return nil, sessions.ErrRecordTooLarge
	}
	return buffer.Bytes(), nil
}

func (s *Server) writeDetail(w http.ResponseWriter, status int, session *sessions.Session, cursor string, cursorPresent bool, limit int, mutation bool) {
	data, err := s.buildDetail(session, cursor, cursorPresent, limit)
	if err == nil {
		s.writeJSONBytes(w, status, data)
		return
	}
	if mutation {
		ack := struct {
			sessionSummary
			MessagesOmitted bool   `json:"messages_omitted"`
			DetailURL       string `json:"detail_url"`
		}{sessionSummary: summaryFromSession(session), MessagesOmitted: true, DetailURL: "/api/sessions/" + session.ID}
		data, encodeErr := encodeNoHTML(ack, maxMutationAckBytes)
		if encodeErr == nil {
			s.writeJSONBytes(w, status, data)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "acknowledgment_failed")
		return
	}
	if errors.Is(err, errCursorStale) || errors.Is(err, errCursorInvalid) {
		writeAPIError(w, http.StatusBadRequest, cursorMessage(err))
		return
	}
	if errors.Is(err, sessions.ErrRecordTooLarge) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "record_too_large")
		return
	}
	writeAPIError(w, http.StatusInternalServerError, "internal_error")
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

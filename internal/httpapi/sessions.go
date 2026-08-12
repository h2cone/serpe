package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/runtime/sessions"
)

// metaKeyTitle is the sessions.Metadata key projected as the HTTP/UI title field.
const metaKeyTitle = "title"

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

type sessionDetail struct {
	sessionSummary
	Messages []messageDTO `json:"messages"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	all, err := s.mgr.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	out := make([]sessionSummary, 0, len(all))
	for _, sess := range all {
		sum, err := toSummary(sess)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CWD   string `json:"cwd"`
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cwd := strings.TrimSpace(body.CWD)
	if cwd == "" {
		cwd = s.cwd
	}
	sess := sessions.New(s.newID(), cwd)
	if body.Title != "" {
		sess.Metadata = withTitle(nil, body.Title)
	}
	created, err := s.mgr.Create(r.Context(), sess)
	if err != nil {
		writeErr(w, err)
		return
	}
	detail, err := toDetail(created)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.mgr.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	detail, err := toDetail(sess)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Title *string `json:"title"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	changes := make(map[string]*string)
	if body.Title != nil {
		if *body.Title == "" {
			changes[metaKeyTitle] = nil
		} else {
			changes[metaKeyTitle] = body.Title
		}
	}
	updated, err := s.mgr.PatchMetadata(r.Context(), id, changes)
	if err != nil {
		writeErr(w, err)
		return
	}
	detail, err := toDetail(updated)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		NewID string `json:"new_id"`
	}
	_ = decodeJSON(r, &body) // empty body is fine
	newID := strings.TrimSpace(body.NewID)
	if newID == "" {
		newID = s.newID()
	}
	forked, err := s.mgr.Fork(r.Context(), id, newID)
	if err != nil {
		writeErr(w, err)
		return
	}
	detail, err := toDetail(forked)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.mgr.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sessionTitle(meta map[string]string) string {
	if meta == nil {
		return ""
	}
	return meta[metaKeyTitle]
}

func withTitle(meta map[string]string, title string) map[string]string {
	out := cloneMetadata(meta)
	out[metaKeyTitle] = title
	return out
}

func cloneMetadata(meta map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range meta {
		out[k] = v
	}
	return out
}

func toSummary(s *sessions.Session) (sessionSummary, error) {
	if s == nil {
		return sessionSummary{}, errors.New("session is nil")
	}
	return sessionSummary{
		ID:           s.ID,
		Title:        sessionTitle(s.Metadata),
		CWD:          s.CWD,
		ParentID:     s.ParentID,
		CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    s.UpdatedAt.UTC().Format(time.RFC3339Nano),
		MessageCount: len(s.Messages),
		Preview:      previewText(s),
	}, nil
}

func toDetail(s *sessions.Session) (sessionDetail, error) {
	sum, err := toSummary(s)
	if err != nil {
		return sessionDetail{}, err
	}
	msgs, err := messagesToDTO(s.Messages)
	if err != nil {
		return sessionDetail{}, err
	}
	return sessionDetail{
		sessionSummary: sum,
		Messages:       msgs,
	}, nil
}

func previewText(s *sessions.Session) string {
	for _, m := range s.Messages {
		if m.Role != models.RoleUser {
			continue
		}
		for _, c := range m.Content {
			if c.Kind == models.ContentText && c.Text != nil && c.Text.Text != "" {
				t := c.Text.Text
				if len(t) > 80 {
					return t[:80] + "…"
				}
				return t
			}
		}
	}
	return ""
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sessions.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, sessions.ErrAlreadyExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, sessions.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, sessions.ErrInvalidSession):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

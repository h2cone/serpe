package httpapi

import (
	"bytes"
	"errors"
	"net/http"

	"github.com/h2cone/serpe/runtime/sessions"
)

const metaKeyTitle = "title"

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if rejectQuery(r) != nil || s.rejectBody(w, r) {
		if r.URL.RawQuery != "" {
			writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		}
		return
	}
	s.writeJSONValue(w, http.StatusOK, map[string]bool{"ok": true}, 1<<10)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.rejectBody(w, r) {
		return
	}
	token, present, limit, err := parsePageQuery(r, "after")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	after := ""
	if present {
		after, err = s.cursors.decodeList(token)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, cursorMessage(err))
			return
		}
	}
	if !tryPermit(s.encodePermits) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
		return
	}
	defer releasePermit(s.encodePermits)
	summaries, nextAfter, err := s.mgr.ListSummariesPage(r.Context(), after, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	var buffer bytes.Buffer
	buffer.WriteByte('[')
	lastAdvanced := after
	for index, summary := range summaries {
		encoded, encodeErr := encodeNoHTML(summaryFromProjection(summary), maxListResponseBytes)
		if encodeErr != nil || len(encoded)+buffer.Len()+1 > maxListResponseBytes {
			nextAfter = lastAdvanced
			break
		}
		if index > 0 {
			buffer.WriteByte(',')
		}
		buffer.Write(encoded)
		lastAdvanced = summary.ID
	}
	buffer.WriteByte(']')
	if nextAfter != "" {
		cursor, cursorErr := s.cursors.encodeList(nextAfter)
		if cursorErr != nil {
			writeAPIError(w, http.StatusInternalServerError, "cursor_error")
			return
		}
		w.Header().Set("X-Serpe-Next-Cursor", cursor)
	}
	s.writeJSONBytes(w, http.StatusOK, buffer.Bytes())
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if rejectQuery(r) != nil {
		writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		return
	}
	body, ok := s.readJSONObject(w, r, mutationBodyLimit, false)
	if !ok {
		return
	}
	if requireFields(body, "cwd", "title") != nil {
		writeAPIError(w, http.StatusBadRequest, "unknown_field")
		return
	}
	cwdValue, cwdPresent, err := optionalString(body, "cwd")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cwd")
		return
	}
	cwd, err := s.normalizeWorkingDir(r.Context(), cwdValue, cwdPresent)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cwd")
		return
	}
	title, titlePresent, err := optionalString(body, "title")
	if err != nil || titlePresent && !validTitle(title) {
		writeAPIError(w, http.StatusBadRequest, "invalid_title")
		return
	}
	if !tryPermit(s.encodePermits) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
		return
	}
	defer releasePermit(s.encodePermits)
	var created *sessions.Session
	for attempt := 0; attempt < 4; attempt++ {
		id, idErr := s.generatedID()
		if idErr != nil {
			writeAPIError(w, http.StatusInternalServerError, "id_generation_failed")
			return
		}
		candidate := sessions.New(id, cwd)
		if titlePresent && title != "" {
			candidate.Metadata = map[string]string{metaKeyTitle: title}
		}
		created, err = s.mgr.Create(r.Context(), candidate)
		if !errors.Is(err, sessions.ErrAlreadyExists) {
			break
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.writeDetail(w, http.StatusCreated, created, "", false, 100, true)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRouteID(id) {
		writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
		return
	}
	if s.rejectBody(w, r) {
		return
	}
	cursor, present, limit, err := parsePageQuery(r, "before")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if !tryPermit(s.encodePermits) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
		return
	}
	defer releasePermit(s.encodePermits)
	session, err := s.mgr.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.writeDetail(w, http.StatusOK, session, cursor, present, limit, false)
}

func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRouteID(id) {
		writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
		return
	}
	if rejectQuery(r) != nil {
		writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		return
	}
	body, ok := s.readJSONObject(w, r, mutationBodyLimit, false)
	if !ok {
		return
	}
	if requireFields(body, "cwd", "title") != nil {
		writeAPIError(w, http.StatusBadRequest, "unknown_field")
		return
	}
	patch := sessions.SessionPatch{}
	title, titlePresent, err := optionalString(body, "title")
	if err != nil || titlePresent && !validTitle(title) {
		writeAPIError(w, http.StatusBadRequest, "invalid_title")
		return
	}
	if titlePresent {
		patch.Metadata = map[string]*string{metaKeyTitle: &title}
		if title == "" {
			patch.Metadata[metaKeyTitle] = nil
		}
	}
	cwdValue, cwdPresent, err := optionalString(body, "cwd")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cwd")
		return
	}
	if cwdPresent {
		cwd, cwdErr := s.normalizeWorkingDir(r.Context(), cwdValue, true)
		if cwdErr != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_cwd")
			return
		}
		patch.CWD = &cwd
	}
	if !tryPermit(s.encodePermits) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
		return
	}
	defer releasePermit(s.encodePermits)
	updated, err := s.mgr.Patch(r.Context(), id, patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.writeDetail(w, http.StatusOK, updated, "", false, 100, true)
}

func (s *Server) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRouteID(id) {
		writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
		return
	}
	if rejectQuery(r) != nil {
		writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		return
	}
	body, ok := s.readJSONObject(w, r, mutationBodyLimit, true)
	if !ok {
		return
	}
	if requireFields(body, "new_id") != nil {
		writeAPIError(w, http.StatusBadRequest, "unknown_field")
		return
	}
	newID, explicit, err := optionalString(body, "new_id")
	if err != nil || explicit && !sessions.ValidID(newID) {
		writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
		return
	}
	if !tryPermit(s.encodePermits) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
		return
	}
	defer releasePermit(s.encodePermits)
	var forked *sessions.Session
	for attempt := 0; attempt < 4; attempt++ {
		if !explicit {
			newID, err = s.generatedID()
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "id_generation_failed")
				return
			}
		}
		forked, err = s.mgr.Fork(r.Context(), id, newID)
		if explicit || !errors.Is(err, sessions.ErrAlreadyExists) {
			break
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	s.writeDetail(w, http.StatusCreated, forked, "", false, 100, true)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRouteID(id) {
		writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
		return
	}
	if rejectQuery(r) != nil {
		writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		return
	}
	if s.rejectBody(w, r) {
		return
	}
	if err := s.mgr.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNoContent)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sessions.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, sessions.ErrAlreadyExists), errors.Is(err, sessions.ErrConflict):
		writeAPIError(w, http.StatusConflict, "conflict")
	case errors.Is(err, sessions.ErrInvalidSession):
		writeAPIError(w, http.StatusBadRequest, "invalid_session")
	case errors.Is(err, sessions.ErrRecordTooLarge):
		writeAPIError(w, http.StatusRequestEntityTooLarge, "record_too_large")
	case errors.Is(err, sessions.ErrClosed):
		writeAPIError(w, http.StatusServiceUnavailable, "store_closed")
	case errors.Is(err, sessions.ErrCommitUncertain):
		writeAPIError(w, http.StatusServiceUnavailable, "commit_uncertain")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error")
	}
}

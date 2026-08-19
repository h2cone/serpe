package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/h2cone/serpe/internal/workdir"
)

func (s *Server) handlePickWorkingDir(w http.ResponseWriter, r *http.Request) {
	if rejectQuery(r) != nil {
		writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		return
	}
	body, ok := s.readJSONObject(w, r, mutationBodyLimit, true)
	if !ok {
		return
	}
	if requireFields(body, "start") != nil {
		writeAPIError(w, http.StatusBadRequest, "unknown_field")
		return
	}
	start := s.cwd
	startValue, present, err := optionalString(body, "start")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cwd")
		return
	}
	if present && startValue != "" {
		if normalized, normErr := s.normalizeWorkingDir(r.Context(), startValue, true); normErr == nil {
			start = normalized
		}
	}
	if s.pickWorkingDir == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "picker_unavailable")
		return
	}
	if !s.picking.CompareAndSwap(false, true) {
		writeAPIError(w, http.StatusConflict, "picker_busy")
		return
	}
	defer s.picking.Store(false)

	path, err := s.pickWorkingDir(r.Context(), start)
	if err == nil {
		path, err = s.normalizeWorkingDir(r.Context(), path, true)
	}
	switch {
	case err == nil:
		s.writeJSONValue(w, http.StatusOK, map[string]string{"cwd": path}, 1<<10)
	case errors.Is(err, workdir.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, workdir.ErrBusy):
		writeAPIError(w, http.StatusConflict, "picker_busy")
	case errors.Is(err, workdir.ErrUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "picker_unavailable")
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_cwd")
	}
}

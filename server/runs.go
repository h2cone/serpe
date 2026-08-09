package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/h2cone/serpe/agent"
	"github.com/h2cone/serpe/core/sessions"
)

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Prompt    string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	if body.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id is required"})
		return
	}
	if body.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
		return
	}

	// Fail fast for missing sessions before opening the SSE stream.
	// compose.Stream loads the session again for the turn; this Get is only a
	// pre-flight so clients get JSON 404 instead of a half-open event stream.
	if _, err := s.mgr.Get(r.Context(), body.SessionID); err != nil {
		writeErr(w, err)
		return
	}

	turn, err := s.turns.Stream(r.Context(), body.SessionID, body.Prompt)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer turn.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	writeFrame := func(frame any) error {
		data, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		return rc.Flush()
	}

	for turn.Next() {
		if frame := mapEvent(turn.Event()); frame != nil {
			if err := writeFrame(frame); err != nil {
				return
			}
		}
	}

	// Terminal state: Err first, then Session (compose contract).
	if err := turn.Err(); err != nil {
		frame := frameError{T: "error", Message: err.Error()}
		if stop := stopFromErr(err, turn.Result()); stop != "" {
			frame.Stop = stop
		}
		_ = writeFrame(frame)
		return
	}
	if sess := turn.Session(); sess != nil {
		stop := agent.StopCompleted
		if res := turn.Result(); res != nil && res.StopReason != "" {
			stop = res.StopReason
		}
		_ = writeFrame(frameDone{
			T:            "done",
			SessionID:    sess.ID,
			Stop:         string(stop),
			MessageCount: len(sess.Messages),
		})
		return
	}
	// Budget / stall: run_end already emitted; no done, no second run_end.
}

func stopFromErr(err error, res *agent.Result) string {
	if errors.Is(err, context.Canceled) {
		return string(agent.StopCancelled)
	}
	if res != nil && res.StopReason != "" {
		return string(res.StopReason)
	}
	if errors.Is(err, sessions.ErrConflict) {
		return string(agent.StopCompleted)
	}
	return string(agent.StopFailed)
}

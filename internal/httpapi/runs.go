package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/h2cone/serpe/compose"
	"github.com/h2cone/serpe/runtime/loops"
	"github.com/h2cone/serpe/runtime/sessions"
)

const (
	maxSSEFrameBytes = 48 << 20
	sseHeartbeat     = 15 * time.Second
)

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if rejectQuery(r) != nil {
		writeAPIError(w, http.StatusBadRequest, "query_not_allowed")
		return
	}
	if !tryPermit(s.runPermits) {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
		return
	}
	defer releasePermit(s.runPermits)
	body, ok := s.readJSONObject(w, r, runBodyLimit, false)
	if !ok {
		return
	}
	if requireFields(body, "session_id", "prompt") != nil {
		writeAPIError(w, http.StatusBadRequest, "unknown_field")
		return
	}
	sessionID, sessionPresent, err := optionalString(body, "session_id")
	if err != nil || !sessionPresent || !sessions.ValidID(sessionID) {
		writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
		return
	}
	prompt, promptPresent, err := optionalString(body, "prompt")
	if err != nil || !promptPresent || prompt == "" || !utf8.ValidString(prompt) || strings.IndexByte(prompt, 0) >= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_prompt")
		return
	}

	runCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	turn, err := s.turns.Stream(runCtx, sessionID, prompt)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer turn.Close()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	items := make(chan turnPumpItem, 1)
	go pumpTurn(runCtx, turn, items)
	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			if err := s.writeSSEBytes(w, []byte(": keepalive\n\n")); err != nil {
				cancel()
				return
			}
		case item := <-items:
			if item.event != nil {
				frame := mapEvent(*item.event)
				if frame == nil {
					continue
				}
				data, encodeErr := encodeSSEFrame(frame)
				if encodeErr != nil {
					_ = s.writeSSEFrame(w, frameError{T: "error", Message: "frame_too_large", Stop: string(loops.StopFailed)})
					cancel()
					return
				}
				if err := s.writeSSEBytes(w, data); err != nil {
					cancel()
					return
				}
				continue
			}
			if item.err != nil {
				_ = s.writeSSEFrame(w, frameError{T: "error", Message: safeRunError(item.err), Stop: stopFromErr(item.err, item.result)})
				return
			}
			if item.session != nil {
				stop := loops.StopCompleted
				if item.result != nil && item.result.StopReason != "" {
					stop = item.result.StopReason
				}
				_ = s.writeSSEFrame(w, frameDone{T: "done", SessionID: item.session.ID, Stop: string(stop), MessageCount: len(item.session.Messages)})
			}
			return
		}
	}
}

type turnPumpItem struct {
	event   *loops.Event
	err     error
	result  *loops.Result
	session *sessions.Session
}

func pumpTurn(ctx context.Context, turn *compose.Turn, output chan<- turnPumpItem) {
	for turn.Next() {
		event := turn.Event()
		select {
		case output <- turnPumpItem{event: &event}:
		case <-ctx.Done():
			return
		}
	}
	item := turnPumpItem{err: turn.Err(), result: turn.Result(), session: turn.Session()}
	select {
	case output <- item:
	case <-ctx.Done():
	}
}

func encodeSSEFrame(frame any) ([]byte, error) {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&limitedBuffer{buffer: &payload, limit: maxSSEFrameBytes - len("data: \n\n")})
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(frame); err != nil {
		return nil, err
	}
	jsonBytes := payload.Bytes()
	if len(jsonBytes) > 0 && jsonBytes[len(jsonBytes)-1] == '\n' {
		jsonBytes = jsonBytes[:len(jsonBytes)-1]
	}
	if len(jsonBytes)+len("data: \n\n") > maxSSEFrameBytes {
		return nil, errors.New("SSE frame exceeds hard limit")
	}
	out := make([]byte, 0, len(jsonBytes)+len("data: \n\n"))
	out = append(out, "data: "...)
	out = append(out, jsonBytes...)
	out = append(out, '\n', '\n')
	return out, nil
}

func (s *Server) writeSSEFrame(w http.ResponseWriter, frame any) error {
	data, err := encodeSSEFrame(frame)
	if err != nil {
		return err
	}
	return s.writeSSEBytes(w, data)
}

func (s *Server) writeSSEBytes(w http.ResponseWriter, data []byte) error {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		return err
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	if _, err := w.Write(data); err != nil {
		return err
	}
	return controller.Flush()
}

func safeRunError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, sessions.ErrConflict):
		return "session_conflict"
	case errors.Is(err, loops.ErrRunLimit):
		return "run_limit"
	default:
		return "run_failed"
	}
}

func stopFromErr(err error, result *loops.Result) string {
	if errors.Is(err, context.Canceled) {
		return string(loops.StopCancelled)
	}
	if result != nil && result.StopReason != "" {
		return string(result.StopReason)
	}
	if errors.Is(err, sessions.ErrConflict) {
		return string(loops.StopCompleted)
	}
	return string(loops.StopFailed)
}

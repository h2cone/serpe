package httpapi

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"runtime/pprof"
	"slices"
	"strings"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 1

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for _, mw := range slices.Backward(mws) {
		h = mw(h)
	}
	return h
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("http handler panic request_id=%q", requestIDFromContext(r.Context()))
				writeAPIError(w, http.StatusInternalServerError, "internal_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestIDMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("X-Request-ID")
		var id string
		switch len(values) {
		case 0:
			var random [16]byte
			if _, err := io.ReadFull(s.random, random[:]); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "entropy_unavailable")
				return
			}
			id = base64.RawURLEncoding.EncodeToString(random[:])
		case 1:
			id = values[0]
			if !validHeaderToken(id, 128) || strings.ContainsRune(id, ',') {
				writeAPIError(w, http.StatusBadRequest, "invalid_request_id")
				return
			}
		default:
			writeAPIError(w, http.StatusBadRequest, "invalid_request_id")
			return
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		pprof.Do(ctx, pprof.Labels("request_id", id), func(ctx context.Context) {
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
}

func validHeaderToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

// deadlineSupportMW fails closed before routing, reading a body, mutating the
// store, opening an SSE response, or starting a model/tool run when the
// underlying HTTP stack cannot enforce the endpoint deadlines.
func (s *Server) deadlineSupportMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(s.readTimeout)); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
			return
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
			return
		}
		if err := controller.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
			return
		}
		if err := controller.SetWriteDeadline(time.Time{}); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "deadline_unsupported")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func loggingMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s request_id=%q", r.Method, r.URL.Path, sw.code,
			time.Since(start).Round(time.Millisecond), requestIDFromContext(r.Context()))
	})
}

func (s *Server) securityMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if len(r.RequestURI) > 8<<10 || queryPairCount(r.URL.RawQuery) > 32 {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_target")
			return
		}
		rawPath := r.RequestURI
		if index := strings.IndexByte(rawPath, '?'); index >= 0 {
			rawPath = rawPath[:index]
		}
		if strings.HasPrefix(rawPath, "/api/") && (strings.ContainsRune(rawPath, '%') || strings.ContainsRune(rawPath, '\\')) {
			writeAPIError(w, http.StatusBadRequest, "invalid_session_id")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func queryPairCount(raw string) int {
	if raw == "" {
		return 0
	}
	return strings.Count(raw, "&") + 1
}

func (s *Server) requestAdmissionMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tryPermit(s.requestPermits) {
			w.Header().Set("Retry-After", "1")
			writeAPIError(w, http.StatusServiceUnavailable, "server_busy")
			return
		}
		defer releasePermit(s.requestPermits)
		next.ServeHTTP(w, r)
	})
}

func tryPermit(permits chan struct{}) bool {
	select {
	case permits <- struct{}{}:
		return true
	default:
		return false
	}
}

func releasePermit(permits chan struct{}) { <-permits }

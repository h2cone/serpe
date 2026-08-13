package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 1

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
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
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
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
		if strings.HasPrefix(rawPath, "/api/sessions/") && (strings.ContainsRune(rawPath, '%') || strings.ContainsRune(rawPath, '\\')) {
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

func (s *Server) authMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || r.URL.Path == "/api/health" || !s.authenticated {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 || strings.ContainsRune(values[0], ',') {
			unauthorized(w)
			return
		}
		value := values[0]
		space := strings.IndexByte(value, ' ')
		if space <= 0 || !strings.EqualFold(value[:space], "Bearer") || space+1 >= len(value) || strings.ContainsRune(value[space+1:], ' ') {
			unauthorized(w)
			return
		}
		token := value[space+1:]
		if validateBearerToken(token) != nil {
			unauthorized(w)
			return
		}
		digest := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(digest[:], s.authHash[:]) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeAPIError(w, http.StatusUnauthorized, "unauthorized")
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

func (s *Server) corsMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		_, allowed := s.origins[origin]
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if !allowed {
				writeAPIError(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", "X-Serpe-Next-Cursor, X-Request-ID")
		}
		if r.Method == http.MethodOptions {
			if origin == "" || !allowed || !validPreflight(r) {
				writeAPIError(w, http.StatusForbidden, "preflight_not_allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-Request-ID")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validPreflight(r *http.Request) bool {
	method := r.Header.Get("Access-Control-Request-Method")
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	allowed := map[string]struct{}{
		"authorization": {}, "content-type": {}, "accept": {}, "x-request-id": {},
	}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		header = strings.ToLower(strings.TrimSpace(header))
		if header == "" {
			continue
		}
		if _, ok := allowed[header]; !ok {
			return false
		}
	}
	return true
}

func normalizeOrigins(values []string) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeOrigin(value)
		if err != nil || normalized != value {
			return nil, &configError{field: "AllowedOrigins"}
		}
		out[normalized] = struct{}{}
	}
	return out, nil
}

func normalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Host == "" {
		return "", errInvalidOrigin
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errInvalidOrigin
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return "", errInvalidOrigin
	}
	if scheme == "http" {
		ip := net.ParseIP(hostname)
		if ip == nil || !ip.IsLoopback() {
			return "", errInvalidOrigin
		}
	}
	host := strings.ToLower(parsed.Host)
	return scheme + "://" + host, nil
}

type configError struct{ field string }

func (e *configError) Error() string { return "httpapi: invalid " + e.field }

var errInvalidOrigin = &configError{field: "origin"}

func sortedOriginKeys(origins map[string]struct{}) []string {
	keys := make([]string, 0, len(origins))
	for key := range origins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

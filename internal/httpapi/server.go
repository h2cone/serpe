// Package httpapi exposes sessions and agent turns over HTTP (REST + SSE).
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/h2cone/serpe/runtime"
	"github.com/h2cone/serpe/compose"
	"github.com/h2cone/serpe/runtime/sessions"
)

// Config constructs a Server.
type Config struct {
	Runner  *runtime.Runner     // required: agent execution
	Manager *sessions.Manager // required: sessions and turn persistence
	CWD     string            // default cwd for new sessions; required non-empty
	NewID   func() string     // optional session ID generator
}

// Server is the HTTP surface for sessions and streaming runs.
type Server struct {
	turns *compose.TurnService
	mgr   *sessions.Manager
	cwd   string
	newID func() string
}

// New validates cfg and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Runner == nil {
		return nil, fmt.Errorf("httpapi: Runner is required")
	}
	if cfg.Manager == nil {
		return nil, fmt.Errorf("httpapi: Manager is required")
	}
	if strings.TrimSpace(cfg.CWD) == "" {
		return nil, fmt.Errorf("httpapi: CWD is required")
	}
	newID := cfg.NewID
	if newID == nil {
		newID = defaultNewID
	}
	turns, err := compose.New(compose.Config{Runner: cfg.Runner, Manager: cfg.Manager})
	if err != nil {
		return nil, fmt.Errorf("httpapi: compose turns: %w", err)
	}
	return &Server{turns: turns, mgr: cfg.Manager, cwd: cfg.CWD, newID: newID}, nil
}

// Handler returns the fully wired HTTP handler (middleware + routes).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.handlePatchSession)
	mux.HandleFunc("POST /api/sessions/{id}/fork", s.handleForkSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/runs", s.handleRun)
	// Content-Type is set explicitly by writeJSON / SSE handlers; no jsonMW interceptor.
	return chain(mux, recoverMW, requestIDMW, loggingMW, corsMW)
}

// ListenAndServe listens on addr with the wired handler.
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}

func defaultNewID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

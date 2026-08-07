// Package server exposes sessions and agent turns over HTTP (REST + SSE).
package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/h2cone/ouro/compose"
	"github.com/h2cone/ouro/core/sessions"
)

// Config constructs a Server.
type Config struct {
	Turns   *compose.TurnService // required: runs path
	Manager *sessions.Manager    // required: session CRUD
	CWD     string               // default cwd for new sessions; required non-empty
	NewID   func() string        // optional session ID generator
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
	if cfg.Turns == nil {
		return nil, fmt.Errorf("server: Turns is required")
	}
	if cfg.Manager == nil {
		return nil, fmt.Errorf("server: Manager is required")
	}
	if strings.TrimSpace(cfg.CWD) == "" {
		return nil, fmt.Errorf("server: CWD is required")
	}
	newID := cfg.NewID
	if newID == nil {
		newID = defaultNewID
	}
	return &Server{turns: cfg.Turns, mgr: cfg.Manager, cwd: cfg.CWD, newID: newID}, nil
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

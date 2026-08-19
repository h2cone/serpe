// Package httpapi exposes sessions and agent turns over HTTP (REST + SSE).
package httpapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/h2cone/serpe/compose"
	"github.com/h2cone/serpe/internal/workdir"
	"github.com/h2cone/serpe/runtime/loops"
	"github.com/h2cone/serpe/runtime/sessions"
)

// Config constructs a Server.
type Config struct {
	Runner         *loops.Runner     // required: agent execution
	Manager        *sessions.Manager // required: sessions and turn persistence
	CWD            string            // default cwd for new sessions; required non-empty
	NewID          func() string
	ListenAddress  string
	Random         io.Reader
	Limits         ServerLimits
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	PickWorkingDir func(context.Context, string) (string, error)
}

// ServerLimits are process-wide admission ceilings. Zero fields use the
// defaults; positive fields may only tighten them.
type ServerLimits struct {
	MaxConnections    int
	MaxRequests       int
	MaxRuns           int
	MaxSessionEncodes int
}

// Server is the HTTP surface for sessions and streaming runs.
type Server struct {
	turns          *compose.TurnService
	mgr            *sessions.Manager
	cwd            string
	newID          func() string
	random         io.Reader
	cursors        *cursorCodec
	limits         ServerLimits
	requestPermits chan struct{}
	runPermits     chan struct{}
	encodePermits  chan struct{}
	readTimeout    time.Duration
	writeTimeout   time.Duration
	listenAddress  string
	listenLoopback bool
	pickWorkingDir func(context.Context, string) (string, error)
	picking        atomic.Bool
}

// New validates cfg and returns a Server.
func New(cfg Config) (*Server, error) {
	if cfg.Runner == nil {
		return nil, fmt.Errorf("httpapi: Runner is required")
	}
	if cfg.Manager == nil {
		return nil, fmt.Errorf("httpapi: Manager is required")
	}
	if cfg.CWD == "" || !filepath.IsAbs(cfg.CWD) {
		return nil, fmt.Errorf("httpapi: CWD is required")
	}
	listenAddress := cfg.ListenAddress
	if listenAddress == "" {
		listenAddress = "127.0.0.1:18080"
	}
	loopback, err := classifyListenAddress(listenAddress)
	if err != nil {
		return nil, err
	}
	limits, err := normalizeServerLimits(cfg.Limits)
	if err != nil {
		return nil, err
	}
	readTimeout, err := normalizeHTTPTimeout(cfg.ReadTimeout, "ReadTimeout")
	if err != nil {
		return nil, err
	}
	writeTimeout, err := normalizeHTTPTimeout(cfg.WriteTimeout, "WriteTimeout")
	if err != nil {
		return nil, err
	}
	random := cfg.Random
	if random == nil {
		random = rand.Reader
	}
	cursors, err := newCursorCodec(random)
	if err != nil {
		return nil, fmt.Errorf("httpapi: cursor entropy: %w", err)
	}
	if err := workdir.Check(context.Background(), cfg.CWD); err != nil {
		return nil, fmt.Errorf("httpapi: invalid startup CWD: %w", err)
	}
	turns, err := compose.New(compose.Config{Runner: cfg.Runner, Manager: cfg.Manager})
	if err != nil {
		return nil, fmt.Errorf("httpapi: compose turns: %w", err)
	}
	server := &Server{
		turns: turns, mgr: cfg.Manager, cwd: filepath.Clean(cfg.CWD), newID: cfg.NewID,
		random: random, cursors: cursors,
		limits: limits, requestPermits: make(chan struct{}, limits.MaxRequests),
		runPermits:    make(chan struct{}, limits.MaxRuns),
		encodePermits: make(chan struct{}, limits.MaxSessionEncodes),
		readTimeout:   readTimeout, writeTimeout: writeTimeout,
		listenAddress: listenAddress, listenLoopback: loopback,
		pickWorkingDir: cfg.PickWorkingDir,
	}
	return server, nil
}

// GateListener verifies the bound address and places the process-wide active
// connection admission gate outside HTTP and TLS handling.
func (s *Server) GateListener(listener net.Listener) (net.Listener, error) {
	if s == nil || listener == nil {
		return nil, fmt.Errorf("httpapi: listener is required")
	}
	configuredHost, configuredPort, err := net.SplitHostPort(s.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("httpapi: invalid configured listener")
	}
	actual, ok := listener.Addr().(*net.TCPAddr)
	if !ok || actual.IP == nil {
		return nil, fmt.Errorf("httpapi: listener must be TCP on an IP literal")
	}
	configuredIP := net.ParseIP(configuredHost)
	if configuredIP == nil || !configuredIP.Equal(actual.IP) || actual.IP.IsLoopback() != s.listenLoopback {
		return nil, fmt.Errorf("httpapi: bound listener address does not match validated configuration")
	}
	if configuredPort != "0" && configuredPort != fmt.Sprintf("%d", actual.Port) {
		return nil, fmt.Errorf("httpapi: bound listener port does not match validated configuration")
	}
	return newGatedListener(listener, s.limits.MaxConnections), nil
}

type gatedListener struct {
	net.Listener
	permits chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newGatedListener(listener net.Listener, maximum int) *gatedListener {
	return &gatedListener{Listener: listener, permits: make(chan struct{}, maximum), done: make(chan struct{})}
}

func (l *gatedListener) Accept() (net.Conn, error) {
	select {
	case l.permits <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.permits
		return nil, err
	}
	return &gatedConnection{Conn: connection, release: func() { <-l.permits }}, nil
}

func (l *gatedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type gatedConnection struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *gatedConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
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
	mux.HandleFunc("POST /api/workdir", s.handlePickWorkingDir)
	// Content-Type is set explicitly by writeJSON / SSE handlers; no jsonMW interceptor.
	return chain(mux, recoverMW, s.requestIDMW, s.deadlineSupportMW, loggingMW, s.securityMW, s.requestAdmissionMW)
}

// ListenAndServe listens on addr with the wired handler.
func (s *Server) ListenAndServe(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	gated, err := s.GateListener(listener)
	if err != nil {
		_ = listener.Close()
		return err
	}
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
	return server.Serve(gated)
}

func classifyListenAddress(address string) (bool, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false, fmt.Errorf("httpapi: ListenAddress must be an IP literal and port")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, fmt.Errorf("httpapi: ListenAddress host must be an IP literal")
	}
	return ip.IsLoopback(), nil
}

func normalizeServerLimits(limits ServerLimits) (ServerLimits, error) {
	values := []struct {
		value   *int
		ceiling int
		name    string
	}{
		{&limits.MaxConnections, 256, "MaxConnections"}, {&limits.MaxRequests, 64, "MaxRequests"},
		{&limits.MaxRuns, 8, "MaxRuns"}, {&limits.MaxSessionEncodes, 4, "MaxSessionEncodes"},
	}
	for _, item := range values {
		if *item.value < 0 || *item.value > item.ceiling {
			return ServerLimits{}, fmt.Errorf("httpapi: %s must be between 1 and %d", item.name, item.ceiling)
		}
		if *item.value == 0 {
			*item.value = item.ceiling
		}
	}
	return limits, nil
}

func normalizeHTTPTimeout(value time.Duration, name string) (time.Duration, error) {
	if value == 0 {
		return 30 * time.Second, nil
	}
	if value < 5*time.Second || value > 2*time.Minute {
		return 0, fmt.Errorf("httpapi: %s must be between 5 seconds and 2 minutes", name)
	}
	return value, nil
}

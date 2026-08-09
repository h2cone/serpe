// Command serpe-server runs the shared HTTP backend for Serpe clients.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/h2cone/serpe/core/sessions"
	"github.com/h2cone/serpe/internal/bootstrap"
	"github.com/h2cone/serpe/internal/httpapi"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(os.Stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr := envOr("SERPE_ADDR", ":8080")
	cwd := envOr("SERPE_CWD", must(os.Getwd()))
	storeRoot := os.Getenv("SERPE_SESSIONS_DIR")

	var store sessions.Store
	if storeRoot != "" {
		if err := os.MkdirAll(storeRoot, 0o755); err != nil {
			log.Fatal(err)
		}
		store = must(sessions.NewFileStore(storeRoot))
		log.Printf("sessions: FileStore %s", storeRoot)
	} else {
		store = sessions.NewMemoryStore()
		log.Print("sessions: MemoryStore")
	}

	mgr := must(sessions.NewManager(store))

	runner := must(bootstrap.NewRunner(bootstrap.RunnerConfigFromEnv()))
	srv := must(httpapi.New(httpapi.Config{Runner: runner, Manager: mgr, CWD: cwd}))

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	go func() {
		<-ctx.Done()
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("listening on %s (cwd=%s)", addr, filepath.Clean(cwd))
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must[T any](v T, err error) T {
	if err != nil {
		log.Fatal(err)
	}
	return v
}

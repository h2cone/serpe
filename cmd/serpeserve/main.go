// Command serpeserve is the HTTP entrypoint for the serpe agent API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/h2cone/serpe/agent"
	"github.com/h2cone/serpe/compose"
	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/providers"
	"github.com/h2cone/serpe/core/sessions"
	"github.com/h2cone/serpe/server"
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

	provider := must(providers.New(providers.Config{
		Protocol: providers.OpenAIResponses,
		APIKey:   os.Getenv("OPENAI_API_KEY"),
		BaseURL:  os.Getenv("OPENAI_BASE_URL"),
	}))
	modelName := os.Getenv("OPENAI_DEFAULT_MODEL")
	if modelName == "" {
		modelName = "gpt-4.1-mini"
	}
	model := must(provider.Model(modelName))

	runner := must(agent.NewRunner(agent.Config{
		Model: model,
		Tools: []agent.Tool{nowTool{}},
	}))
	turns := must(compose.New(compose.Config{Runner: runner, Manager: mgr}))
	srv := must(server.New(server.Config{Turns: turns, Manager: mgr, CWD: cwd}))

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

type nowTool struct{}

func (nowTool) Definition() models.Tool {
	return models.NewTool("now", "Current wall-clock time in RFC 3339.", json.RawMessage(`{"type":"object","properties":{}}`))
}

func (nowTool) Execute(_ context.Context, _ json.RawMessage) (agent.ToolOutput, error) {
	return agent.TextResult(time.Now().Format(time.RFC3339)), nil
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

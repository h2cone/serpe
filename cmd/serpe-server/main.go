// Command serpe-server runs the shared HTTP backend for Serpe clients.
package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/core/tools/builtin"
	"github.com/h2cone/serpe/internal/bootstrap"
	"github.com/h2cone/serpe/internal/httpapi"
	"github.com/h2cone/serpe/internal/workdir"
	"github.com/h2cone/serpe/runtime/sessions"
)

type options struct {
	listen    string
	cwd       string
	storeRoot string
	tools     string
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetOutput(os.Stderr)
	if err := run(); err != nil {
		log.Printf("serpe-server: %v", err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	if len(os.Args) > 1 && os.Args[1] == "migrate-store" {
		return runMigrateStore(os.Args[2:])
	}
	opts, err := parseOptions()
	if err != nil {
		return err
	}
	cwd, err := filepath.Abs(opts.cwd)
	if err != nil {
		return fmt.Errorf("resolve CWD: %w", err)
	}

	store, err := openSessionStore(opts.storeRoot)
	if err != nil {
		return err
	}
	manager, err := sessions.NewManager(store, sessions.Limits{})
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("construct session manager: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, manager.Close()) }()

	localTools, err := resolveLocalTools(opts.tools)
	if err != nil {
		return err
	}
	runnerConfig := bootstrap.RunnerConfigFromEnv()
	runnerConfig.Tools = localTools
	runner, err := bootstrap.NewRunner(runnerConfig)
	if err != nil {
		return err
	}

	api, err := httpapi.New(httpapi.Config{
		Runner: runner, Manager: manager, CWD: cwd,
		ListenAddress:  opts.listen,
		PickWorkingDir: workdir.Pick,
	})
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.listen, err)
	}
	gated, err := api.GateListener(listener)
	if err != nil {
		_ = listener.Close()
		return err
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{
		Handler:             api.Handler(),
		ReadHeaderTimeout:   5 * time.Second,
		IdleTimeout:         2 * time.Minute,
		MaxHeaderBytes:      32 << 10,
		MaxHeaderValueCount: 32,
		BaseContext: func(net.Listener) context.Context {
			return rootContext
		},
	}
	shutdownDone := make(chan error, 1)
	go func() {
		<-rootContext.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownDone <- httpServer.Shutdown(shutdownContext)
	}()

	log.Printf("listening on %s (cwd=%s tools=%d)", gated.Addr(), filepath.Clean(cwd), len(runner.ToolDefinitions()))
	serveErr := httpServer.Serve(gated)
	stop()
	shutdownErr := <-shutdownDone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		returnErr = errors.Join(returnErr, serveErr)
	}
	if shutdownErr != nil && !errors.Is(shutdownErr, http.ErrServerClosed) {
		returnErr = errors.Join(returnErr, shutdownErr)
	}
	return returnErr
}

func runMigrateStore(arguments []string) error {
	flags := flag.NewFlagSet("migrate-store", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options sessions.MaintenanceOptions
	flags.StringVar(&options.StoreRoot, "store-root", "", "absolute FileStore root")
	flags.StringVar(&options.CWDBase, "cwd-base", "", "absolute base for legacy relative CWD values")
	flags.BoolVar(&options.Apply, "apply", false, "create a verified backup and apply the migration")
	flags.StringVar(&options.RestoreManifest, "restore", "", "absolute backup manifest to restore")
	flags.StringVar(&options.CleanupManifest, "cleanup", "", "absolute backup manifest to verify and remove")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("migrate-store flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("migrate-store does not accept positional arguments")
	}
	result, err := sessions.MaintainFileStore(context.Background(), options)
	if encodeErr := jsonv2.MarshalWrite(os.Stdout, result, jsonv2.Deterministic(true)); encodeErr != nil {
		return errors.Join(err, fmt.Errorf("write migration report: %w", encodeErr))
	}
	if _, writeErr := os.Stdout.Write([]byte{'\n'}); writeErr != nil {
		return errors.Join(err, fmt.Errorf("write migration report: %w", writeErr))
	}
	return err
}

func parseOptions() (options, error) {
	defaultCWD, err := os.Getwd()
	if err != nil {
		return options{}, err
	}
	var opts options
	flag.StringVar(&opts.listen, "listen", envOr("SERPE_ADDR", "127.0.0.1:18080"), "IP-literal listen address and port")
	flag.StringVar(&opts.cwd, "cwd", envOr("SERPE_CWD", defaultCWD), "default session working directory")
	flag.StringVar(&opts.storeRoot, "sessions-dir", os.Getenv("SERPE_SESSIONS_DIR"), "private session store directory")
	flag.StringVar(&opts.tools, "tools", os.Getenv("SERPE_TOOLS"), "comma-separated subset of read,write,edit,bash; empty enables all four; none disables local tools")
	flag.Parse()
	if flag.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments")
	}
	return opts, nil
}

func openSessionStore(root string) (sessions.Store, error) {
	if root == "" {
		log.Print("sessions: MemoryStore")
		return sessions.NewMemoryStore(), nil
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("session store path must be absolute")
	}
	absolute := filepath.Clean(root)
	store, err := sessions.NewFileStore(absolute)
	if err != nil {
		return nil, err
	}
	log.Printf("sessions: FileStore %s", absolute)
	return store, nil
}

func resolveLocalTools(spec string) ([]tools.Tool, error) {
	if spec == "none" {
		return nil, nil
	}
	var names []string
	if spec != "" {
		var err error
		names, err = parseToolNames(spec)
		if err != nil {
			return nil, err
		}
	}
	set, err := builtin.NewDefault()
	if err != nil {
		return nil, err
	}
	if spec == "" {
		return set.Tools(), nil
	}
	return set.Select(names)
}

func parseToolNames(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return nil, fmt.Errorf("invalid --tools list")
		}
		if part == "none" {
			return nil, fmt.Errorf("--tools=none cannot be combined with other names")
		}
	}
	return parts, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

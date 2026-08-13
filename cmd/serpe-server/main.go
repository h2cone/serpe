// Command serpe-server runs the shared HTTP backend for Serpe clients.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/h2cone/serpe/internal/bootstrap"
	"github.com/h2cone/serpe/internal/httpapi"
	"github.com/h2cone/serpe/internal/securefs"
	"github.com/h2cone/serpe/runtime/sessions"
)

type options struct {
	listen         string
	cwd            string
	storeRoot      string
	tokenFile      string
	insecureNoAuth bool
	tlsCert        string
	tlsKey         string
	origins        string
	tools          string
	workspaceRoots string
	enableBash     bool
	bashPath       string
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

	runnerConfig := bootstrap.RunnerConfigFromEnv()
	runnerConfig.ToolProfile, err = serverToolProfile(opts)
	if err != nil {
		return err
	}
	runner, access, err := bootstrap.NewRunner(runnerConfig)
	if err != nil {
		return err
	}
	if err := access.Validate(context.Background(), cwd); err != nil {
		return fmt.Errorf("validate startup CWD: %w", err)
	}

	token, err := readTokenFile(opts.tokenFile)
	if err != nil {
		return err
	}
	tlsConfig, err := loadTLSConfig(opts.tlsCert, opts.tlsKey)
	if err != nil {
		return err
	}
	api, err := httpapi.New(httpapi.Config{
		Runner: runner, Manager: manager, CWD: cwd,
		BindWorkingDir: access.Bind, ValidateWorkingDir: access.Validate,
		ListenAddress: opts.listen, TLSConfigured: tlsConfig != nil,
		BearerToken: token, AllowInsecureNoAuth: opts.insecureNoAuth,
		AllowedOrigins: splitCommaList(opts.origins),
	})
	if err != nil {
		return err
	}
	if opts.insecureNoAuth {
		log.Print("WARNING: insecure no-auth development mode exposes transcripts and provider quota to every local process")
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
	if tlsConfig != nil {
		gated = tls.NewListener(gated, tlsConfig)
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpServer := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
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

	log.Printf("listening on %s (cwd=%s tls=%t tools=%d)", gated.Addr(), filepath.Clean(cwd), tlsConfig != nil, len(runner.ToolDefinitions()))
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
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if encodeErr := encoder.Encode(result); encodeErr != nil {
		return errors.Join(err, fmt.Errorf("write migration report: %w", encodeErr))
	}
	return err
}

func parseOptions() (options, error) {
	defaultCWD, err := os.Getwd()
	if err != nil {
		return options{}, err
	}
	var opts options
	flag.StringVar(&opts.listen, "listen", envOr("SERPE_ADDR", "127.0.0.1:8080"), "IP-literal listen address and port")
	flag.StringVar(&opts.cwd, "cwd", envOr("SERPE_CWD", defaultCWD), "default session working directory")
	flag.StringVar(&opts.storeRoot, "sessions-dir", os.Getenv("SERPE_SESSIONS_DIR"), "private session store directory")
	flag.StringVar(&opts.tokenFile, "api-token-file", os.Getenv("SERPE_API_TOKEN_FILE"), "absolute file containing the bearer token")
	flag.BoolVar(&opts.insecureNoAuth, "insecure-no-auth", envBool("SERPE_INSECURE_NO_AUTH"), "allow unauthenticated loopback development with zero tools")
	flag.StringVar(&opts.tlsCert, "tls-cert", os.Getenv("SERPE_TLS_CERT"), "absolute TLS certificate path")
	flag.StringVar(&opts.tlsKey, "tls-key", os.Getenv("SERPE_TLS_KEY"), "absolute TLS private-key path")
	flag.StringVar(&opts.origins, "allowed-origins", os.Getenv("SERPE_ALLOWED_ORIGINS"), "comma-separated canonical browser origins")
	flag.StringVar(&opts.tools, "tools", os.Getenv("SERPE_TOOLS"), "comma-separated local tools: read,write,edit,bash")
	flag.StringVar(&opts.workspaceRoots, "workspace-roots", os.Getenv("SERPE_WORKSPACE_ROOTS"), "OS path-list of authorized workspace roots")
	flag.BoolVar(&opts.enableBash, "enable-bash", envBool("SERPE_ENABLE_BASH"), "independent high-risk opt-in for bash")
	flag.StringVar(&opts.bashPath, "bash-path", os.Getenv("SERPE_BASH_PATH"), "absolute trusted Bash executable")
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

func serverToolProfile(opts options) (*bootstrap.ToolProfile, error) {
	enabled := splitCommaList(opts.tools)
	if len(enabled) == 0 {
		if opts.enableBash || opts.bashPath != "" || opts.workspaceRoots != "" {
			return nil, fmt.Errorf("tool-specific options require --tools")
		}
		return nil, nil
	}
	hasBash := false
	for _, name := range enabled {
		if name == "bash" {
			hasBash = true
		}
	}
	if hasBash != opts.enableBash {
		return nil, fmt.Errorf("bash requires both --tools=bash and --enable-bash")
	}
	if hasBash && opts.bashPath == "" {
		return nil, fmt.Errorf("enabled bash requires --bash-path")
	}
	var roots []string
	if opts.workspaceRoots != "" {
		for _, root := range filepath.SplitList(opts.workspaceRoots) {
			absolute, err := filepath.Abs(root)
			if err != nil {
				return nil, err
			}
			roots = append(roots, absolute)
		}
	}
	return &bootstrap.ToolProfile{
		Enabled: enabled, WorkspaceRoots: roots, BashPath: opts.bashPath,
	}, nil
}

func readTokenFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("api token file: path must be absolute")
	}
	file, err := securefs.OpenRegular(filepath.Clean(path), true)
	if err != nil {
		return "", fmt.Errorf("api token file: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 4099))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		clear(raw)
		return "", errors.Join(readErr, closeErr)
	}
	defer clear(raw)
	if len(raw) > 4098 || bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return "", fmt.Errorf("api token file is invalid")
	}
	if bytes.HasSuffix(raw, []byte("\r\n")) {
		raw = raw[:len(raw)-2]
	} else if bytes.HasSuffix(raw, []byte("\n")) {
		raw = raw[:len(raw)-1]
	}
	if bytes.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("api token file must contain exactly one line")
	}
	return string(raw), nil
}

func loadTLSConfig(certificatePath, keyPath string) (*tls.Config, error) {
	if certificatePath == "" && keyPath == "" {
		return nil, nil
	}
	if certificatePath == "" || keyPath == "" {
		return nil, fmt.Errorf("both --tls-cert and --tls-key are required")
	}
	if !filepath.IsAbs(certificatePath) || !filepath.IsAbs(keyPath) {
		return nil, fmt.Errorf("TLS certificate and private-key paths must be absolute")
	}
	certificate, err := securefs.OpenRegular(filepath.Clean(certificatePath), false)
	if err != nil {
		return nil, fmt.Errorf("TLS certificate: %w", err)
	}
	defer certificate.Close()
	key, err := securefs.OpenRegular(filepath.Clean(keyPath), true)
	if err != nil {
		return nil, fmt.Errorf("TLS private key: %w", err)
	}
	defer key.Close()
	const maxTLSPEMBytes = 16 << 20
	certificatePEM, err := readBounded(certificate, maxTLSPEMBytes)
	if err != nil {
		return nil, fmt.Errorf("read TLS certificate: %w", err)
	}
	defer clear(certificatePEM)
	keyPEM, err := readBounded(key, maxTLSPEMBytes)
	if err != nil {
		return nil, fmt.Errorf("read TLS private key: %w", err)
	}
	defer clear(keyPEM)
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		clear(data)
		return nil, err
	}
	if int64(len(data)) > limit {
		clear(data)
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func splitCommaList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		if parts[i] == "" || strings.TrimSpace(parts[i]) != parts[i] {
			return []string{""}
		}
	}
	return parts
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	value := os.Getenv(key)
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return parsed
}

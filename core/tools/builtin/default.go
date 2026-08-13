package builtin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/h2cone/serpe/core/tools"
)

var setSequence atomic.Uint64

type rootSnapshot struct {
	path     string
	identity string
	depth    int
}

type bashSnapshot struct {
	path        string
	identity    string
	unavailable bool
}

type cursorCodec struct {
	aead        cipher.AEAD
	keyID       [16]byte
	noncePrefix [4]byte
	counterMu   sync.Mutex
	counter     uint64
	exhausted   bool
}

// Set is the immutable bundle of the four local tools and their workspace
// policy. Tools() order is always read, write, edit, bash.
type Set struct {
	id     uint64
	cfg    Config
	lim    Limits
	tools  []tools.Tool
	names  []string
	roots  []rootSnapshot
	bash   bashSnapshot
	cursor *cursorCodec
}

// NewDefault constructs the four builtin tools and pins every configured root,
// the Bash image (when available), and the process-local read cursor key.
func NewDefault(cfg Config) (*Set, error) {
	lim, err := normalizeLimits(cfg.Limits)
	if err != nil {
		return nil, err
	}
	timeout := cfg.BashTimeout
	if timeout == 0 {
		timeout = defaultBashTimeout
	}
	if timeout < minBashTimeout || timeout > maxBashTimeout {
		return nil, fmt.Errorf("builtin: BashTimeout must be between 1s and 10m")
	}
	environment := copyEnv(cfg.BashEnvironment)
	if err := validateBashEnv(environment); err != nil {
		return nil, err
	}
	roots, err := pinRoots(cfg.WorkspaceRoots, lim.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	bash, err := pinBash(cfg.BashPath, environment, lim.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	cursor, err := newCursorCodec(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("builtin: initialize read cursor key: %w", err)
	}

	cfg.WorkspaceRoots = make([]string, len(roots))
	for i := range roots {
		cfg.WorkspaceRoots[i] = roots[i].path
	}
	cfg.BashPath = bash.path
	cfg.BashTimeout = timeout
	cfg.BashEnvironment = environment
	cfg.Limits = lim
	s := &Set{
		id:     setSequence.Add(1),
		cfg:    cfg,
		lim:    lim,
		names:  []string{"read", "write", "edit", "bash"},
		roots:  roots,
		bash:   bash,
		cursor: cursor,
	}
	s.tools = []tools.Tool{readTool{set: s}, writeTool{set: s}, editTool{set: s}, bashTool{set: s}}
	return s, nil
}

func newCursorCodec(random io.Reader) (*cursorCodec, error) {
	var key [32]byte
	codec := new(cursorCodec)
	if _, err := io.ReadFull(random, key[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(random, codec.keyID[:]); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(random, codec.noncePrefix[:]); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key[:])
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return nil, err
	}
	codec.aead, err = cipher.NewGCM(block)
	return codec, err
}

func pinRoots(configured []string, maxPath int64) ([]rootSnapshot, error) {
	roots := make([]rootSnapshot, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for i, path := range configured {
		if err := checkPathString(path, maxPath); err != nil {
			return nil, fmt.Errorf("builtin: workspace root %d: %w", i, err)
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("builtin: workspace root %d must be absolute", i)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("builtin: resolve workspace root %d: %w", i, err)
		}
		file, err := os.Open(resolved)
		if err != nil {
			return nil, fmt.Errorf("builtin: open workspace root %d: %w", i, err)
		}
		info, statErr := file.Stat()
		identity, idErr := platformFileIdentity(file)
		closeErr := file.Close()
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("builtin: workspace root %d is not a directory", i)
		}
		if idErr != nil {
			return nil, fmt.Errorf("builtin: workspace root %d has no stable identity: %w", i, idErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("builtin: close workspace root %d: %w", i, closeErr)
		}
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("builtin: duplicate workspace root identity")
		}
		seen[identity] = struct{}{}
		roots = append(roots, rootSnapshot{
			path:     filepath.Clean(resolved),
			identity: identity,
			depth:    pathDepth(resolved),
		})
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].depth != roots[j].depth {
			return roots[i].depth > roots[j].depth
		}
		return roots[i].path < roots[j].path
	})
	return roots, nil
}

func pathDepth(path string) int {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	rest = strings.Trim(rest, string(os.PathSeparator))
	if rest == "" {
		return 0
	}
	return len(strings.Split(rest, string(os.PathSeparator)))
}

func pinBash(explicit string, environment map[string]string, maxPath int64) (bashSnapshot, error) {
	path := explicit
	if path != "" {
		if err := checkPathString(path, maxPath); err != nil {
			return bashSnapshot{}, fmt.Errorf("builtin: BashPath: %w", err)
		}
		if !filepath.IsAbs(path) {
			return bashSnapshot{}, fmt.Errorf("builtin: BashPath must be absolute")
		}
	} else {
		found, err := findBash(environment["PATH"])
		if err != nil {
			return bashSnapshot{}, err
		}
		if found == "" {
			return bashSnapshot{unavailable: true}, nil
		}
		path = found
	}
	path = filepath.Clean(path)
	if err := validateBashLaunchPlatform(); err != nil {
		return bashSnapshot{}, err
	}
	file, err := openPinnedBashFile(path)
	if err != nil {
		return bashSnapshot{}, fmt.Errorf("builtin: configured BashPath is unavailable: %w", err)
	}
	info, statErr := file.Stat()
	identity, idErr := platformFileIdentity(file)
	closeErr := file.Close()
	if statErr != nil || !info.Mode().IsRegular() {
		return bashSnapshot{}, fmt.Errorf("builtin: BashPath is not a regular file")
	}
	if info.Mode()&0o111 == 0 && runtime.GOOS != "windows" {
		return bashSnapshot{}, fmt.Errorf("builtin: BashPath is not executable")
	}
	if idErr != nil {
		return bashSnapshot{}, fmt.Errorf("builtin: BashPath has no stable identity: %w", idErr)
	}
	if closeErr != nil {
		return bashSnapshot{}, closeErr
	}
	return bashSnapshot{path: path, identity: identity}, nil
}

func findBash(pathValue string) (string, error) {
	if pathValue == "" {
		return "", nil
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" || !filepath.IsAbs(directory) {
			return "", fmt.Errorf("builtin: PATH entries must be non-empty absolute directories")
		}
		candidates := []string{"bash"}
		if runtime.GOOS == "windows" {
			candidates = []string{"bash.exe", "bash"}
		}
		for _, name := range candidates {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}
	return "", nil
}

// Tools returns a defensive copy of the four tools in fixed order.
func (s *Set) Tools() []tools.Tool {
	out := make([]tools.Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

// Select returns the named tools in the Set's fixed order.
func (s *Set) Select(names []string) ([]tools.Tool, error) {
	if len(names) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(names))
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("builtin: duplicate tool name %q", name)
		}
		seen[name] = struct{}{}
		known := false
		for _, candidate := range s.names {
			if candidate == name {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("builtin: unknown tool %q", name)
		}
		wanted[name] = struct{}{}
	}
	var selected []tools.Tool
	for i, name := range s.names {
		if _, ok := wanted[name]; ok {
			selected = append(selected, s.tools[i])
		}
	}
	return selected, nil
}

func copyEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

var bashEnvAllow = map[string]struct{}{
	"PATH": {}, "HOME": {}, "USER": {}, "LOGNAME": {},
	"TMPDIR": {}, "TMP": {}, "TEMP": {}, "TZ": {}, "TERM": {},
	"SYSTEMROOT": {}, "WINDIR": {}, "LANG": {}, "LC_ALL": {},
	"LC_CTYPE": {}, "LC_NUMERIC": {}, "LC_TIME": {}, "LC_COLLATE": {},
	"LC_MONETARY": {}, "LC_MESSAGES": {}, "LC_PAPER": {}, "LC_NAME": {},
	"LC_ADDRESS": {}, "LC_TELEPHONE": {}, "LC_MEASUREMENT": {}, "LC_IDENTIFICATION": {},
}

func validateBashEnv(environment map[string]string) error {
	seen := make(map[string]struct{}, len(environment))
	total := 1 // native block terminator
	for key, value := range environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("builtin: invalid environment entry")
		}
		upper := strings.ToUpper(key)
		if _, allowed := bashEnvAllow[upper]; !allowed {
			return fmt.Errorf("builtin: environment key %q is not allowed", key)
		}
		if _, duplicate := seen[upper]; duplicate {
			return fmt.Errorf("builtin: duplicate environment key %q", key)
		}
		seen[upper] = struct{}{}
		total += len(key) + 1 + len(value) + 1
		if total > 64<<10 {
			return fmt.Errorf("builtin: environment block exceeds 64 KiB")
		}
	}
	return nil
}

type boundWorkingDir struct {
	setID    uint64
	cwd      string
	resolved string
	identity string
	root     rootSnapshot
}

type boundWorkingDirKey struct{}

func boundFrom(ctx context.Context) (boundWorkingDir, bool) {
	if ctx == nil {
		return boundWorkingDir{}, false
	}
	bound, ok := ctx.Value(boundWorkingDirKey{}).(boundWorkingDir)
	return bound, ok
}

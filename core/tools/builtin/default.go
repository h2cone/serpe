package builtin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/h2cone/serpe/core/tools"
)

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

// Set is the immutable bundle of the four local tools. Tools() order is
// always read, write, edit, bash.
type Set struct {
	lim      limits
	tools    []tools.Tool
	names    []string
	bash     bashSnapshot
	bashOnce sync.Once
	cursor   *cursorCodec
}

// NewDefault constructs the four builtin tools and initializes the
// process-local read cursor key. Bash is pinned the first time the bash
// tool is selected or executed, so a messy PATH cannot block file tools.
func NewDefault() (*Set, error) {
	lim, err := normalizeLimits(limits{})
	if err != nil {
		return nil, err
	}
	cursor, err := newCursorCodec(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("builtin: initialize read cursor key: %w", err)
	}
	s := &Set{
		lim:    lim,
		names:  []string{"read", "write", "edit", "bash"},
		cursor: cursor,
	}
	s.tools = []tools.Tool{readTool{set: s}, writeTool{set: s}, editTool{set: s}, bashTool{set: s}}
	return s, nil
}

func (s *Set) ensureBash() {
	if s == nil {
		return
	}
	s.bashOnce.Do(func() {
		s.bash = pinBash()
	})
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

func pinBash() bashSnapshot {
	found := findBash(os.Getenv("PATH"))
	if found == "" {
		return bashSnapshot{unavailable: true}
	}
	path := filepath.Clean(found)
	if err := validateBashLaunchPlatform(); err != nil {
		return bashSnapshot{unavailable: true}
	}
	file, err := openPinnedBashFile(path)
	if err != nil {
		return bashSnapshot{unavailable: true}
	}
	info, statErr := file.Stat()
	identity, idErr := platformFileIdentity(file)
	closeErr := file.Close()
	if statErr != nil || !info.Mode().IsRegular() {
		return bashSnapshot{unavailable: true}
	}
	if info.Mode()&0o111 == 0 && runtime.GOOS != "windows" {
		return bashSnapshot{unavailable: true}
	}
	if idErr != nil || closeErr != nil {
		return bashSnapshot{unavailable: true}
	}
	return bashSnapshot{path: path, identity: identity}
}

func findBash(pathValue string) string {
	if pathValue == "" {
		return ""
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidates := []string{"bash"}
		if runtime.GOOS == "windows" {
			candidates = []string{"bash.exe", "bash"}
		}
		for _, name := range candidates {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
	}
	return ""
}

// Tools returns a defensive copy of the four tools in fixed order.
func (s *Set) Tools() []tools.Tool {
	s.ensureBash()
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
	if _, ok := wanted["bash"]; ok {
		s.ensureBash()
	}
	var selected []tools.Tool
	for i, name := range s.names {
		if _, ok := wanted[name]; ok {
			selected = append(selected, s.tools[i])
		}
	}
	return selected, nil
}

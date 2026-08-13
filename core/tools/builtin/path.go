package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

type targetMode uint8

const (
	targetRead targetMode = iota + 1
	targetWrite
	targetEdit
)

type resolvedTarget struct {
	path           string
	parent         string
	exists         bool
	identity       string
	parentIdentity string
	rootIdentity   string
	file           *os.File
	expected       []byte
	claims         []tools.Claim
}

func (r *resolvedTarget) close() error {
	if r == nil || r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// ValidateWorkingDir validates and pins an absolute working directory against
// this Set's authorized root identities.
func (s *Set) ValidateWorkingDir(ctx context.Context, cwd string) error {
	_, err := s.snapshotWorkingDir(ctx, cwd)
	return err
}

// BindWorkingDir validates cwd, attaches tools.Scope, and records the exact
// root/CWD identity snapshot that file Activators must re-check this turn.
func (s *Set) BindWorkingDir(ctx context.Context, cwd string) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, err := s.snapshotWorkingDir(ctx, cwd)
	if err != nil {
		return nil, err
	}
	ctx = tools.WithScope(ctx, tools.Scope{WorkingDir: filepath.Clean(cwd)})
	return context.WithValue(ctx, boundWorkingDirKey{}, bound), nil
}

func (s *Set) snapshotWorkingDir(ctx context.Context, cwd string) (boundWorkingDir, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return boundWorkingDir{}, err
	}
	if err := checkPathString(cwd, s.lim.MaxPathBytes); err != nil {
		return boundWorkingDir{}, fmt.Errorf("working directory: %w", err)
	}
	if !filepath.IsAbs(cwd) {
		return boundWorkingDir{}, fmt.Errorf("working directory must be absolute")
	}
	clean := filepath.Clean(cwd)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return boundWorkingDir{}, fmt.Errorf("working directory is not accessible")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return boundWorkingDir{}, fmt.Errorf("working directory is not accessible")
	}
	info, statErr := file.Stat()
	identity, idErr := platformFileIdentity(file)
	closeErr := file.Close()
	if statErr != nil || !info.IsDir() {
		return boundWorkingDir{}, fmt.Errorf("working directory is not a directory")
	}
	if idErr != nil {
		return boundWorkingDir{}, fmt.Errorf("working directory identity: %w", idErr)
	}
	if closeErr != nil {
		return boundWorkingDir{}, closeErr
	}
	root, err := s.selectRoot(ctx, resolved)
	if err != nil {
		return boundWorkingDir{}, err
	}
	return boundWorkingDir{setID: s.id, cwd: clean, resolved: filepath.Clean(resolved), identity: identity, root: root}, nil
}

func (s *Set) selectRoot(ctx context.Context, resolvedCWD string) (rootSnapshot, error) {
	if len(s.roots) == 0 {
		return rootSnapshot{}, nil
	}
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return rootSnapshot{}, err
		}
		if !pathWithin(root.path, resolvedCWD) {
			continue
		}
		file, err := os.Open(root.path)
		if err != nil {
			return rootSnapshot{}, fmt.Errorf("authorized workspace root is unavailable")
		}
		identity, idErr := platformFileIdentity(file)
		closeErr := file.Close()
		if idErr != nil || identity != root.identity {
			return rootSnapshot{}, fmt.Errorf("authorized workspace root identity changed")
		}
		if closeErr != nil {
			return rootSnapshot{}, closeErr
		}
		return root, nil
	}
	return rootSnapshot{}, fmt.Errorf("working directory is outside the authorized workspace")
}

func (s *Set) resolveTarget(ctx context.Context, in tools.Invocation, userPath string, mode targetMode) (*resolvedTarget, error) {
	bound, err := s.snapshotWorkingDir(ctx, in.Scope.WorkingDir)
	if err != nil {
		return nil, err
	}
	if expected, ok := boundFrom(ctx); ok {
		if expected.setID != s.id || expected.cwd != filepath.Clean(in.Scope.WorkingDir) ||
			expected.resolved != bound.resolved || expected.identity != bound.identity ||
			expected.root.identity != bound.root.identity {
			return nil, fmt.Errorf("working directory identity changed after binding")
		}
	}
	lexical, err := resolveUserPath(filepath.Clean(in.Scope.WorkingDir), userPath, s.lim.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &resolvedTarget{}
	result.rootIdentity = bound.root.identity
	if _, err := os.Lstat(lexical); err == nil {
		resolved, err := filepath.EvalSymlinks(lexical)
		if err != nil {
			return nil, fmt.Errorf("path could not be resolved")
		}
		resolved = filepath.Clean(resolved)
		if !pathWithin(bound.resolved, resolved) {
			return nil, fmt.Errorf("resolved path is outside the working directory")
		}
		file, err := os.Open(resolved)
		if err != nil {
			return nil, fmt.Errorf("path is not accessible")
		}
		info, statErr := file.Stat()
		identity, idErr := platformFileIdentity(file)
		if statErr != nil || idErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("path identity could not be fixed")
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, fmt.Errorf("path is not a regular file")
		}
		result.path = resolved
		result.parent = filepath.Dir(resolved)
		result.exists = true
		result.identity = identity
		result.file = file
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("path is not accessible")
	} else {
		if mode != targetWrite {
			return nil, fmt.Errorf("file not found")
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(lexical))
		if err != nil {
			return nil, fmt.Errorf("parent directory does not exist")
		}
		parent = filepath.Clean(parent)
		if !pathWithin(bound.resolved, parent) {
			return nil, fmt.Errorf("resolved parent is outside the working directory")
		}
		parentFile, err := os.Open(parent)
		if err != nil {
			return nil, fmt.Errorf("parent directory is not accessible")
		}
		info, statErr := parentFile.Stat()
		identity, idErr := platformFileIdentity(parentFile)
		closeErr := parentFile.Close()
		if statErr != nil || !info.IsDir() || idErr != nil {
			return nil, fmt.Errorf("parent directory identity could not be fixed")
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result.path = filepath.Join(parent, filepath.Base(lexical))
		result.parent = parent
		result.parentIdentity = identity
	}
	access := tools.AccessRead
	if mode != targetRead {
		access = tools.AccessWrite
	}
	result.claims = []tools.Claim{{Resource: digestResource("file:real:v1", result.path), Access: access}}
	if result.identity != "" {
		result.claims = append(result.claims, tools.Claim{Resource: digestResource("file:id:v1", result.identity), Access: access})
	}
	if !result.exists {
		result.claims = append(result.claims, tools.Claim{Resource: digestResource("file:parent:v1", result.parentIdentity), Access: tools.AccessWrite})
	}
	return result, nil
}

func digestResource(domain, value string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + value))
	return domain + ":" + hex.EncodeToString(sum[:])
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func workingDir(in tools.Invocation, maxPath int64) (string, error) {
	wd := in.Scope.WorkingDir
	if err := checkPathString(wd, maxPath); err != nil {
		return "", fmt.Errorf("working directory %s", err)
	}
	if !filepath.IsAbs(wd) {
		return "", fmt.Errorf("working directory must be an absolute path")
	}
	info, err := os.Stat(wd)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("working directory is not an accessible directory")
	}
	return filepath.Clean(wd), nil
}

func pathFail(err error) (tools.Output, error) { return tools.Error(err.Error()), nil }

func resolveUserPath(wd, user string, maxPath int64) (string, error) {
	if err := checkPathString(user, maxPath); err != nil {
		return "", err
	}
	if err := rejectWindowsUnsafe(user); err != nil {
		return "", err
	}
	var target string
	if filepath.IsAbs(user) {
		target = filepath.Clean(user)
	} else {
		target = filepath.Clean(filepath.Join(wd, user))
	}
	if !pathWithin(wd, target) {
		return "", fmt.Errorf("path is outside the working directory")
	}
	return target, nil
}

func rejectWindowsUnsafe(user string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if len(user) >= 2 && user[1] == ':' && (len(user) == 2 || (user[2] != '\\' && user[2] != '/')) {
		return fmt.Errorf("drive-relative paths are not allowed")
	}
	lower := strings.ToLower(user)
	if strings.HasPrefix(lower, `\\.\`) || strings.HasPrefix(lower, `\\?\`) || strings.HasPrefix(lower, "//./") || strings.HasPrefix(lower, "//?/") {
		return fmt.Errorf("Win32 device paths are not allowed")
	}
	for _, component := range strings.FieldsFunc(user, func(r rune) bool { return r == '\\' || r == '/' }) {
		if strings.Contains(component, ":") {
			// The volume separator is only valid in the first absolute component.
			if !(len(component) == 2 && component[1] == ':' && strings.HasPrefix(user, component)) {
				return fmt.Errorf("alternate data streams are not allowed")
			}
		}
		if strings.HasSuffix(component, " ") || strings.HasSuffix(component, ".") {
			return fmt.Errorf("trailing dots or spaces in names are not allowed")
		}
		stem := component
		if i := strings.IndexByte(stem, '.'); i >= 0 {
			stem = stem[:i]
		}
		switch strings.ToUpper(stem) {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return fmt.Errorf("reserved device names are not allowed")
		}
	}
	return nil
}

func objectString(value jsonvalue.Value, key string) (string, bool) {
	member, ok := value.Lookup(key)
	return member.String, ok && member.Kind == jsonvalue.KindString
}

func objectInt(value jsonvalue.Value, key string) (int64, bool, error) {
	member, ok := value.Lookup(key)
	if !ok {
		return 0, false, nil
	}
	if member.Kind != jsonvalue.KindNumber || member.Number == "" {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	var n int64
	for _, char := range member.Number {
		if char < '0' || char > '9' {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		digit := int64(char - '0')
		if n > (1<<63-1-digit)/10 {
			return 0, true, fmt.Errorf("%s is out of range", key)
		}
		n = n*10 + digit
	}
	return n, true, nil
}

func parseObject(in tools.Invocation) (jsonvalue.Value, error) {
	value, err := jsonvalue.ParseObject(in.Arguments, jsonvalue.ObjectLimits())
	if err != nil {
		return jsonvalue.Value{}, tools.Reject("invalid arguments")
	}
	return value, nil
}

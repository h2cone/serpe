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

func (s *Set) resolveTarget(ctx context.Context, in tools.Invocation, userPath string, mode targetMode) (*resolvedTarget, error) {
	wd, err := workingDir(in, s.lim.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	boundary, err := filepath.EvalSymlinks(wd)
	if err != nil {
		return nil, fmt.Errorf("working directory is not accessible")
	}
	boundary = filepath.Clean(boundary)
	lexical, err := resolveUserPath(wd, userPath, s.lim.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &resolvedTarget{}
	if _, err := os.Lstat(lexical); err == nil {
		resolved, err := filepath.EvalSymlinks(lexical)
		if err != nil {
			return nil, fmt.Errorf("path could not be resolved")
		}
		resolved = filepath.Clean(resolved)
		if !pathWithin(boundary, resolved) {
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
		if !pathWithin(boundary, parent) {
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

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/core/tools/builtin"
)

// ToolProfile is the entrypoint-owned authorization for local tools.
// A nil profile means deny-local-tools.
type ToolProfile struct {
	Enabled         []string
	WorkspaceRoots  []string
	BashPath        string
	BashTimeout     time.Duration
	BashEnvironment map[string]string
	BuiltinLimits   builtin.Limits
}

// WorkingDirAccess is the immutable CWD validator/binder returned with a Runner.
type WorkingDirAccess struct {
	Validate func(context.Context, string) error
	Bind     func(context.Context, string) (context.Context, error)
}

func defaultWorkingDirAccess() WorkingDirAccess {
	return WorkingDirAccess{
		Validate: validateAbsDir,
		Bind: func(ctx context.Context, cwd string) (context.Context, error) {
			if err := validateAbsDir(ctx, cwd); err != nil {
				return nil, err
			}
			if ctx == nil {
				ctx = context.Background()
			}
			return tools.WithScope(ctx, tools.Scope{WorkingDir: filepath.Clean(cwd)}), nil
		},
	}
}

const maxWorkingDirBytes = 32 << 10

func validateAbsDir(ctx context.Context, cwd string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cwd == "" || !utf8.ValidString(cwd) {
		return fmt.Errorf("working directory is required")
	}
	if len(cwd) > maxWorkingDirBytes {
		return fmt.Errorf("working directory exceeds %d bytes", maxWorkingDirBytes)
	}
	for _, r := range cwd {
		if unicode.IsControl(r) {
			return fmt.Errorf("working directory contains a control character")
		}
	}
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("working directory must be absolute")
	}
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(cwd)
		if strings.HasPrefix(lower, `\\.\`) || strings.HasPrefix(lower, `\\?\`) ||
			strings.HasPrefix(lower, "//./") || strings.HasPrefix(lower, "//?/") {
			return fmt.Errorf("working directory uses an unsupported device path")
		}
	}
	file, err := os.Open(filepath.Clean(cwd))
	if err != nil {
		return fmt.Errorf("working directory is not accessible")
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || !info.IsDir() {
		return fmt.Errorf("working directory is not a directory")
	}
	if closeErr != nil {
		return fmt.Errorf("close working directory: %w", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func buildTools(profile *ToolProfile, contributed []tools.Tool, executorConfig tools.Config) (*tools.Executor, WorkingDirAccess, error) {
	access := defaultWorkingDirAccess()
	var authorized []tools.Tool
	if profile != nil {
		normalized, err := normalizeProfile(*profile)
		if err != nil {
			return nil, WorkingDirAccess{}, err
		}
		enabled := normalized.Enabled
		roots := normalized.WorkspaceRoots
		for i, r := range roots {
			if !filepath.IsAbs(r) {
				return nil, WorkingDirAccess{}, fmt.Errorf("bootstrap: workspace root %d must be absolute", i)
			}
			roots[i] = filepath.Clean(r)
		}
		set, err := builtin.NewDefault(builtin.Config{
			WorkspaceRoots:  roots,
			BashPath:        normalized.BashPath,
			BashTimeout:     normalized.BashTimeout,
			BashEnvironment: normalized.BashEnvironment,
			Limits:          normalized.BuiltinLimits,
		})
		if err != nil {
			return nil, WorkingDirAccess{}, err
		}
		selected, err := set.Select(enabled)
		if err != nil {
			return nil, WorkingDirAccess{}, err
		}
		authorized = append(authorized, selected...)
		hasFile := false
		for _, n := range enabled {
			if n == "read" || n == "write" || n == "edit" {
				hasFile = true
			}
		}
		if hasFile {
			if len(roots) == 0 {
				return nil, WorkingDirAccess{}, fmt.Errorf("bootstrap: file tools require workspace roots")
			}
			access.Validate = func(ctx context.Context, cwd string) error {
				if err := validateAbsDir(ctx, cwd); err != nil {
					return err
				}
				return set.ValidateWorkingDir(ctx, cwd)
			}
			access.Bind = func(ctx context.Context, cwd string) (context.Context, error) {
				if err := validateAbsDir(ctx, cwd); err != nil {
					return nil, err
				}
				return set.BindWorkingDir(ctx, cwd)
			}
		}
	}
	authorized = append(authorized, append([]tools.Tool(nil), contributed...)...)
	if len(authorized) == 0 {
		return nil, access, nil
	}
	exec, err := tools.New(executorConfig, authorized...)
	if err != nil {
		return nil, WorkingDirAccess{}, err
	}
	return exec, access, nil
}

// LocalCLIProfile enables the four local tools against one workspace root.
func LocalCLIProfile(root string) *ToolProfile {
	return &ToolProfile{
		Enabled:         []string{"read", "write", "edit", "bash"},
		WorkspaceRoots:  []string{root},
		BashEnvironment: localCLIEnvironment(),
	}
}

func normalizeProfile(profile ToolProfile) (ToolProfile, error) {
	profile.Enabled = append([]string(nil), profile.Enabled...)
	profile.WorkspaceRoots = append([]string(nil), profile.WorkspaceRoots...)
	profile.BashEnvironment = cloneEnvironment(profile.BashEnvironment)

	enabled := make(map[string]bool, len(profile.Enabled))
	for _, name := range profile.Enabled {
		switch name {
		case "read", "write", "edit", "bash":
		default:
			return ToolProfile{}, fmt.Errorf("bootstrap: unknown local tool %q", name)
		}
		if enabled[name] {
			return ToolProfile{}, fmt.Errorf("bootstrap: duplicate local tool %q", name)
		}
		enabled[name] = true
	}
	hasFile := enabled["read"] || enabled["write"] || enabled["edit"]
	if !hasFile && len(profile.WorkspaceRoots) != 0 {
		return ToolProfile{}, fmt.Errorf("bootstrap: WorkspaceRoots require a file tool")
	}
	if hasFile && len(profile.WorkspaceRoots) == 0 {
		return ToolProfile{}, fmt.Errorf("bootstrap: file tools require workspace roots")
	}
	limits := profile.BuiltinLimits
	if len(profile.Enabled) == 0 && limits.MaxPathBytes != 0 {
		return ToolProfile{}, fmt.Errorf("bootstrap: MaxPathBytes requires a local tool")
	}
	if !enabled["read"] && limits.MaxReadScanBytes != 0 {
		return ToolProfile{}, fmt.Errorf("bootstrap: MaxReadScanBytes requires read")
	}
	if !enabled["write"] && !enabled["edit"] && limits.MaxWriteBytes != 0 {
		return ToolProfile{}, fmt.Errorf("bootstrap: MaxWriteBytes requires write or edit")
	}
	if !enabled["edit"] && (limits.MaxEditBytes != 0 || limits.MaxEditWorkBytes != 0) {
		return ToolProfile{}, fmt.Errorf("bootstrap: edit limits require edit")
	}
	if !enabled["bash"] {
		if profile.BashPath != "" || profile.BashTimeout != 0 || len(profile.BashEnvironment) != 0 ||
			limits.MaxBashCommand != 0 || limits.MaxBashScanBytes != 0 {
			return ToolProfile{}, fmt.Errorf("bootstrap: Bash configuration requires bash")
		}
	}
	return profile, nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	clone := make(map[string]string, len(environment))
	for key, value := range environment {
		clone[key] = value
	}
	return clone
}

var localEnvironmentKeys = [...]string{
	"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "TZ", "TERM",
	"SYSTEMROOT", "WINDIR", "LANG", "LC_ALL", "LC_CTYPE", "LC_NUMERIC", "LC_TIME",
	"LC_COLLATE", "LC_MONETARY", "LC_MESSAGES", "LC_PAPER", "LC_NAME", "LC_ADDRESS",
	"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION",
}

func localCLIEnvironment() map[string]string {
	environment := make(map[string]string)
	for _, key := range localEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok {
			environment[key] = value
		}
	}
	return environment
}

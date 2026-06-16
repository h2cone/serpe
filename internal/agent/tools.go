package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ToolExecutor runs the local tools advertised to the model.
type ToolExecutor struct {
	workingDir string
}

// NewToolExecutor creates a tool executor rooted at workingDir.
func NewToolExecutor(workingDir string) ToolExecutor {
	return ToolExecutor{workingDir: workingDir}
}

func (t ToolExecutor) execute(ctx context.Context, name string, arguments map[string]any) (string, error) {
	switch name {
	case "execute_shell":
		command, err := requiredString(arguments, "command")
		if err != nil {
			return "", err
		}
		return t.executeShell(ctx, command)
	case "read_file":
		path, err := requiredString(arguments, "path")
		if err != nil {
			return "", err
		}
		return t.readFile(path)
	case "write_file":
		path, err := requiredString(arguments, "path")
		if err != nil {
			return "", err
		}
		content, err := requiredString(arguments, "content")
		if err != nil {
			return "", err
		}
		return t.writeFile(path, content)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func (t ToolExecutor) executeShell(ctx context.Context, command string) (string, error) {
	cmd := shellCommand(ctx, command)
	cmd.Dir = t.workingDir

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err := cmd.Run()
	output := combined.String()
	if output == "" {
		output = exitStatusMessage(err)
	} else if err != nil {
		output += "\n[" + exitStatusMessage(err) + "]"
	}

	return output, nil
}

func (t ToolExecutor) readFile(path string) (string, error) {
	data, err := os.ReadFile(t.resolvePath(path))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t ToolExecutor) writeFile(path, content string) (string, error) {
	resolved := t.resolvePath(path)
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), resolved), nil
}

func (t ToolExecutor) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(t.workingDir, path)
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	}
	return exec.CommandContext(ctx, "sh", "-lc", command)
}

func exitStatusMessage(err error) string {
	if err == nil {
		return "Command exited with status 0"
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return fmt.Sprintf("exit status: %d", exitError.ExitCode())
	}
	return err.Error()
}

func requiredString(arguments map[string]any, field string) (string, error) {
	value, ok := arguments[field].(string)
	if !ok {
		return "", fmt.Errorf("missing string field %q", field)
	}
	return value, nil
}

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/tw8ap/ouro/internal/canon"
)

const (
	defaultBashTimeout = 120
	maxBashTimeout     = 600
	maxBashOutputBytes = 100 << 10
)

type bashTool struct{}

func (bashTool) Name() string { return "bash" }

func (bashTool) Definition() canon.Tool {
	return canon.Tool{
		Name:        "bash",
		Description: "Execute a shell command in the working directory with timeout and output limits.",
		Parameters:  jsonRaw(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute."},"timeout":{"type":["integer","null"],"description":"Timeout in seconds; null uses 120."}},"required":["command","timeout"],"additionalProperties":false}`),
	}
}

func (bashTool) Execute(ctx context.Context, env ToolContext, args map[string]any) (Result, error) {
	command, err := requiredString(args, "command")
	if err != nil {
		return Result{}, err
	}
	timeout, err := nullableInt(args, "timeout", defaultBashTimeout)
	if err != nil {
		return Result{}, err
	}
	if timeout < 1 {
		timeout = 1
	}
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := shellCommand(runCtx, command)
	cmd.Dir = env.WorkingDir
	cmd.WaitDelay = 5 * time.Second
	configureCommandProcessGroup(cmd)
	cmd.Cancel = func() error {
		return terminateCommandProcessGroup(cmd)
	}

	output := &boundedWriter{limit: maxBashOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output

	err = cmd.Run()
	text := output.String()
	if output.Truncated() {
		text += "\n... (truncated)"
	}

	switch {
	case runCtx.Err() != nil:
		text = appendStatus(text, fmt.Sprintf("exit status: timed out after %ds", timeout))
		return TextResult(text), nil
	case err == nil:
		if text == "" {
			text = "Command exited with status 0"
		}
		return TextResult(text), nil
	case isExitError(err):
		text = appendStatus(text, exitStatusMessage(err))
		return TextResult(text), nil
	default:
		return Result{}, err
	}
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

func isExitError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

func appendStatus(text, status string) string {
	if text == "" {
		return "[" + status + "]"
	}
	return text + "\n[" + status + "]"
}

type boundedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() < w.limit {
		remaining := w.limit - w.buf.Len()
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *boundedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

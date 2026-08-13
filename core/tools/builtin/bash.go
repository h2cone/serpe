package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

var bashSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Bash command passed to the pinned Bash image as --noprofile --norc -c. This is not a sandbox."}
  },
  "required": ["command"],
  "additionalProperties": false
}`)

type bashTool struct{ set *Set }

func (bashTool) Definition() models.Tool {
	return models.NewTool("bash", "Run a command with a startup-pinned Bash image and an explicit minimal environment. Bash has the full Serpe process identity, can access paths outside workspace roots, and is not a sandbox.", bashSchema)
}

func (t bashTool) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	value, err := parseObject(in)
	if err != nil {
		return tools.Output{}, err
	}
	commandText, ok := objectString(value, "command")
	if !ok {
		return tools.Output{}, tools.Reject("command is required")
	}
	if !utf8.ValidString(commandText) || bytes.IndexByte([]byte(commandText), 0) >= 0 {
		return tools.Output{}, tools.Reject("command must be valid UTF-8 without NUL")
	}
	if int64(len(commandText)) > t.set.lim.MaxBashCommand {
		return tools.Output{}, tools.Reject("command exceeds the size limit")
	}
	workingDirectory, err := workingDir(in, t.set.lim.MaxPathBytes)
	if err != nil {
		return pathFail(err)
	}
	bashFile, err := t.openPinnedBash()
	if err != nil {
		return tools.Error(err.Error()), nil
	}
	defer func() {
		if bashFile != nil {
			_ = bashFile.Close()
		}
	}()

	runCtx, cancel := context.WithTimeout(ctx, t.set.cfg.BashTimeout)
	defer cancel()
	command := exec.Command(t.set.bash.path, "--noprofile", "--norc", "-c", commandText)
	command.Dir = workingDirectory
	command.Env = flattenEnv(t.set.cfg.BashEnvironment)
	command.Stdin = nil
	if err := bindPinnedBashImage(command, bashFile); err != nil {
		return tools.Output{}, err
	}
	process, err := newProcessController(command)
	if err != nil {
		return tools.Output{}, err
	}
	stdout, stdoutChild, err := os.Pipe()
	if err != nil {
		return tools.Output{}, err
	}
	defer stdout.Close()
	stderr, stderrChild, err := os.Pipe()
	if err != nil {
		_ = stdoutChild.Close()
		return tools.Output{}, err
	}
	defer stderr.Close()
	command.Stdout = stdoutChild
	command.Stderr = stderrChild
	group, err := tools.NewTextCollectorGroup(in.OutputLimits, tools.HeadTail, "stdout", "stderr")
	if err != nil {
		_ = stdoutChild.Close()
		_ = stderrChild.Close()
		return tools.Output{}, err
	}
	if err := process.start(); err != nil {
		_ = stdoutChild.Close()
		_ = stderrChild.Close()
		return tools.Output{}, fmt.Errorf("builtin: start contained Bash: %w", err)
	}
	if err := errors.Join(stdoutChild.Close(), stderrChild.Close()); err != nil {
		process.terminate()
		_ = command.Wait()
		return tools.Output{}, fmt.Errorf("builtin: close parent Bash pipe handles: %w", err)
	}
	// The image identity is fixed through Start. The portable exec API cannot
	// execute this handle directly, but keeping it open and rechecking just
	// before Start closes the ordinary replacement race on supported filesystems.
	if err := bashFile.Close(); err != nil {
		process.terminate()
		_ = command.Wait()
		return tools.Output{}, err
	}
	bashFile = nil

	limit := &sharedScanLimit{remaining: t.set.lim.MaxBashScanBytes, cancel: cancel}
	pipeDone := make(chan bashPipeResult, 2)
	go func() { pipeDone <- bashPipeResult{err: copyPipe(group, "stdout", stdout, limit)} }()
	go func() { pipeDone <- bashPipeResult{err: copyPipe(group, "stderr", stderr, limit)} }()

	monitorDone := make(chan struct{})
	monitorExited := make(chan struct{})
	go func() {
		defer close(monitorExited)
		select {
		case <-runCtx.Done():
			process.terminate()
		case <-monitorDone:
		}
	}()
	waitErr := command.Wait()
	close(monitorDone)
	containmentDone := make(chan struct{})
	go func() {
		process.terminate()
		close(containmentDone)
	}()
	firstPipe, secondPipe := drainBashPipes(stdout, stderr, pipeDone)
	<-monitorExited
	<-containmentDone
	if firstPipe.err != nil && !errors.Is(firstPipe.err, errBashOutputLimit) {
		return tools.Output{}, firstPipe.err
	}
	if secondPipe.err != nil && !errors.Is(secondPipe.err, errBashOutputLimit) {
		return tools.Output{}, secondPipe.err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tools.Output{}, ctxErr
	}

	exitCode := 0
	reason := "exited"
	isError := false
	if limit.exceeded() {
		exitCode = -1
		reason = "output_limit"
		isError = true
	} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		exitCode = -1
		reason = "timeout"
		isError = true
	} else if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
			isError = true
		} else {
			return tools.Error("bash process failed"), nil
		}
	}
	metadata, _ := json.Marshal(map[string]any{
		"exit_code":   exitCode,
		"termination": reason,
	})
	return group.Output(metadata, isError)
}

type bashPipeResult struct{ err error }

func drainBashPipes(stdout, stderr *os.File, done <-chan bashPipeResult) (bashPipeResult, bashPipeResult) {
	results := [2]bashPipeResult{}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for index := range results {
		select {
		case results[index] = <-done:
		case <-timer.C:
			_ = stdout.Close()
			_ = stderr.Close()
			for ; index < len(results); index++ {
				results[index] = <-done
			}
			return results[0], results[1]
		}
	}
	return results[0], results[1]
}

func (t bashTool) openPinnedBash() (*os.File, error) {
	if t.set.bash.unavailable || t.set.bash.path == "" {
		return nil, errors.New("bash was unavailable at startup; restart after configuring BashPath or PATH")
	}
	file, err := openPinnedBashFile(t.set.bash.path)
	if err != nil {
		return nil, errors.New("the pinned Bash image is unavailable; inspect configuration and restart")
	}
	info, statErr := file.Stat()
	identity, idErr := platformFileIdentity(file)
	if statErr != nil || !info.Mode().IsRegular() || idErr != nil || identity != t.set.bash.identity {
		_ = file.Close()
		return nil, errors.New("the pinned Bash image identity changed; inspect configuration and restart")
	}
	return file, nil
}

func flattenEnv(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return stringsLessFold(keys[i], keys[j]) })
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+environment[key])
	}
	return out
}

func stringsLessFold(left, right string) bool {
	upperLeft, upperRight := strings.ToUpper(left), strings.ToUpper(right)
	if upperLeft == upperRight {
		return left < right
	}
	return upperLeft < upperRight
}

var errBashOutputLimit = errors.New("bash output scan limit exceeded")

type sharedScanLimit struct {
	mu        sync.Mutex
	remaining int64
	hit       bool
	cancel    context.CancelFunc
}

func (l *sharedScanLimit) admit(p []byte) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hit {
		return nil, true
	}
	if int64(len(p)) <= l.remaining {
		l.remaining -= int64(len(p))
		return p, false
	}
	accepted := p
	if l.remaining < int64(len(p)) {
		accepted = p[:int(l.remaining)]
	}
	l.remaining = 0
	l.hit = true
	l.cancel()
	return accepted, true
}

func (l *sharedScanLimit) exceeded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hit
}

func copyPipe(group *tools.TextCollectorGroup, name string, source io.Reader, limit *sharedScanLimit) error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			accepted, exceeded := limit.admit(buffer[:n])
			if len(accepted) > 0 {
				if writeErr := group.WriteSource(name, accepted); writeErr != nil {
					return writeErr
				}
			}
			if exceeded {
				return errBashOutputLimit
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read bash %s: %w", name, err)
		}
	}
}

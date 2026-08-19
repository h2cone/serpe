//go:build linux

package workdir

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

func pickNative(ctx context.Context, start string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type candidate struct {
		name string
		args func(start string) []string
	}
	tools := []candidate{
		{name: "zenity", args: func(start string) []string {
			args := []string{"--file-selection", "--directory", "--title=Choose a working folder"}
			if start != "" {
				args = append(args, "--filename="+strings.TrimRight(start, "/")+"/")
			}
			return args
		}},
		{name: "qarma", args: func(start string) []string {
			args := []string{"--file-selection", "--directory", "--title=Choose a working folder"}
			if start != "" {
				args = append(args, "--filename="+strings.TrimRight(start, "/")+"/")
			}
			return args
		}},
		{name: "yad", args: func(start string) []string {
			args := []string{"--file-selection", "--directory", "--title=Choose a working folder"}
			if start != "" {
				args = append(args, "--filename="+strings.TrimRight(start, "/")+"/")
			}
			return args
		}},
		{name: "kdialog", args: func(start string) []string {
			args := []string{"--getexistingdirectory"}
			if start != "" {
				args = append(args, start)
			} else {
				args = append(args, ".")
			}
			return args
		}},
	}
	var sawTool bool
	for _, tool := range tools {
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		sawTool = true
		cmd := exec.CommandContext(ctx, tool.name, tool.args(start)...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		err := cmd.Run()
		path := strings.TrimSpace(stdout.String())
		if err == nil && path != "" {
			return path, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && path == "" {
			return "", ErrCanceled
		}
	}
	if !sawTool {
		return "", ErrUnavailable
	}
	return "", ErrCanceled
}

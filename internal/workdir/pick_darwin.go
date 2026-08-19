//go:build darwin

package workdir

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

func pickNative(ctx context.Context, start string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		return "", ErrUnavailable
	}
	script := `try
POSIX path of (choose folder with prompt "Choose a working folder"`
	if start != "" {
		script += ` default location POSIX file ` + appleScriptString(start)
	}
	script += `)
on error number -128
return ""
end try`
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrUnavailable
	}
	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", ErrCanceled
	}
	return path, nil
}

func appleScriptString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

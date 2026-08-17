package workdir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxBytes = 32 << 10

// Check validates a working directory: UTF-8, no controls, absolute, not a
// Windows device path, and an accessible directory.
func Check(ctx context.Context, cwd string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cwd == "" || !utf8.ValidString(cwd) {
		return fmt.Errorf("working directory is required")
	}
	if len(cwd) > maxBytes {
		return fmt.Errorf("working directory exceeds %d bytes", maxBytes)
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
	return ctx.Err()
}

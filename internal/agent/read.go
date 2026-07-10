package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/tw8ap/ouro/internal/canon"
)

const (
	maxImageBytes = 10 << 20
	maxReadLines  = 2000
	maxReadBytes  = 256 << 10
)

type readTool struct{}

func (readTool) Name() string { return "read" }

func (readTool) Definition() canon.Tool {
	return canon.Tool{
		Name:        "read",
		Description: "Read a text file with line numbers or view an image.",
		Parameters:  jsonRaw(`{"type":"object","properties":{"path":{"type":"string","description":"Relative or absolute path."},"offset":{"type":["integer","null"],"description":"1-based starting line; null starts at line 1."},"limit":{"type":["integer","null"],"description":"Maximum number of lines; null reads up to the safety cap."}},"required":["path","offset","limit"],"additionalProperties":false}`),
	}
}

func (readTool) Execute(ctx context.Context, env ToolContext, args map[string]any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	path, err := requiredString(args, "path")
	if err != nil {
		return Result{}, err
	}
	offset, err := nullableInt(args, "offset", 1)
	if err != nil {
		return Result{}, err
	}
	if offset < 1 {
		return Result{}, fmt.Errorf("offset must be at least 1")
	}
	limit, err := nullableInt(args, "limit", maxReadLines)
	if err != nil {
		return Result{}, err
	}
	if limit < 1 {
		return Result{}, fmt.Errorf("limit must be positive or null")
	}
	if limit > maxReadLines {
		limit = maxReadLines
	}

	resolved := resolvePath(env.WorkingDir, path)
	f, err := os.Open(resolved)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory", resolved)
	}

	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && err != io.EOF {
		return Result{}, err
	}
	head = head[:n]
	mime := http.DetectContentType(head)

	if strings.HasPrefix(mime, "image/") {
		if info.Size() > maxImageBytes {
			return Result{}, fmt.Errorf("image exceeds %d bytes", maxImageBytes)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Result{}, err
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return Result{}, err
		}
		return ImageResult(mime, data), nil
	}

	if !isTextLike(mime, filepath.Ext(resolved), head) {
		return Result{}, fmt.Errorf("file is not a supported text or image type")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	text, err := readTextWindow(f, offset, limit)
	if err != nil {
		return Result{}, err
	}
	return TextResult(text), nil
}

func isTextLike(mime, ext string, sample []byte) bool {
	if bytes.IndexByte(sample, 0) >= 0 {
		return false
	}
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "application/json", "application/xml", "application/javascript", "application/x-javascript":
		return true
	}
	switch strings.ToLower(ext) {
	case ".txt", ".md", ".go", ".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".css", ".js", ".ts", ".tsx", ".jsx", ".rs", ".py", ".sh", ".ps1":
		return true
	}
	return false
}

func readTextWindow(r io.Reader, offset, limit int) (string, error) {
	reader := bufio.NewReader(r)
	var out strings.Builder
	lineNo := 0
	captured := 0
	bytesWritten := 0
	truncated := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		if line == "" && err == io.EOF {
			break
		}
		lineNo++
		if lineNo >= offset {
			if captured >= limit || bytesWritten >= maxReadBytes {
				truncated = true
				break
			}
			line = strings.TrimRight(line, "\r\n")
			rendered := fmt.Sprintf("%6d\t%s\n", lineNo, line)
			if bytesWritten+len(rendered) > maxReadBytes {
				truncated = true
				break
			}
			out.WriteString(rendered)
			bytesWritten += len(rendered)
			captured++
		}
		if err == io.EOF {
			break
		}
	}

	text := out.String()
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("file is not valid UTF-8 text")
	}
	if truncated {
		text += "... (truncated)"
	}
	return strings.TrimRight(text, "\n"), nil
}

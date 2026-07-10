package agent

import (
	"context"
	"fmt"

	"github.com/tw8ap/ouro/internal/canon"
)

type writeTool struct{}

func (writeTool) Name() string { return "write" }

func (writeTool) Definition() canon.Tool {
	return canon.Tool{
		Name:        "write",
		Description: "Create or overwrite a text file atomically, creating parent directories as needed.",
		Parameters:  jsonRaw(`{"type":"object","properties":{"path":{"type":"string","description":"Relative or absolute path."},"content":{"type":"string","description":"Text content to write."}},"required":["path","content"],"additionalProperties":false}`),
	}
}

func (writeTool) Execute(ctx context.Context, env ToolContext, args map[string]any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	path, err := requiredString(args, "path")
	if err != nil {
		return Result{}, err
	}
	content, err := requiredString(args, "content")
	if err != nil {
		return Result{}, err
	}
	resolved := resolvePath(env.WorkingDir, path)
	if err := atomicWrite(resolved, []byte(content), defaultFileMode); err != nil {
		return Result{}, err
	}
	return TextResult(fmt.Sprintf("Wrote %d bytes to %s", len(content), resolved)), nil
}

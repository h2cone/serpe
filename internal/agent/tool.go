package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/tw8ap/ouro/internal/canon"
)

// Tool is one capability advertised to the model. Each tool describes its own
// protocol-agnostic schema and execution logic.
type Tool interface {
	Name() string
	Definition() canon.Tool
	Execute(ctx context.Context, env ToolContext, args map[string]any) (Result, error)
}

// ToolContext is the single-execution environment injected by ToolExecutor.
// Tool instances stay stateless; tools needing a path read WorkingDir.
type ToolContext struct {
	WorkingDir string
}

// Result is a tool's product. Content reuses canonical blocks so a tool may
// return text and image; IsError marks a recoverable failure fed back to the model.
type Result struct {
	Content []canon.ContentBlock
	IsError bool
}

// TextResult builds a single-text-block result.
func TextResult(text string) Result {
	return Result{Content: []canon.ContentBlock{&canon.TextBlock{Text: text}}}
}

// ImageResult builds a text summary plus a base64 image block.
func ImageResult(mime string, data []byte) Result {
	return Result{Content: []canon.ContentBlock{
		&canon.TextBlock{Text: fmt.Sprintf("image (%s, %d bytes)", mime, len(data))},
		&canon.ImageBlock{MediaType: mime, Data: base64.StdEncoding.EncodeToString(data)},
	}}
}

// ToolExecutor is a registry of tools. It dispatches by name and aggregates
// canonical tool definitions for the agent's requests.
type ToolExecutor struct {
	workingDir string
	tools      []Tool
	byName     map[string]Tool
}

// NewToolExecutor creates a tool executor rooted at workingDir. When no tools
// are passed, the default local toolset is registered.
func NewToolExecutor(workingDir string, tools ...Tool) ToolExecutor {
	reg := ToolExecutor{
		workingDir: workingDir,
		tools:      make([]Tool, 0),
		byName:     map[string]Tool{},
	}
	if len(tools) == 0 {
		tools = defaultTools()
	}
	for _, tool := range tools {
		name := tool.Name()
		if _, exists := reg.byName[name]; exists {
			panic(fmt.Sprintf("duplicate tool %q", name))
		}
		reg.tools = append(reg.tools, tool)
		reg.byName[name] = tool
	}
	return reg
}

// execute runs the named tool. Unknown tools and recoverable execution failures
// are reported via the returned error (the agent converts those to error Results).
func (t ToolExecutor) execute(ctx context.Context, name string, args map[string]any) (Result, error) {
	tool, ok := t.byName[name]
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	return tool.Execute(ctx, ToolContext{WorkingDir: t.workingDir}, args)
}

// Definitions aggregates every registered tool's canonical schema, in order.
func (t ToolExecutor) Definitions() []canon.Tool {
	defs := make([]canon.Tool, 0, len(t.tools))
	for _, tool := range t.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

// resolvePath cleans absolute paths and joins relative paths onto the working dir.
func resolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(workingDir, path)
}

// requiredString extracts a string argument, returning a recoverable error
// (wrapped so the agent can feed it back to the model) when missing.
func requiredString(args map[string]any, field string) (string, error) {
	value, ok := args[field].(string)
	if !ok {
		return "", fmt.Errorf("missing string field %q", field)
	}
	return value, nil
}

func nullableInt(args map[string]any, field string, fallback int) (int, error) {
	value, ok := args[field]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("field %q must be an integer", field)
		}
		return int(v), nil
	case json.Number:
		i, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, fmt.Errorf("field %q must be an integer", field)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("field %q must be an integer or null", field)
	}
}

func nullableBool(args map[string]any, field string, fallback bool) (bool, error) {
	value, ok := args[field]
	if !ok || value == nil {
		return fallback, nil
	}
	v, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("field %q must be a boolean or null", field)
	}
	return v, nil
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/h2cone/ouro/core/models"
)

// Tool binds one model-visible definition to its execution logic.
// Implementations must be safe for concurrent use.
type Tool interface {
	Definition() models.Tool
	Execute(ctx context.Context, arguments json.RawMessage) (ToolResult, error)
}

// ToolResult is safe content returned to the model.
type ToolResult struct {
	Content []models.Content
	IsError bool
}

// TextResult returns a successful text tool result.
func TextResult(text string) ToolResult {
	return ToolResult{Content: []models.Content{models.Text(text)}}
}

// ErrorResult returns a model-recoverable tool failure.
func ErrorResult(text string) ToolResult {
	return ToolResult{Content: []models.Content{models.Text(text)}, IsError: true}
}

func (r ToolResult) clone() ToolResult {
	return ToolResult{Content: cloneResultContent(r.Content), IsError: r.IsError}
}

func cloneResultContent(in []models.Content) []models.Content {
	if in == nil {
		return nil
	}
	out := make([]models.Content, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

type registeredTool struct {
	tool       Tool
	definition models.Tool
}

// toolSet owns the registered tool registry: construction-time validation and
// snapshots, name lookup, and defensive definition projection.
type toolSet struct {
	ordered []registeredTool
	byName  map[string]int
}

func registerTools(tools []Tool) (toolSet, error) {
	out := toolSet{
		ordered: make([]registeredTool, 0, len(tools)),
		byName:  make(map[string]int, len(tools)),
	}
	for i, tool := range tools {
		if tool == nil {
			return toolSet{}, fmt.Errorf("%w: tool %d is nil", ErrInvalidConfig, i)
		}
		def := tool.Definition()
		if err := def.Validate(); err != nil {
			return toolSet{}, fmt.Errorf("%w: tool %d: %v", ErrInvalidConfig, i, err)
		}
		if _, exists := out.byName[def.Name]; exists {
			return toolSet{}, fmt.Errorf("%w: duplicate tool name %q", ErrInvalidConfig, def.Name)
		}
		// Snapshot schema immediately; Runner never re-calls Definition().
		out.byName[def.Name] = len(out.ordered)
		out.ordered = append(out.ordered, registeredTool{tool: tool, definition: def.Clone()})
	}
	return out, nil
}

// definitions returns a defensive snapshot of all registered tool definitions.
func (ts toolSet) definitions() []models.Tool {
	if len(ts.ordered) == 0 {
		return nil
	}
	out := make([]models.Tool, len(ts.ordered))
	for i := range ts.ordered {
		out[i] = ts.ordered[i].definition.Clone()
	}
	return out
}

// lookup resolves a tool by name.
func (ts toolSet) lookup(name string) (registeredTool, bool) {
	index, ok := ts.byName[name]
	if !ok {
		return registeredTool{}, false
	}
	return ts.ordered[index], true
}

func normalizeToolOutput(call models.ToolCall, out ToolResult) (models.Content, error) {
	content := models.ToolResultContent(call.ID, call.Name, out.IsError, out.Content...)
	if err := content.Validate(); err != nil {
		return models.Content{}, err
	}
	return content, nil
}

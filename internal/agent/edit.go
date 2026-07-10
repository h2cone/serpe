package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tw8ap/ouro/internal/canon"
)

type editTool struct{}

func (editTool) Name() string { return "edit" }

func (editTool) Definition() canon.Tool {
	return canon.Tool{
		Name:        "edit",
		Description: "Replace an exact string in a file. By default the old string must appear exactly once.",
		Parameters:  jsonRaw(`{"type":"object","properties":{"path":{"type":"string","description":"Relative or absolute path."},"old_string":{"type":"string","description":"Exact text to replace, including indentation."},"new_string":{"type":"string","description":"Replacement text."},"replace_all":{"type":["boolean","null"],"description":"true replaces every match; null/false requires a unique match."}},"required":["path","old_string","new_string","replace_all"],"additionalProperties":false}`),
	}
}

func (editTool) Execute(ctx context.Context, env ToolContext, args map[string]any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	path, err := requiredString(args, "path")
	if err != nil {
		return Result{}, err
	}
	oldString, err := requiredString(args, "old_string")
	if err != nil {
		return Result{}, err
	}
	newString, err := requiredString(args, "new_string")
	if err != nil {
		return Result{}, err
	}
	replaceAll, err := nullableBool(args, "replace_all", false)
	if err != nil {
		return Result{}, err
	}
	if oldString == "" {
		return Result{}, fmt.Errorf("old_string must not be empty")
	}
	if oldString == newString {
		return Result{}, fmt.Errorf("new_string must differ from old_string")
	}

	resolved := resolvePath(env.WorkingDir, path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return Result{}, err
	}
	text := string(data)
	matches := strings.Count(text, oldString)
	if matches == 0 {
		return Result{}, fmt.Errorf("old_string not found")
	}
	if !replaceAll && matches > 1 {
		return Result{}, fmt.Errorf("old_string matches %d times; make it unique or set replace_all", matches)
	}

	replacements := 1
	if replaceAll {
		replacements = matches
		text = strings.ReplaceAll(text, oldString, newString)
	} else {
		text = strings.Replace(text, oldString, newString, 1)
	}

	if err := atomicWrite(resolved, []byte(text), defaultFileMode); err != nil {
		return Result{}, err
	}
	return TextResult(fmt.Sprintf("Edited %s: %d replacement(s)", resolved, replacements)), nil
}

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

var editSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Existing UTF-8 regular file to edit. A final symlink resolves to its target."},
    "edits": {
      "type": "array",
      "minItems": 1,
      "maxItems": 128,
      "items": {
        "type": "object",
        "properties": {
          "old_text": {"type": "string"},
          "new_text": {"type": "string"}
        },
        "required": ["old_text", "new_text"],
        "additionalProperties": false
      },
      "description": "Ordered byte-exact replacements. At each step old_text must occur exactly once."
    }
  },
  "required": ["path", "edits"],
  "additionalProperties": false
}`)

type editTool struct{ set *Set }

func (editTool) Definition() models.Tool {
	return models.NewTool("edit", "Atomically apply ordered, byte-exact unique replacements to an existing UTF-8 file. Every old_text must match exactly once; otherwise no edit is published.", editSchema)
}

type textEdit struct{ old, new string }

type editArguments struct {
	path  string
	edits []textEdit
}

func parseEditArguments(in tools.Invocation, inputLimit int64) (editArguments, error) {
	value, err := parseObject(in)
	if err != nil {
		return editArguments{}, err
	}
	path, ok := objectString(value, "path")
	if !ok {
		return editArguments{}, tools.Reject("path is required")
	}
	editsValue, ok := value.Lookup("edits")
	if !ok || editsValue.Kind != jsonvalue.KindArray || len(editsValue.Array) < 1 || len(editsValue.Array) > 128 {
		return editArguments{}, tools.Reject("edits must contain 1–128 items")
	}
	args := editArguments{path: path, edits: make([]textEdit, 0, len(editsValue.Array))}
	var total int64
	for i, item := range editsValue.Array {
		if item.Kind != jsonvalue.KindObject {
			return editArguments{}, tools.Reject(fmt.Sprintf("edits[%d] must be an object", i))
		}
		oldText, oldOK := objectString(item, "old_text")
		newText, newOK := objectString(item, "new_text")
		if !oldOK || !newOK || oldText == "" {
			return editArguments{}, tools.Reject(fmt.Sprintf("edits[%d] requires non-empty old_text and string new_text", i))
		}
		if int64(len(oldText)) > inputLimit-total {
			return editArguments{}, tools.Reject("edit texts exceed the input limit")
		}
		total += int64(len(oldText))
		if int64(len(newText)) > inputLimit-total {
			return editArguments{}, tools.Reject("edit texts exceed the input limit")
		}
		total += int64(len(newText))
		args.edits = append(args.edits, textEdit{old: oldText, new: newText})
	}
	return args, nil
}

func (t editTool) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	act, err := t.Activate(ctx, in)
	if err != nil {
		return tools.Output{}, err
	}
	return executeActivated(ctx, act)
}

func (t editTool) executeResolved(ctx context.Context, in tools.Invocation, target *resolvedTarget) (tools.Output, error) {
	args, err := parseEditArguments(in, t.set.lim.MaxWriteBytes)
	if err != nil {
		return tools.Output{}, err
	}
	if target == nil || target.file == nil {
		return tools.Output{}, errors.New("edit activation did not provide a file")
	}
	info, err := target.file.Stat()
	if err != nil {
		return tools.Error("file is not accessible"), nil
	}
	if info.Size() < 0 || info.Size() > t.set.lim.MaxEditBytes {
		return tools.Error("file exceeds the edit size limit"), nil
	}
	if _, err := target.file.Seek(0, io.SeekStart); err != nil {
		return tools.Error("file is not seekable"), nil
	}
	data, err := io.ReadAll(io.LimitReader(target.file, t.set.lim.MaxEditBytes+1))
	if err != nil {
		return tools.Error("file could not be read"), nil
	}
	if int64(len(data)) > t.set.lim.MaxEditBytes {
		return tools.Error("file exceeds the edit size limit"), nil
	}
	if !utf8.Valid(data) {
		return tools.Error("file is not valid UTF-8 text"), nil
	}
	body := append([]byte(nil), data...)
	var work int64
	for i, edit := range args.edits {
		if err := ctx.Err(); err != nil {
			return tools.Output{}, err
		}
		count := bytes.Count(body, []byte(edit.old))
		if count != 1 {
			return tools.Error(fmt.Sprintf("edits[%d] matched %d non-overlapping byte-exact occurrences; want 1", i, count)), nil
		}
		newLength := len(body) - len(edit.old) + len(edit.new)
		if newLength < 0 || int64(newLength) > t.set.lim.MaxEditBytes {
			return tools.Error("edit result exceeds the file size limit"), nil
		}
		step := 2*int64(len(body)) + int64(newLength) + int64(len(edit.old)) + int64(len(edit.new))
		if step < 0 || work > t.set.lim.MaxEditWorkBytes-step {
			return tools.Error("edit work budget exceeded"), nil
		}
		work += step
		body = bytes.Replace(body, []byte(edit.old), []byte(edit.new), 1)
	}
	// Re-read the same opened object immediately before publish. This detects
	// in-place changes that preserve the directory entry identity.
	if _, err := target.file.Seek(0, io.SeekStart); err != nil {
		return tools.Error("file changed during edit"), nil
	}
	current, err := io.ReadAll(io.LimitReader(target.file, t.set.lim.MaxEditBytes+1))
	if err != nil || !bytes.Equal(current, data) {
		return tools.Error("file changed during edit"), nil
	}
	if err := target.close(); err != nil {
		return tools.Output{}, err
	}
	target.expected = append([]byte(nil), data...)
	published, err := atomicPublish(ctx, target, body, info.Mode().Perm())
	if err != nil {
		if errors.Is(err, errPublishConflict) {
			return tools.Error("file changed before the atomic edit could be published"), nil
		}
		return tools.Output{}, err
	}
	if !published {
		return tools.Output{}, errors.New("edit publish invariant failed")
	}
	return tools.Text(fmt.Sprintf("applied %d edits to %s", len(args.edits), args.path)), nil
}

package builtin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

var writeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File to create or atomically overwrite. A final symlink is resolved and its regular-file target is replaced; the link itself is preserved. Parent directory must exist."},
    "content": {"type": "string", "description": "UTF-8 file contents."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`)

type writeTool struct{ set *Set }

func (writeTool) Definition() models.Tool {
	return models.NewTool("write", "Create or atomically overwrite a UTF-8 file in the working directory. A final symlink resolves to its regular-file target. Atomic replacement intentionally breaks hard-link sharing.", writeSchema)
}

type writeArguments struct {
	path    string
	content string
}

func parseWriteArguments(in tools.Invocation, limit int64) (writeArguments, error) {
	value, err := parseObject(in)
	if err != nil {
		return writeArguments{}, err
	}
	path, pathOK := objectString(value, "path")
	content, contentOK := objectString(value, "content")
	if !pathOK {
		return writeArguments{}, tools.Reject("path is required")
	}
	if !contentOK {
		return writeArguments{}, tools.Reject("content is required")
	}
	if !utf8.ValidString(content) {
		return writeArguments{}, tools.Reject("content is not valid UTF-8")
	}
	if int64(len(content)) > limit {
		return writeArguments{}, tools.Reject("content exceeds the write limit")
	}
	return writeArguments{path: path, content: content}, nil
}

func (t writeTool) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	args, err := parseWriteArguments(in, t.set.lim.MaxWriteBytes)
	if err != nil {
		return tools.Output{}, err
	}
	target, err := t.set.resolveTarget(ctx, in, args.path, targetWrite)
	if err != nil {
		return pathFail(err)
	}
	defer target.close()
	return t.executeResolved(ctx, in, target)
}

func (t writeTool) executeResolved(ctx context.Context, in tools.Invocation, target *resolvedTarget) (tools.Output, error) {
	args, err := parseWriteArguments(in, t.set.lim.MaxWriteBytes)
	if err != nil {
		return tools.Output{}, err
	}
	if target == nil {
		return tools.Output{}, errors.New("write activation did not provide a target")
	}
	if err := ctx.Err(); err != nil {
		return tools.Output{}, err
	}
	mode := os.FileMode(0o600)
	if target.exists {
		info, err := target.file.Stat()
		if err != nil {
			return tools.Error("target changed before write"), nil
		}
		mode = info.Mode().Perm()
		if err := target.close(); err != nil {
			return tools.Output{}, err
		}
	}
	published, err := atomicPublish(ctx, target, []byte(args.content), mode)
	if err != nil {
		if errors.Is(err, errPublishConflict) {
			return tools.Error("target changed before the atomic write could be published"), nil
		}
		return tools.Output{}, err
	}
	if !published {
		return tools.Output{}, errors.New("write publish invariant failed")
	}
	return tools.Text("wrote " + args.path), nil
}

var errPublishConflict = errors.New("publish target changed")

func atomicPublish(ctx context.Context, target *resolvedTarget, data []byte, mode os.FileMode) (bool, error) {
	if target.path == "" || target.parent == "" {
		return false, fmt.Errorf("invalid resolved publish target")
	}
	var (
		tempPath string
		file     *os.File
		err      error
	)
	for attempt := 0; attempt < 4; attempt++ {
		var random [16]byte
		if _, err = io.ReadFull(rand.Reader, random[:]); err != nil {
			return false, fmt.Errorf("generate write temporary name: %w", err)
		}
		tempPath = filepath.Join(target.parent, ".serpe-write-"+hex.EncodeToString(random[:])+".tmp")
		file, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if err == nil {
			break
		}
		if !os.IsExist(err) || attempt == 3 {
			return false, fmt.Errorf("create write temporary file: %w", err)
		}
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return false, err
	}
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return false, err
		}
		chunk := data
		if len(chunk) > 64<<10 {
			chunk = chunk[:64<<10]
		}
		n, err := file.Write(chunk)
		if err != nil {
			_ = file.Close()
			return false, err
		}
		if n == 0 {
			_ = file.Close()
			return false, io.ErrShortWrite
		}
		data = data[n:]
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}

	if target.exists {
		current, err := os.Open(target.path)
		if err != nil {
			return false, errPublishConflict
		}
		identity, idErr := platformFileIdentity(current)
		contentMatches := true
		if target.expected != nil {
			observed, readErr := io.ReadAll(io.LimitReader(current, int64(len(target.expected))+1))
			contentMatches = readErr == nil && bytes.Equal(observed, target.expected)
		}
		closeErr := current.Close()
		if idErr != nil || closeErr != nil || identity != target.identity || !contentMatches {
			return false, errPublishConflict
		}
		if err := replaceFile(tempPath, target.path); err != nil {
			return false, err
		}
	} else {
		if err := publishNewFile(tempPath, target.path); err != nil {
			if os.IsExist(err) || errors.Is(err, os.ErrExist) || errors.Is(err, errPublishConflict) {
				return false, errPublishConflict
			}
			return false, err
		}
	}
	committed = true
	if err := syncDirectory(target.parent); err != nil {
		return true, fmt.Errorf("file was published but directory durability is unconfirmed: %w", err)
	}
	return true, nil
}

func publishNewFile(tempPath, targetPath string) error {
	if err := os.Link(tempPath, targetPath); err != nil {
		if os.IsExist(err) || errors.Is(err, os.ErrExist) {
			return errPublishConflict
		}
		return fmt.Errorf("atomic no-replace publish is unavailable: %w", err)
	}
	return os.Remove(tempPath)
}

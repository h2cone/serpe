package builtin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
	"github.com/h2cone/serpe/internal/imagecheck"
)

var readSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Path relative to the working directory, or an absolute path inside it."},
    "line": {"type": "integer", "minimum": 1, "description": "1-based start line for text. Default 1."},
    "lines": {"type": "integer", "minimum": 1, "maximum": 10000, "description": "Maximum text lines to return. Default 2000."},
    "cursor": {"type": "string", "maxLength": 1024, "description": "Opaque continuation from a previous read. Mutually exclusive with line/lines."}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

type readTool struct{ set *Set }

func (readTool) Definition() models.Tool {
	return models.NewTool("read", "Read a UTF-8 text file or a structurally valid static PNG/JPEG/GIF/WebP image from the working directory. Large text is continued with an opaque cursor.", readSchema)
}

type readArguments struct {
	path      string
	line      int64
	lines     int64
	cursor    string
	hasLine   bool
	hasLines  bool
	hasCursor bool
}

func parseReadArguments(in tools.Invocation) (readArguments, error) {
	value, err := parseObject(in)
	if err != nil {
		return readArguments{}, err
	}
	path, ok := objectString(value, "path")
	if !ok {
		return readArguments{}, tools.Reject("path is required")
	}
	args := readArguments{path: path, line: 1, lines: 2000}
	_, args.hasLine = value.Lookup("line")
	_, args.hasLines = value.Lookup("lines")
	_, args.hasCursor = value.Lookup("cursor")
	if args.hasCursor && (args.hasLine || args.hasLines) {
		return readArguments{}, tools.Reject("cursor cannot be combined with line or lines")
	}
	if args.hasLine {
		line, _, err := objectInt(value, "line")
		if err != nil || line < 1 || line > math.MaxInt32 {
			return readArguments{}, tools.Reject("line is out of range")
		}
		args.line = line
	}
	if args.hasLines {
		lines, _, err := objectInt(value, "lines")
		if err != nil || lines < 1 || lines > 10_000 {
			return readArguments{}, tools.Reject("lines is out of range")
		}
		args.lines = lines
	}
	if args.hasCursor {
		cursor, ok := objectString(value, "cursor")
		if !ok || cursor == "" || len(cursor) > maxCursorBytes {
			return readArguments{}, tools.Reject("cursor is invalid")
		}
		args.cursor = cursor
	}
	return args, nil
}

func (t readTool) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	args, err := parseReadArguments(in)
	if err != nil {
		return tools.Output{}, err
	}
	target, err := t.set.resolveTarget(ctx, in, args.path, targetRead)
	if err != nil {
		return pathFail(err)
	}
	defer target.close()
	return t.executeResolved(ctx, in, target)
}

func (t readTool) executeResolved(ctx context.Context, in tools.Invocation, target *resolvedTarget) (tools.Output, error) {
	args, err := parseReadArguments(in)
	if err != nil {
		return tools.Output{}, err
	}
	if target == nil || target.file == nil {
		return tools.Output{}, errors.New("read activation did not provide a file")
	}
	before, err := target.file.Stat()
	if err != nil {
		return tools.Error("file is not accessible"), nil
	}
	if before.Size() < 0 || before.Size() > t.set.lim.MaxReadScanBytes {
		return tools.Error("file exceeds the read scan limit"), nil
	}
	if _, err := target.file.Seek(0, io.SeekStart); err != nil {
		return tools.Error("file is not seekable"), nil
	}
	prefix := make([]byte, 12)
	n, prefixErr := io.ReadFull(target.file, prefix)
	if prefixErr != nil && !errors.Is(prefixErr, io.EOF) && !errors.Is(prefixErr, io.ErrUnexpectedEOF) {
		return tools.Error("file could not be read"), nil
	}
	prefix = prefix[:n]
	if _, err := target.file.Seek(0, io.SeekStart); err != nil {
		return tools.Error("file is not seekable"), nil
	}
	if mime, image := imagecheck.Detect(prefix); image {
		if args.hasLine || args.hasLines || args.hasCursor {
			return tools.Error("line, lines, and cursor apply only to text files"), nil
		}
		return t.readImage(ctx, in, target, before, mime)
	}
	return t.readText(ctx, in, target, before, args)
}

func (t readTool) readImage(ctx context.Context, in tools.Invocation, target *resolvedTarget, before os.FileInfo, mime string) (tools.Output, error) {
	ceiling := in.OutputLimits.MaxImageBytes
	if in.OutputLimits.MaxFramedBytes < ceiling {
		ceiling = in.OutputLimits.MaxFramedBytes
	}
	if before.Size() > ceiling {
		return tools.Error("image exceeds the output byte limit"), nil
	}
	reader := io.LimitReader(target.file, ceiling+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return tools.Error("image could not be read"), nil
	}
	if int64(len(data)) > ceiling {
		return tools.Error("image exceeds the output byte limit"), nil
	}
	if err := ctx.Err(); err != nil {
		return tools.Output{}, err
	}
	_, err = imagecheck.Inspect(mime, data, imagecheck.Limits{
		MaxBytes:   in.OutputLimits.MaxImageBytes,
		MaxWidth:   in.OutputLimits.MaxImageWidth,
		MaxHeight:  in.OutputLimits.MaxImageHeight,
		MaxPixels:  in.OutputLimits.MaxImagePixels,
		MaxRecords: 65_536,
	})
	if err != nil {
		return tools.Error("image container is invalid, animated, or exceeds configured limits"), nil
	}
	if changed, err := targetChanged(target, before); err != nil || changed {
		return tools.Error("file changed while it was being read"), nil
	}
	return tools.Output{Content: []models.Content{models.ImageBytes(mime, data)}}, nil
}

func (t readTool) readText(ctx context.Context, in tools.Invocation, target *resolvedTarget, before os.FileInfo, args readArguments) (tools.Output, error) {
	startOffset := int64(-1)
	startLine := args.line
	lineLimit := args.lines
	var previous readCursorPayload
	if args.hasCursor {
		decoded, err := t.set.cursor.open(args.cursor)
		if err != nil {
			if errors.Is(err, errCursorStale) {
				return tools.Error("read cursor is stale after a restart or on this instance"), nil
			}
			return tools.Error("read cursor is invalid"), nil
		}
		if decoded.path != cursorDigest(target.path) ||
			decoded.identity != cursorDigest(target.identity) {
			return tools.Error("read cursor is stale because the path identity changed"), nil
		}
		previous = decoded
		startOffset = int64(decoded.offset)
		startLine = int64(decoded.line)
		lineLimit = int64(decoded.lines)
	}
	collector, err := tools.NewTextCollector(in.OutputLimits, tools.Prefix)
	if err != nil {
		return tools.Output{}, err
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: target.file, N: t.set.lim.MaxReadScanBytes + 1}
	reader := bufio.NewReaderSize(io.TeeReader(limited, hasher), 64<<10)
	currentLine := int64(1)
	offset := int64(0)
	foundStart := false
	stoppedAtLineLimit := false
	writtenEnd := int64(0)
	var newlineEnds []int64

	for {
		if err := ctx.Err(); err != nil {
			return tools.Output{}, err
		}
		r, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return tools.Error("file could not be read"), nil
		}
		if offset+int64(size) > t.set.lim.MaxReadScanBytes {
			return tools.Error("file exceeds the read scan limit"), nil
		}
		if r == utf8.RuneError && size == 1 {
			return tools.Error("file is not valid UTF-8 text"), nil
		}
		beforeRune := offset
		offset += int64(size)
		if !foundStart {
			switch {
			case startOffset >= 0 && beforeRune == startOffset:
				foundStart = true
			case startOffset >= 0 && beforeRune > startOffset:
				return tools.Error("read cursor offset is not a UTF-8 boundary"), nil
			case startOffset < 0 && currentLine == startLine:
				startOffset = beforeRune
				foundStart = true
			}
		}
		if foundStart && !stoppedAtLineLimit {
			var encoded [utf8.UTFMax]byte
			n := utf8.EncodeRune(encoded[:], r)
			if _, err := collector.Write(encoded[:n]); err != nil {
				return tools.Output{}, err
			}
			writtenEnd = offset
			if r == '\n' {
				newlineEnds = append(newlineEnds, writtenEnd-startOffset)
				if int64(len(newlineEnds)) >= lineLimit {
					stoppedAtLineLimit = true
				}
			}
		}
		if r == '\n' {
			currentLine++
		}
	}
	if !foundStart {
		if startOffset == offset { // cursor exactly at EOF
			foundStart = true
			writtenEnd = offset
		} else {
			return tools.Error("requested line is beyond end of file"), nil
		}
	}
	if limited.N <= 0 {
		return tools.Error("file exceeds the read scan limit"), nil
	}
	digest := hasher.Sum(nil)
	if args.hasCursor && previous.content != *(*[32]byte)(digest) {
		return tools.Error("read cursor is stale because the file content changed"), nil
	}
	if changed, err := targetChanged(target, before); err != nil || changed {
		return tools.Error("file changed while it was being read"), nil
	}
	state, err := collector.PreparePrefix()
	if err != nil {
		return tools.Output{}, err
	}
	nextOffset := writtenEnd
	if state.Truncated {
		nextOffset = startOffset + state.KeptLogicalBytes
	}
	hasMore := nextOffset < offset
	if hasMore && nextOffset <= startOffset {
		return tools.Error("output budget cannot make read progress"), nil
	}
	nextLine := startLine
	for _, newlineEnd := range newlineEnds {
		if newlineEnd > nextOffset-startOffset {
			break
		}
		nextLine++
	}
	metadata := map[string]any{
		"path":         args.path,
		"start_offset": startOffset,
		"end_offset":   nextOffset,
		"start_line":   startLine,
		"sha256":       hex.EncodeToString(digest),
	}
	if hasMore {
		var contentDigest [32]byte
		copy(contentDigest[:], digest)
		token, err := t.set.cursor.seal(readCursorPayload{
			path:     cursorDigest(target.path),
			identity: cursorDigest(target.identity),
			content:  contentDigest,
			offset:   uint64(nextOffset),
			line:     uint32(nextLine),
			lines:    uint32(lineLimit),
		})
		if err != nil {
			return tools.Output{}, err
		}
		metadata["next_cursor"] = token
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		return tools.Output{}, err
	}
	return collector.Output(rawMetadata, false)
}

func targetChanged(target *resolvedTarget, before os.FileInfo) (bool, error) {
	after, err := target.file.Stat()
	if err != nil {
		return true, err
	}
	identity, err := platformFileIdentity(target.file)
	if err != nil {
		return true, err
	}
	return identity != target.identity || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()), nil
}

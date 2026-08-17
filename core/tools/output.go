package tools

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
)

const (
	outputDomain      = "serpe.tools.output.v1"
	outputVersion     = uint64(1)
	frameChunk        = 4096
	blockBoundary     = "block"
	truncationPrefix  = "\n[serpe-tool-truncated:v1 kept_framed_bytes="
	truncationMiddle  = " original_framed_bytes="
	truncationSHA     = " sha256="
	truncationSuffix  = " continue=use a continuation cursor]\n"
	budgetErrorText   = "tool output exceeded configured limits"
	inspectionCeiling = int64(256 << 20)
)

type outputSeal struct {
	executor uint64
	limits   OutputLimits
	isError  bool
	stats    OutputStats
	retained string
}

type collectorReceipt struct {
	limits        OutputLimits
	originalBytes int64
	originalSHA   string
	keptBytes     int64
	truncated     bool
	isError       bool
	retained      string
	sources       []string
	metadata      []byte
}

func (e *Executor) Finalize(out Output) (Output, error) {
	return e.finalize(out, e.output, false)
}

func (e *Executor) ReFinalize(previous, replacement Output) (Output, error) {
	if previous.seal == nil || previous.seal.executor != e.id {
		return Output{}, wrapExecution("ReFinalize previous is not sealed by this executor")
	}
	if previous.receipt != nil {
		return Output{}, wrapExecution("ReFinalize previous must not carry a collector receipt")
	}
	if err := e.verifySeal(previous); err != nil {
		return Output{}, err
	}
	if replacement.seal != nil || replacement.receipt != nil {
		return Output{}, wrapExecution("ReFinalize replacement must be unsealed")
	}
	return e.finalize(replacement, previous.seal.limits, true)
}

func (e *Executor) finalize(out Output, limits OutputLimits, allowRewrite bool) (Output, error) {
	if out.seal != nil && !allowRewrite {
		return Output{}, wrapExecution("tool reused a sealed output")
	}
	blocks, err := inspectContent(out.Content, limits)
	if err != nil {
		return Output{}, err
	}
	if out.receipt != nil {
		if err := e.verifyReceipt(out, blocks, limits); err != nil {
			return Output{}, err
		}
	}
	originalBytes, originalSHA, err := digestBlocks(blocks)
	if err != nil {
		return Output{}, err
	}

	kept := blocks
	truncated := false
	if out.receipt != nil {
		originalBytes = out.receipt.originalBytes
		originalSHA = out.receipt.originalSHA
		truncated = out.receipt.truncated
	} else if overText(blocks, limits) || originalBytes > limits.MaxFramedBytes {
		next, ok := truncateBlocks(blocks, limits, originalBytes, originalSHA)
		if !ok {
			return Output{}, errBudget
		}
		kept = next
		truncated = true
	}
	keptBytes, retainedSHA, err := digestBlocks(kept)
	if err != nil {
		return Output{}, err
	}
	if keptBytes > limits.MaxFramedBytes {
		return Output{}, errBudget
	}
	content, err := materialize(kept)
	if err != nil {
		return Output{}, err
	}
	for i := range content {
		if err := content[i].Validate(); err != nil {
			return Output{}, wrapExecution("final content: %v", err)
		}
	}
	stats := OutputStats{
		OriginalBytes: originalBytes,
		KeptBytes:     keptBytes,
		SHA256:        originalSHA,
		Truncated:     truncated || keptBytes != originalBytes,
	}
	if !stats.Truncated {
		stats.SHA256 = retainedSHA
		stats.OriginalBytes = stats.KeptBytes
	}
	final := Output{Content: content, IsError: out.IsError, Stats: stats}
	final.seal = &outputSeal{
		executor: e.id,
		limits:   limits,
		isError:  final.IsError,
		stats:    stats,
		retained: retainedSHA,
	}
	return final, nil
}

func (e *Executor) verifyReceipt(out Output, blocks []contentBlock, limits OutputLimits) error {
	r := out.receipt
	if r.limits != limits {
		return wrapExecution("collector receipt limits do not match this call")
	}
	if r.isError != out.IsError {
		return wrapExecution("collector receipt IsError mismatch")
	}
	kept, retained, err := digestBlocks(blocks)
	if err != nil {
		return err
	}
	if kept != r.keptBytes || retained != r.retained {
		return wrapExecution("collector receipt content mismatch")
	}
	if r.originalBytes < 0 || r.keptBytes < 0 || len(r.originalSHA) != sha256.Size*2 {
		return wrapExecution("collector receipt statistics are invalid")
	}
	if !r.truncated && (r.originalBytes != r.keptBytes || r.originalSHA != r.retained) {
		return wrapExecution("collector receipt domains disagree")
	}
	return nil
}

func (e *Executor) verifySeal(out Output) error {
	s := out.seal
	if s.isError != out.IsError || s.stats != out.Stats {
		return wrapExecution("sealed output was mutated")
	}
	blocks, err := inspectContent(out.Content, s.limits)
	if err != nil {
		return wrapExecution("sealed output content is invalid")
	}
	_, digest, err := digestBlocks(blocks)
	if err != nil {
		return err
	}
	if digest != s.retained {
		return wrapExecution("sealed output content was mutated")
	}
	return nil
}

func (e *Executor) budgetOutput(limits OutputLimits, _ bool) (Output, error) {
	out := Error(budgetErrorText)
	final, err := e.finalize(out, limits, true)
	if err != nil {
		return Output{}, err
	}
	return final, nil
}

var errBudget = fmt.Errorf("%w", ErrOutputLimit)

func isBudget(err error) bool {
	return errors.Is(err, ErrOutputLimit)
}

type contentBlock struct {
	kind    models.ContentKind
	text    string
	mime    string
	detail  models.ImageDetail
	data    []byte
	imageOK bool
}

func inspectContent(in []models.Content, limits OutputLimits) ([]contentBlock, error) {
	if len(in) == 0 {
		return nil, wrapExecution("output content is empty")
	}
	if len(in) > limits.MaxBlocks {
		return nil, errBudget
	}
	if err := preflightContent(in, limits); err != nil {
		return nil, err
	}
	out := make([]contentBlock, len(in))
	var imageBytes int64
	for i, c := range in {
		if err := inspectUnion(c); err != nil {
			return nil, err
		}
		switch c.Kind {
		case models.ContentText:
			if !utf8.ValidString(c.Text.Text) {
				return nil, wrapExecution("text is not valid UTF-8")
			}
			out[i] = contentBlock{kind: models.ContentText, text: c.Text.Text}
		case models.ContentImage:
			info, err := inspectImage(c.Image.MIMEType, c.Image.Data)
			if err != nil {
				return nil, err
			}
			if c.Image.Detail != "" && c.Image.Detail != models.ImageDetailAuto &&
				c.Image.Detail != models.ImageDetailLow && c.Image.Detail != models.ImageDetailHigh {
				return nil, wrapExecution("invalid image detail")
			}
			if info.width > limits.MaxImageWidth || info.height > limits.MaxImageHeight {
				return nil, errBudget
			}
			pixels, ok := mul64(int64(info.width), int64(info.height))
			if !ok || pixels > limits.MaxImagePixels {
				return nil, errBudget
			}
			next, ok := add64(imageBytes, int64(len(c.Image.Data)))
			if !ok || next > limits.MaxImageBytes {
				return nil, errBudget
			}
			imageBytes = next
			out[i] = contentBlock{
				kind:    models.ContentImage,
				mime:    c.Image.MIMEType,
				detail:  c.Image.Detail,
				data:    c.Image.Data,
				imageOK: true,
			}
		default:
			return nil, wrapExecution("output block %d has unsupported kind %q", i, c.Kind)
		}
	}
	return out, nil
}

func preflightContent(in []models.Content, limits OutputLimits) error {
	framed := int64(8 + len(outputDomain) + 8 + 8)
	var imageBytes int64
	for i, content := range in {
		if err := inspectUnion(content); err != nil {
			return err
		}
		var payload int64
		var mime, detail string
		switch content.Kind {
		case models.ContentText:
			payload = int64(len(content.Text.Text))
		case models.ContentImage:
			if content.Image.URI != "" {
				return wrapExecution("image URI is not allowed")
			}
			mime = content.Image.MIMEType
			detail = string(content.Image.Detail)
			if len(mime) > maxCollectorMetaBytes || len(detail) > maxCollectorMetaBytes {
				return wrapExecution("image metadata exceeds structural ceiling")
			}
			if int64(len(mime)) > limits.MaxMetadataBytes || int64(len(detail)) > limits.MaxMetadataBytes {
				return errBudget
			}
			payload = int64(len(content.Image.Data))
			next, ok := add64(imageBytes, payload)
			if !ok || next > limits.MaxImageBytes {
				return errBudget
			}
			imageBytes = next
		default:
			return wrapExecution("output block %d has unsupported kind %q", i, content.Kind)
		}
		size, ok := framedBlockSize(string(content.Kind), mime, detail, payload)
		if !ok {
			return errBudget
		}
		framed, ok = add64(framed, size)
		if !ok || framed > inspectionCeiling {
			return errBudget
		}
	}
	return nil
}

func framedBlockSize(kind, mime, detail string, payload int64) (int64, bool) {
	if payload < 0 {
		return 0, false
	}
	// index, three framed tags, payload chunks, zero chunk, payload trailer,
	// and the framed block-boundary tag.
	size := int64(8)
	for _, n := range []int64{int64(len(kind)), int64(len(mime)), int64(len(detail))} {
		var ok bool
		size, ok = add64(size, 8+n)
		if !ok {
			return 0, false
		}
	}
	chunks := payload / frameChunk
	if payload%frameChunk != 0 {
		chunks++
	}
	chunkOverhead, ok := mul64(chunks, 8)
	if !ok {
		return 0, false
	}
	for _, n := range []int64{chunkOverhead, payload, 8, 8, 8 + int64(len(blockBoundary))} {
		size, ok = add64(size, n)
		if !ok {
			return 0, false
		}
	}
	return size, true
}

func inspectUnion(c models.Content) error {
	n := 0
	if c.Text != nil {
		n++
	}
	if c.Image != nil {
		n++
	}
	if c.ToolCall != nil {
		n++
	}
	if c.ToolResult != nil {
		n++
	}
	if c.ReasoningSummary != nil {
		n++
	}
	if c.Refusal != nil {
		n++
	}
	if n != 1 {
		return wrapExecution("content is not a closed union")
	}
	switch c.Kind {
	case models.ContentText:
		if c.Text == nil {
			return wrapExecution("text kind does not match variant")
		}
	case models.ContentImage:
		if c.Image == nil {
			return wrapExecution("image kind does not match variant")
		}
	default:
		return wrapExecution("unsupported content kind %q", c.Kind)
	}
	return nil
}

func overText(blocks []contentBlock, limits OutputLimits) bool {
	var n int64
	for _, b := range blocks {
		if b.kind == models.ContentText {
			n += int64(len(b.text))
		}
	}
	return n > limits.MaxTextBytes
}

func textTotal(blocks []contentBlock) int64 {
	var n int64
	for _, b := range blocks {
		if b.kind == models.ContentText {
			n += int64(len(b.text))
		}
	}
	return n
}

func hasText(blocks []contentBlock) bool {
	for _, b := range blocks {
		if b.kind == models.ContentText {
			return true
		}
	}
	return false
}

func truncateBlocks(blocks []contentBlock, limits OutputLimits, originalBytes int64, originalSHA string) ([]contentBlock, bool) {
	if !hasText(blocks) {
		return nil, false
	}
	lo, hi := int64(0), textTotal(blocks)
	bestKeep := int64(-1)
	markerBytes := int64(len(formatMarker(0, uint64(originalBytes), originalSHA)))
	for lo <= hi {
		mid := (lo + hi) / 2
		framed, text, ok := truncatedShapeSize(blocks, mid, markerBytes)
		if ok && framed <= limits.MaxFramedBytes && text <= limits.MaxTextBytes {
			bestKeep = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if bestKeep < 0 {
		return nil, false
	}
	framed, _, ok := truncatedShapeSize(blocks, bestKeep, markerBytes)
	if !ok {
		return nil, false
	}
	marked := applyKeep(blocks, bestKeep, formatMarker(uint64(framed), uint64(originalBytes), originalSHA))
	actual, ok := blocksFrameSize(marked)
	if !ok || actual != framed || textTotal(marked) > limits.MaxTextBytes {
		return nil, false
	}
	return marked, true
}

func truncatedShapeSize(blocks []contentBlock, keep, markerBytes int64) (int64, int64, bool) {
	total := textTotal(blocks)
	if keep > total {
		keep = total
	}
	headCut := textBoundaryBefore(blocks, keep/2)
	tailStart := textBoundaryAfter(blocks, total-(keep-keep/2))
	if tailStart < headCut {
		tailStart = headCut
	}
	markerBlock := firstTextAtOrAfter(blocks, headCut)
	framed := int64(8 + len(outputDomain) + 8 + 8)
	var textBytes, offset int64
	for i, block := range blocks {
		payload := int64(len(block.data))
		if block.kind == models.ContentText {
			start, end := offset, offset+int64(len(block.text))
			payload = 0
			if start < headCut {
				payload += max64(0, min64(end, headCut)-start)
			}
			if end > tailStart {
				payload += max64(0, end-max64(start, tailStart))
			}
			if i == markerBlock {
				payload += markerBytes
			}
			offset = end
			var ok bool
			textBytes, ok = add64(textBytes, payload)
			if !ok {
				return 0, 0, false
			}
		}
		blockSize, ok := framedBlockSize(string(block.kind), block.mime, string(block.detail), payload)
		if !ok {
			return 0, 0, false
		}
		framed, ok = add64(framed, blockSize)
		if !ok {
			return 0, 0, false
		}
	}
	return framed, textBytes, true
}

func blocksFrameSize(blocks []contentBlock) (int64, bool) {
	framed := int64(8 + len(outputDomain) + 8 + 8)
	for _, block := range blocks {
		payload := int64(len(block.text))
		if block.kind == models.ContentImage {
			payload = int64(len(block.data))
		}
		blockSize, ok := framedBlockSize(string(block.kind), block.mime, string(block.detail), payload)
		if !ok {
			return 0, false
		}
		framed, ok = add64(framed, blockSize)
		if !ok {
			return 0, false
		}
	}
	return framed, true
}

func applyKeep(blocks []contentBlock, keep int64, marker string) []contentBlock {
	total := textTotal(blocks)
	if keep > total {
		keep = total
	}
	headCut := textBoundaryBefore(blocks, keep/2)
	tailStart := textBoundaryAfter(blocks, total-(keep-keep/2))
	if tailStart < headCut {
		tailStart = headCut
	}
	markerBlock := firstTextAtOrAfter(blocks, headCut)
	out := make([]contentBlock, len(blocks))
	copy(out, blocks)
	var offset int64
	for i := range out {
		if out[i].kind != models.ContentText {
			continue
		}
		text := out[i].text
		start, end := offset, offset+int64(len(text))
		buf := make([]byte, 0, len(text)+len(marker))
		if start < headCut {
			to := min64(end, headCut)
			buf = append(buf, text[:int(to-start)]...)
		}
		if i == markerBlock {
			buf = append(buf, marker...)
		}
		if end > tailStart {
			from := max64(start, tailStart)
			if from < end {
				buf = append(buf, text[int(from-start):]...)
			}
		}
		out[i].text = string(buf)
		offset = end
	}
	return out
}

func textBoundaryBefore(blocks []contentBlock, target int64) int64 {
	var offset int64
	for _, block := range blocks {
		if block.kind != models.ContentText {
			continue
		}
		end := offset + int64(len(block.text))
		if target <= end {
			local := target - offset
			for local > 0 && !utf8.ValidString(block.text[:int(local)]) {
				local--
			}
			return offset + local
		}
		offset = end
	}
	return offset
}

func textBoundaryAfter(blocks []contentBlock, target int64) int64 {
	var offset int64
	for _, block := range blocks {
		if block.kind != models.ContentText {
			continue
		}
		end := offset + int64(len(block.text))
		if target <= end {
			local := target - offset
			if local < 0 {
				local = 0
			}
			for local < int64(len(block.text)) && !utf8.ValidString(block.text[int(local):]) {
				local++
			}
			return offset + local
		}
		offset = end
	}
	return offset
}

func firstTextAtOrAfter(blocks []contentBlock, target int64) int {
	var offset int64
	last := -1
	for i, block := range blocks {
		if block.kind != models.ContentText {
			continue
		}
		last = i
		end := offset + int64(len(block.text))
		if target <= end {
			return i
		}
		offset = end
	}
	return last
}

func formatMarker(kept, original uint64, sha string) string {
	return fmt.Sprintf("%s%016x%s%016x%s%s%s",
		truncationPrefix, kept, truncationMiddle, original, truncationSHA, sha, truncationSuffix)
}

func materialize(blocks []contentBlock) ([]models.Content, error) {
	out := make([]models.Content, len(blocks))
	for i, b := range blocks {
		switch b.kind {
		case models.ContentText:
			out[i] = models.Text(b.text)
		case models.ContentImage:
			out[i] = models.ImageBytes(b.mime, b.data)
			if b.detail != "" {
				out[i].Image.Detail = b.detail
			}
		default:
			return nil, wrapExecution("internal: unknown block kind")
		}
	}
	return out, nil
}

func digestBlocks(blocks []contentBlock) (int64, string, error) {
	enc := frameHashEnc{h: sha256.New()}
	if !enc.str(outputDomain) || !enc.u64(outputVersion) || !enc.u64(uint64(len(blocks))) {
		return 0, "", errBudget
	}
	for i, block := range blocks {
		if !enc.u64(uint64(i)) || !enc.str(string(block.kind)) || !enc.str(block.mime) || !enc.str(string(block.detail)) {
			return 0, "", errBudget
		}
		payload := []byte(block.text)
		if block.kind == models.ContentImage {
			payload = block.data
		}
		for off := 0; off < len(payload); off += frameChunk {
			end := off + frameChunk
			if end > len(payload) {
				end = len(payload)
			}
			if !enc.bytes(payload[off:end]) {
				return 0, "", errBudget
			}
		}
		if !enc.u64(0) || !enc.u64(uint64(len(payload))) || !enc.str(blockBoundary) {
			return 0, "", errBudget
		}
	}
	return enc.n, hex.EncodeToString(enc.h.Sum(nil)), nil
}

type frameHashEnc struct {
	h hash.Hash
	n int64
}

func (e *frameHashEnc) write(p []byte) bool {
	next, ok := add64(e.n, int64(len(p)))
	if !ok {
		return false
	}
	_, _ = e.h.Write(p)
	e.n = next
	return true
}

func (e *frameHashEnc) u64(v uint64) bool {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], v)
	return e.write(raw[:])
}

func (e *frameHashEnc) bytes(p []byte) bool {
	return e.u64(uint64(len(p))) && e.write(p)
}

func (e *frameHashEnc) str(s string) bool { return e.bytes([]byte(s)) }

type frameEnc struct{ buf []byte }

func (e *frameEnc) u64(v uint64) {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], v)
	e.buf = append(e.buf, raw[:]...)
}

func (e *frameEnc) bytes(p []byte) {
	e.u64(uint64(len(p)))
	e.buf = append(e.buf, p...)
}

func (e *frameEnc) str(s string) { e.bytes([]byte(s)) }

func sha256Hex(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

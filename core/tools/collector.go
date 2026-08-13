package tools

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sync"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

// CollectorMode selects how a bounded text collector retains a stream.
type CollectorMode uint8

const (
	// HeadTail keeps a prefix and suffix of the logical stream.
	HeadTail CollectorMode = iota + 1
	// Prefix keeps a leading prefix and reserves a stable continuation boundary.
	Prefix
)

const (
	metadataBegin  = "\n@serpe-tool-metadata-begin:v1\n"
	metadataEndFmt = "\n@serpe-tool-metadata-end:v1 bytes=%08x"
	collectorChunk = 32 << 10
)

// PrefixState is the frozen body boundary returned by PreparePrefix.
type PrefixState struct {
	KeptLogicalBytes int64
	Truncated        bool
}

type collectorState uint8

const (
	collectorWriting collectorState = iota
	collectorPrepared
	collectorFinished
)

// TextCollector is a one-shot bounded writer for a single logical text
// stream. Its retained memory is proportional to the configured output
// window, not to the number of bytes written.
type TextCollector struct {
	limits OutputLimits
	mode   CollectorMode

	mu      sync.Mutex
	state   collectorState
	pending []byte // at most one incomplete UTF-8 sequence
	total   int64
	hasher  *streamHasher

	bodyBudget int64
	window     []byte
	headBytes  int
	omitted    bool
	prepared   PrefixState

	// recordMode is private to TextCollectorGroup. In this mode head/tail
	// retention never cuts a source record.
	recordMode bool
	recordFull [][]byte
	recordHead [][]byte
	recordTail [][]byte
	recordN    int64
}

// NewTextCollector constructs a collector bound to the invocation limits.
func NewTextCollector(limits OutputLimits, mode CollectorMode) (*TextCollector, error) {
	if mode != HeadTail && mode != Prefix {
		return nil, wrapExecution("unknown collector mode")
	}
	if limits.MaxTextBytes < minTextBytes || limits.MaxFramedBytes < minFramedBytes {
		return nil, wrapExecution("collector limits are below the package floor")
	}
	reservation := int64(len(metadataBegin) + maxCollectorMetaBytes + len(fmt.Sprintf(metadataEndFmt, maxCollectorMetaBytes)))
	marker := int64(len(formatMarker(0, 0, sha256Hex(nil))))
	bodyBudget := maxCollectorBody(limits, reservation, marker)
	return &TextCollector{
		limits:     limits,
		mode:       mode,
		hasher:     newStreamHasher(),
		bodyBudget: bodyBudget,
	}, nil
}

func maxCollectorBody(limits OutputLimits, trailerReserve, markerReserve int64) int64 {
	hi := limits.MaxTextBytes - trailerReserve - markerReserve
	if hi < 0 {
		return 0
	}
	lo := int64(0)
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		framed, ok := singleTextFrameSize(mid + trailerReserve + markerReserve)
		if ok && framed <= limits.MaxFramedBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

func singleTextFrameSize(textBytes int64) (int64, bool) {
	block, ok := framedBlockSize(string(models.ContentText), "", "", textBytes)
	if !ok {
		return 0, false
	}
	return add64(int64(8+len(outputDomain)+8+8), block)
}

// Write consumes raw bytes, escaping illegal UTF-8 deterministically.
func (c *TextCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != collectorWriting {
		return 0, wrapExecution("collector is no longer writable")
	}
	if c.recordMode {
		return 0, wrapExecution("record collector cannot be written directly")
	}
	if err := c.consumeRaw(p, false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *TextCollector) consumeRaw(p []byte, final bool) error {
	for len(p) > 0 {
		n := len(p)
		if n > collectorChunk {
			n = collectorChunk
		}
		chunk := p[:n]
		if len(c.pending) == 0 && utf8.Valid(chunk) {
			if err := c.acceptLogical(chunk); err != nil {
				return err
			}
			p = p[n:]
			continue
		}
		buf := make([]byte, 0, len(c.pending)+n)
		buf = append(buf, c.pending...)
		buf = append(buf, chunk...)
		c.pending = nil
		logical, rest := escapeValidPrefix(buf, final && n == len(p))
		c.pending = append(c.pending, rest...)
		if err := c.acceptLogical(logical); err != nil {
			return err
		}
		p = p[n:]
	}
	if final && len(c.pending) > 0 {
		logical := escapeBytes(c.pending)
		c.pending = nil
		return c.acceptLogical(logical)
	}
	return nil
}

func escapeValidPrefix(in []byte, final bool) (logical, rest []byte) {
	logical = make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		if in[i] < utf8.RuneSelf {
			logical = append(logical, in[i])
			i++
			continue
		}
		if !utf8.FullRune(in[i:]) && !final {
			return logical, in[i:]
		}
		r, n := utf8.DecodeRune(in[i:])
		if r == utf8.RuneError && n == 1 {
			logical = appendEscapedByte(logical, in[i])
			i++
			continue
		}
		logical = append(logical, in[i:i+n]...)
		i += n
	}
	return logical, nil
}

func (c *TextCollector) acceptLogical(logical []byte) error {
	if len(logical) == 0 {
		return nil
	}
	next, ok := add64(c.total, int64(len(logical)))
	if !ok {
		return errBudget
	}
	if !c.hasher.writeLogical(logical) {
		return errBudget
	}
	c.total = next
	switch c.mode {
	case Prefix:
		need := c.bodyBudget - int64(len(c.window))
		if need <= 0 {
			c.omitted = true
			return nil
		}
		if int64(len(logical)) > need {
			logical = logical[:int(need)]
			for len(logical) > 0 && !utf8.Valid(logical) {
				logical = logical[:len(logical)-1]
			}
			c.omitted = true
		}
		c.window = append(c.window, logical...)
	case HeadTail:
		c.appendHeadTail(logical)
	}
	return nil
}

func (c *TextCollector) appendHeadTail(logical []byte) {
	budget := int(c.bodyBudget)
	if budget <= 0 {
		c.omitted = true
		return
	}
	if !c.omitted && len(c.window)+len(logical) <= budget {
		c.window = append(c.window, logical...)
		c.headBytes = len(c.window)
		return
	}
	headBudget := budget / 2
	tailBudget := budget - headBudget
	if !c.omitted {
		old := c.window
		retained := make([]byte, 0, budget)
		if len(old) >= headBudget {
			retained = append(retained, utf8PrefixView(old, headBudget)...)
		} else {
			retained = append(retained, old...)
			retained = append(retained, utf8PrefixView(logical, headBudget-len(retained))...)
		}
		c.headBytes = len(retained)
		retained = appendCombinedSuffix(retained, old, logical, tailBudget)
		c.window = retained
		c.omitted = true
		return
	}
	oldTail := c.window[c.headBytes:]
	var fromOld, fromLogical []byte
	if len(logical) >= tailBudget {
		fromLogical = utf8SuffixView(logical, tailBudget)
	} else {
		fromOld = utf8SuffixView(oldTail, tailBudget-len(logical))
		fromLogical = logical
	}
	tailLen := len(fromOld) + len(fromLogical)
	need := c.headBytes + tailLen
	if need > cap(c.window) {
		grown := make([]byte, c.headBytes, budget)
		copy(grown, c.window[:c.headBytes])
		c.window = grown
	}
	c.window = c.window[:need]
	copy(c.window[c.headBytes:], fromOld)
	copy(c.window[c.headBytes+len(fromOld):], fromLogical)
}

func appendCombinedSuffix(dst, first, second []byte, budget int) []byte {
	if len(second) >= budget {
		return append(dst, utf8SuffixView(second, budget)...)
	}
	dst = append(dst, utf8SuffixView(first, budget-len(second))...)
	return append(dst, second...)
}

// PreparePrefix freezes the retained body and exposes its exact logical byte
// boundary. It is required exactly once before Output in Prefix mode.
func (c *TextCollector) PreparePrefix() (PrefixState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mode != Prefix {
		return PrefixState{}, wrapExecution("PreparePrefix is only valid for Prefix collectors")
	}
	if c.state != collectorWriting {
		return PrefixState{}, wrapExecution("PreparePrefix called out of order")
	}
	if err := c.consumeRaw(nil, true); err != nil {
		return PrefixState{}, err
	}
	c.prepared = PrefixState{
		KeptLogicalBytes: int64(len(c.window)),
		Truncated:        c.omitted || c.total > int64(len(c.window)),
	}
	c.state = collectorPrepared
	return c.prepared, nil
}

// Output finishes the collector. A metadata/trailer budget overflow returns a
// fixed IsError output and nil error. Structural/lifecycle mistakes are fatal.
func (c *TextCollector) Output(metadata json.RawMessage, isError bool) (Output, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == collectorFinished {
		return Output{}, wrapExecution("collector already finished")
	}
	if c.mode == Prefix && c.state != collectorPrepared {
		return Output{}, wrapExecution("Prefix collector must be prepared before Output")
	}
	if c.mode == HeadTail && c.state != collectorWriting {
		return Output{}, wrapExecution("collector Output called out of order")
	}
	if c.mode == HeadTail {
		if err := c.consumeRaw(nil, true); err != nil {
			return Output{}, err
		}
	}
	c.state = collectorFinished

	canonical, err := parseCollectorMetadata(metadata)
	if err != nil {
		if errors.Is(err, ErrOutputLimit) {
			return budgetCollectorOutput(), nil
		}
		return Output{}, err
	}
	trailer := renderMetadataTrailer(canonical)
	maxTrailer := len(metadataBegin) + maxCollectorMetaBytes + len(fmt.Sprintf(metadataEndFmt, maxCollectorMetaBytes))
	if len(trailer) > maxTrailer {
		return budgetCollectorOutput(), nil
	}
	if !c.hasher.writeLogical(trailer) {
		return budgetCollectorOutput(), nil
	}
	original, ok := c.hasher.finish()
	if !ok {
		return budgetCollectorOutput(), nil
	}

	body, head := c.retainedBody()
	truncated := c.total != int64(len(body)) || c.omitted
	logical := append(append([]byte(nil), body...), trailer...)
	if truncated {
		logical = renderCollectorTruncation(body, head, trailer, c.mode, original)
	}
	if overLimit(logical, c.limits) {
		return budgetCollectorOutput(), nil
	}
	block := []contentBlock{{kind: models.ContentText, text: string(logical)}}
	keptBytes, retained, err := digestBlocks(block)
	if err != nil {
		return Output{}, err
	}
	if truncated {
		// kept_framed_bytes is fixed-width, so replacing its zero placeholder
		// cannot change the frame size.
		logical = renderCollectorTruncationWithKept(body, head, trailer, c.mode, original, uint64(keptBytes))
		block[0].text = string(logical)
		actual, digest, err := digestBlocks(block)
		if err != nil || actual != keptBytes {
			return Output{}, wrapExecution("collector truncation frame invariant failed")
		}
		retained = digest
	}
	out := Output{Content: []models.Content{models.Text(string(logical))}, IsError: isError}
	out.receipt = &collectorReceipt{
		limits:        c.limits,
		originalBytes: original.bytes,
		originalSHA:   original.sha,
		keptBytes:     keptBytes,
		truncated:     truncated,
		isError:       isError,
		retained:      retained,
		metadata:      append([]byte(nil), canonical...),
	}
	return out, nil
}

func (c *TextCollector) retainedBody() ([]byte, int) {
	if !c.recordMode {
		return append([]byte(nil), c.window...), c.headBytes
	}
	var records [][]byte
	head := 0
	if !c.omitted {
		records = c.recordFull
		head = int(c.recordN)
	} else {
		records = append(append([][]byte(nil), c.recordHead...), c.recordTail...)
		for _, record := range c.recordHead {
			head += len(record)
		}
	}
	retained := 0
	for _, record := range records {
		retained += len(record)
	}
	body := make([]byte, 0, retained)
	for _, record := range records {
		body = append(body, record...)
	}
	if !c.omitted {
		head = len(body)
	}
	return body, head
}

func renderCollectorTruncation(body []byte, head int, trailer []byte, mode CollectorMode, original hashedFrame) []byte {
	return renderCollectorTruncationWithKept(body, head, trailer, mode, original, 0)
}

func renderCollectorTruncationWithKept(body []byte, head int, trailer []byte, mode CollectorMode, original hashedFrame, kept uint64) []byte {
	marker := []byte(formatMarker(kept, uint64(original.bytes), original.sha))
	if mode == Prefix {
		out := make([]byte, 0, len(body)+len(marker)+len(trailer))
		out = append(out, body...)
		out = append(out, marker...)
		return append(out, trailer...)
	}
	if head < 0 || head > len(body) {
		head = len(body) / 2
	}
	out := make([]byte, 0, len(body)+len(marker)+len(trailer))
	out = append(out, body[:head]...)
	out = append(out, marker...)
	out = append(out, body[head:]...)
	return append(out, trailer...)
}

func parseCollectorMetadata(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > maxCollectorMetaBytes {
		return nil, errBudget
	}
	value, err := jsonvalue.ParseObject(raw, jsonvalue.Limits{
		MaxDepth:       maxCollectorMetaDepth,
		MaxNodes:       maxCollectorMetaNodes,
		MaxNumberBytes: 128,
		MaxExponent:    1000,
		MaxScale:       1024,
	})
	if err != nil {
		return nil, wrapExecution("collector metadata: %v", err)
	}
	canonical, err := jsonvalue.CanonicalValue(value)
	if err != nil {
		return nil, wrapExecution("collector metadata: %v", err)
	}
	if len(canonical) > maxCollectorMetaBytes {
		return nil, errBudget
	}
	return canonical, nil
}

func renderMetadataTrailer(canonical []byte) []byte {
	out := make([]byte, 0, len(metadataBegin)+len(canonical)+64)
	out = append(out, metadataBegin...)
	out = append(out, canonical...)
	out = append(out, fmt.Sprintf(metadataEndFmt, len(canonical))...)
	return out
}

func overLimit(logical []byte, limits OutputLimits) bool {
	if int64(len(logical)) > limits.MaxTextBytes {
		return true
	}
	framed, ok := singleTextFrameSize(int64(len(logical)))
	return !ok || framed > limits.MaxFramedBytes
}

func budgetCollectorOutput() Output { return Error(budgetErrorText) }

func utf8Prefix(in []byte, n int) []byte {
	return append([]byte(nil), utf8PrefixView(in, n)...)
}

func utf8PrefixView(in []byte, n int) []byte {
	if n >= len(in) {
		return in
	}
	if n < 0 {
		n = 0
	}
	for n > 0 && !utf8.Valid(in[:n]) {
		n--
	}
	return in[:n]
}

func utf8Suffix(in []byte, n int) []byte {
	return append([]byte(nil), utf8SuffixView(in, n)...)
}

func utf8SuffixView(in []byte, n int) []byte {
	if n >= len(in) {
		return in
	}
	if n < 0 {
		n = 0
	}
	start := len(in) - n
	for start < len(in) && !utf8.Valid(in[start:]) {
		start++
	}
	return in[start:]
}

func appendEscapedByte(dst []byte, b byte) []byte {
	const digits = "0123456789abcdef"
	return append(dst, '\\', 'x', digits[b>>4], digits[b&0x0f])
}

func escapeBytes(in []byte) []byte {
	out := make([]byte, 0, len(in)*4)
	for _, b := range in {
		out = appendEscapedByte(out, b)
	}
	return out
}

type streamHasher struct {
	h       hash.Hash
	pending []byte
	total   int64
	done    bool
}

type hashedFrame struct {
	bytes int64
	sha   string
}

func newStreamHasher() *streamHasher {
	h := &streamHasher{h: sha256.New()}
	var enc frameEnc
	enc.str(outputDomain)
	enc.u64(outputVersion)
	enc.u64(1)
	enc.u64(0)
	enc.str(string(models.ContentText))
	enc.str("")
	enc.str("")
	_, _ = h.h.Write(enc.buf)
	return h
}

func (s *streamHasher) writeLogical(p []byte) bool {
	if s.done {
		return false
	}
	next, ok := add64(s.total, int64(len(p)))
	if !ok {
		return false
	}
	s.total = next
	for len(p) > 0 {
		if len(s.pending) == 0 && len(p) >= frameChunk {
			s.emit(p[:frameChunk])
			p = p[frameChunk:]
			continue
		}
		need := frameChunk - len(s.pending)
		if need > len(p) {
			need = len(p)
		}
		s.pending = append(s.pending, p[:need]...)
		p = p[need:]
		if len(s.pending) == frameChunk {
			s.emit(s.pending)
			s.pending = s.pending[:0]
		}
	}
	return true
}

func (s *streamHasher) emit(chunk []byte) {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(len(chunk)))
	_, _ = s.h.Write(raw[:])
	_, _ = s.h.Write(chunk)
}

func (s *streamHasher) finish() (hashedFrame, bool) {
	if s.done {
		return hashedFrame{}, false
	}
	s.done = true
	if len(s.pending) > 0 {
		s.emit(s.pending)
		s.pending = nil
	}
	var enc frameEnc
	enc.u64(0)
	enc.u64(uint64(s.total))
	enc.str(blockBoundary)
	_, _ = s.h.Write(enc.buf)
	framed, ok := singleTextFrameSize(s.total)
	if !ok {
		return hashedFrame{}, false
	}
	return hashedFrame{bytes: framed, sha: hex.EncodeToString(s.h.Sum(nil))}, true
}

// TextCollectorGroup shares one text/framed quota across named sources.
type TextCollectorGroup struct {
	mu      sync.Mutex
	closed  bool
	names   []string
	sources map[string]*sourceState
	col     *TextCollector
}

type sourceState struct {
	pending []byte
	record  []byte
}

// NewTextCollectorGroup constructs a multi-source HeadTail collector.
func NewTextCollectorGroup(limits OutputLimits, mode CollectorMode, sourceNames ...string) (*TextCollectorGroup, error) {
	if mode != HeadTail {
		return nil, wrapExecution("collector groups only support HeadTail mode")
	}
	if len(sourceNames) < 1 || len(sourceNames) > 16 {
		return nil, wrapExecution("collector group must have 1–16 sources")
	}
	seen := make(map[string]struct{}, len(sourceNames))
	for _, name := range sourceNames {
		if !validSourceName(name) {
			return nil, wrapExecution("invalid collector source name %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, wrapExecution("duplicate collector source name %q", name)
		}
		seen[name] = struct{}{}
	}
	col, err := NewTextCollector(limits, HeadTail)
	if err != nil {
		return nil, err
	}
	col.recordMode = true
	g := &TextCollectorGroup{
		names:   append([]string(nil), sourceNames...),
		sources: make(map[string]*sourceState, len(sourceNames)),
		col:     col,
	}
	for _, name := range sourceNames {
		g.sources[name] = &sourceState{}
	}
	return g, nil
}

func validSourceName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	if name[0] != '_' && (name[0] < 'A' || name[0] > 'Z') && (name[0] < 'a' || name[0] > 'z') {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			continue
		}
		return false
	}
	return true
}

// WriteSource appends raw bytes for one named source.
func (g *TextCollectorGroup) WriteSource(name string, p []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return wrapExecution("collector group already finished")
	}
	source, ok := g.sources[name]
	if !ok {
		return wrapExecution("unknown collector source %q", name)
	}
	return g.consumeSource(name, source, p, false)
}

func (g *TextCollectorGroup) consumeSource(name string, source *sourceState, p []byte, final bool) error {
	for len(p) > 0 {
		n := len(p)
		if n > collectorChunk {
			n = collectorChunk
		}
		buf := make([]byte, 0, len(source.pending)+n)
		buf = append(buf, source.pending...)
		buf = append(buf, p[:n]...)
		source.pending = nil
		if err := g.consumeSourceChunk(name, source, buf, false); err != nil {
			return err
		}
		p = p[n:]
	}
	if final {
		if err := g.consumeSourceChunk(name, source, source.pending, true); err != nil {
			return err
		}
		source.pending = nil
		if len(source.record) > 0 {
			if err := g.flushRecord(name, source.record); err != nil {
				return err
			}
			source.record = nil
		}
	}
	return nil
}

func (g *TextCollectorGroup) consumeSourceChunk(name string, source *sourceState, buf []byte, final bool) error {
	for i := 0; i < len(buf); {
		var unit []byte
		if buf[i] < utf8.RuneSelf {
			unit = buf[i : i+1]
			i++
		} else if !utf8.FullRune(buf[i:]) && !final {
			source.pending = append(source.pending, buf[i:]...)
			break
		} else {
			r, n := utf8.DecodeRune(buf[i:])
			if r == utf8.RuneError && n == 1 {
				unit = appendEscapedByte(nil, buf[i])
				i++
			} else {
				unit = buf[i : i+n]
				i += n
			}
		}
		if len(source.record)+len(unit) > frameChunk {
			if err := g.flushRecord(name, source.record); err != nil {
				return err
			}
			source.record = source.record[:0]
		}
		source.record = append(source.record, unit...)
		if len(source.record) == frameChunk {
			if err := g.flushRecord(name, source.record); err != nil {
				return err
			}
			source.record = source.record[:0]
		}
	}
	return nil
}

func (g *TextCollectorGroup) flushRecord(name string, payload []byte) error {
	record := formatSourceRecord(name, payload)
	return g.col.acceptRecord(record)
}

func formatSourceRecord(name string, payload []byte) []byte {
	header := fmt.Sprintf("@serpe-source:v1 name=%s bytes=%08x\n", name, len(payload))
	record := make([]byte, 0, len(header)+len(payload))
	record = append(record, header...)
	record = append(record, payload...)
	return record
}

func (c *TextCollector) acceptRecord(record []byte) error {
	if !utf8.Valid(record) {
		return wrapExecution("collector source record is not valid UTF-8")
	}
	next, ok := add64(c.total, int64(len(record)))
	if !ok || !c.hasher.writeLogical(record) {
		return errBudget
	}
	c.total = next
	c.recordN += int64(len(record))
	copyRecord := append([]byte(nil), record...)
	if !c.omitted {
		c.recordFull = append(c.recordFull, copyRecord)
		if c.recordN <= c.bodyBudget {
			return nil
		}
		c.partitionRecords()
		return nil
	}
	c.recordTail = append(c.recordTail, copyRecord)
	c.trimRecordTail()
	return nil
}

func (c *TextCollector) partitionRecords() {
	headBudget := c.bodyBudget / 2
	tailBudget := c.bodyBudget - headBudget
	var used int64
	for _, record := range c.recordFull {
		if used+int64(len(record)) > headBudget {
			break
		}
		c.recordHead = append(c.recordHead, record)
		used += int64(len(record))
	}
	used = 0
	for i := len(c.recordFull) - 1; i >= len(c.recordHead); i-- {
		record := c.recordFull[i]
		if used+int64(len(record)) > tailBudget {
			continue
		}
		c.recordTail = append([][]byte{record}, c.recordTail...)
		used += int64(len(record))
	}
	c.recordFull = nil
	c.omitted = true
}

func (c *TextCollector) trimRecordTail() {
	budget := c.bodyBudget - c.bodyBudget/2
	var total int64
	for _, record := range c.recordTail {
		total += int64(len(record))
	}
	for total > budget && len(c.recordTail) > 0 {
		total -= int64(len(c.recordTail[0]))
		c.recordTail[0] = nil
		c.recordTail = c.recordTail[1:]
	}
}

// Output finishes every source in declaration order and returns one text Output.
func (g *TextCollectorGroup) Output(metadata json.RawMessage, isError bool) (Output, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return Output{}, wrapExecution("collector group already finished")
	}
	g.closed = true
	for _, name := range g.names {
		if err := g.consumeSource(name, g.sources[name], nil, true); err != nil {
			return Output{}, err
		}
	}
	return g.col.Output(metadata, isError)
}

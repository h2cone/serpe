package models

import (
	"fmt"
	"math"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/internal/jsonvalue"
)

// StreamLimits bound one normalized provider turn's event envelope. Zero
// fields select the package hard ceilings; positive fields may only tighten
// them. Tool-call identity and argument grammar live in tools.InputLimits.
type StreamLimits struct {
	MaxEventBytes      int64
	MaxTurnBytes       int64
	MaxEvents          int
	MaxCandidates      int
	MaxParts           int
	MaxMetadataEntries int
	MaxToolCalls       int
}

const (
	defaultStreamEventBytes      = int64(2 << 20)
	defaultStreamTurnBytes       = int64(32 << 20)
	defaultStreamEvents          = 65_536
	defaultStreamCandidates      = 8
	defaultStreamParts           = 4_096
	defaultStreamMetadataEntries = 256
	defaultStreamToolCalls       = 128
	maxTerminalFinishes          = 8
	streamFrameNodeBytes         = int64(16)
)

// NormalizeStreamLimits applies package ceilings. Zero fields become those
// ceilings; positive fields may only tighten them.
func NormalizeStreamLimits(limits StreamLimits) (StreamLimits, error) {
	return normalizeStreamLimits(limits)
}

func normalizeStreamLimits(limits StreamLimits) (StreamLimits, error) {
	var err error
	if limits.MaxEventBytes, err = streamInt64Limit(limits.MaxEventBytes, defaultStreamEventBytes, 512, "MaxEventBytes"); err != nil {
		return StreamLimits{}, err
	}
	if limits.MaxTurnBytes, err = streamInt64Limit(limits.MaxTurnBytes, defaultStreamTurnBytes, 1<<10, "MaxTurnBytes"); err != nil {
		return StreamLimits{}, err
	}
	if limits.MaxTurnBytes < limits.MaxEventBytes {
		return StreamLimits{}, fmt.Errorf("MaxTurnBytes must be at least MaxEventBytes")
	}
	if limits.MaxEvents, err = streamIntLimit(limits.MaxEvents, defaultStreamEvents, 2, "MaxEvents"); err != nil {
		return StreamLimits{}, err
	}
	if limits.MaxCandidates, err = streamIntLimit(limits.MaxCandidates, defaultStreamCandidates, 1, "MaxCandidates"); err != nil {
		return StreamLimits{}, err
	}
	if limits.MaxParts, err = streamIntLimit(limits.MaxParts, defaultStreamParts, 1, "MaxParts"); err != nil {
		return StreamLimits{}, err
	}
	if limits.MaxMetadataEntries, err = streamIntLimit(limits.MaxMetadataEntries, defaultStreamMetadataEntries, 1, "MaxMetadataEntries"); err != nil {
		return StreamLimits{}, err
	}
	if limits.MaxToolCalls, err = streamIntLimit(limits.MaxToolCalls, defaultStreamToolCalls, 1, "MaxToolCalls"); err != nil {
		return StreamLimits{}, err
	}
	return limits, nil
}

func streamInt64Limit(value, ceiling, minimum int64, name string) (int64, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	if value == 0 {
		return ceiling, nil
	}
	if value < minimum || value > ceiling {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, ceiling)
	}
	return value, nil
}

func streamIntLimit(value, ceiling, minimum int, name string) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	if value == 0 {
		return ceiling, nil
	}
	if value < minimum || value > ceiling {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, ceiling)
	}
	return value, nil
}

type streamLimiter struct {
	limits          StreamLimits
	events          int
	turnBytes       int64
	metadataEntries int
	toolCalls       int
	candidates      map[int]struct{}
	parts           map[partKey]ContentKind
}

func newStreamLimiter(limits StreamLimits) *streamLimiter {
	return &streamLimiter{
		limits:     limits,
		candidates: make(map[int]struct{}),
		parts:      make(map[partKey]ContentKind),
	}
}

func (l *streamLimiter) accept(event Event) error {
	if l.events >= l.limits.MaxEvents {
		return fmt.Errorf("model event count exceeds %d", l.limits.MaxEvents)
	}
	if err := l.validateIndexes(event); err != nil {
		return err
	}
	size, err := measureEventFrame(event, l.limits.MaxEventBytes)
	if err != nil {
		return err
	}
	if size > l.limits.MaxEventBytes {
		return fmt.Errorf("model event frame exceeds %d bytes", l.limits.MaxEventBytes)
	}
	if size > l.limits.MaxTurnBytes-l.turnBytes {
		return fmt.Errorf("model turn frame exceeds %d bytes", l.limits.MaxTurnBytes)
	}
	if err := l.acceptStructure(event); err != nil {
		return err
	}
	l.events++
	l.turnBytes += size
	return nil
}

func (l *streamLimiter) validateIndexes(event Event) error {
	if event.Kind == EventPartStart || event.Kind == EventPartDelta || event.Kind == EventPartEnd {
		if event.CandidateIndex < 0 || event.CandidateIndex >= l.limits.MaxCandidates {
			return fmt.Errorf("candidate index %d exceeds limit", event.CandidateIndex)
		}
		if event.PartIndex < 0 || event.PartIndex >= l.limits.MaxParts {
			return fmt.Errorf("part index %d exceeds limit", event.PartIndex)
		}
	}
	if len(event.Finishes) > maxTerminalFinishes {
		return fmt.Errorf("response_end has more than %d finishes", maxTerminalFinishes)
	}
	seen := make(map[int]struct{}, len(event.Finishes))
	for _, finish := range event.Finishes {
		if finish.CandidateIndex < 0 || finish.CandidateIndex >= l.limits.MaxCandidates {
			return fmt.Errorf("finish candidate index %d exceeds limit", finish.CandidateIndex)
		}
		if _, exists := seen[finish.CandidateIndex]; exists {
			return fmt.Errorf("duplicate finish candidate index %d", finish.CandidateIndex)
		}
		seen[finish.CandidateIndex] = struct{}{}
	}
	return nil
}

func (l *streamLimiter) acceptStructure(event Event) error {
	metadata := 0
	if event.Response != nil {
		metadata = len(event.Response.Metadata)
	}
	if metadata > l.limits.MaxMetadataEntries-l.metadataEntries {
		return fmt.Errorf("model response metadata exceeds %d entries", l.limits.MaxMetadataEntries)
	}

	newCandidate := false
	newPart := false
	key := partKey{candidate: event.CandidateIndex, part: event.PartIndex}
	if event.Kind == EventPartStart {
		_, newCandidate = l.candidates[event.CandidateIndex]
		newCandidate = !newCandidate
		_, exists := l.parts[key]
		newPart = !exists
		if newPart && len(l.parts) >= l.limits.MaxParts {
			return fmt.Errorf("model part count exceeds %d", l.limits.MaxParts)
		}
		if newCandidate && len(l.candidates) >= l.limits.MaxCandidates {
			return fmt.Errorf("model candidate count exceeds %d", l.limits.MaxCandidates)
		}
	}
	for _, finish := range event.Finishes {
		if _, exists := l.candidates[finish.CandidateIndex]; !exists {
			if len(l.candidates) >= l.limits.MaxCandidates {
				return fmt.Errorf("model candidate count exceeds %d", l.limits.MaxCandidates)
			}
			l.candidates[finish.CandidateIndex] = struct{}{}
		}
	}

	if event.Kind == EventPartStart && event.Part.Kind == ContentToolCall && event.Part.ToolCall != nil {
		if l.toolCalls >= l.limits.MaxToolCalls {
			return fmt.Errorf("model tool call count exceeds %d", l.limits.MaxToolCalls)
		}
		l.toolCalls++
	}

	l.metadataEntries += metadata
	if event.Kind == EventPartStart {
		if newCandidate {
			l.candidates[event.CandidateIndex] = struct{}{}
		}
		if newPart {
			l.parts[key] = event.Part.Kind
		}
	}
	return nil
}

// BoundedIdentity checks UTF-8, control characters, and a byte ceiling.
func BoundedIdentity(value string, limit int64, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("is empty")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("is not valid UTF-8")
	}
	if int64(len(value)) > limit {
		return fmt.Errorf("exceeds %d bytes", limit)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("contains a control character")
		}
	}
	return nil
}

type eventFrameCounter struct {
	size int64
	max  int64
}

func (c *eventFrameCounter) add(size int64) error {
	if size < 0 || size > math.MaxInt64-c.size {
		return fmt.Errorf("model event frame size overflow")
	}
	c.size += size
	if c.size > c.max {
		return fmt.Errorf("model event frame exceeds %d bytes", c.max)
	}
	return nil
}

func (c *eventFrameCounter) node() error { return c.add(streamFrameNodeBytes) }

func (c *eventFrameCounter) string(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("model event contains invalid UTF-8")
	}
	if err := c.node(); err != nil {
		return err
	}
	return c.add(int64(len(value)))
}

func (c *eventFrameCounter) bytes(value []byte) error {
	if err := c.node(); err != nil {
		return err
	}
	return c.add(int64(len(value)))
}

func (c *eventFrameCounter) raw(value []byte) error {
	if len(value) == 0 {
		return c.node()
	}
	if int64(len(value)) > c.max-c.size {
		return fmt.Errorf("model event raw JSON exceeds frame limit")
	}
	parsed, err := jsonvalue.Parse(value, jsonvalue.ObjectLimits())
	if err != nil {
		return fmt.Errorf("model event contains invalid raw JSON: %w", err)
	}
	if err := c.bytes(value); err != nil {
		return err
	}
	return c.jsonValue(parsed)
}

func (c *eventFrameCounter) jsonValue(value jsonvalue.Value) error {
	if err := c.node(); err != nil {
		return err
	}
	switch value.Kind {
	case jsonvalue.KindNumber:
		return c.add(int64(len(value.Number)))
	case jsonvalue.KindString:
		return c.add(int64(len(value.String)))
	case jsonvalue.KindArray:
		for i := range value.Array {
			if err := c.jsonValue(value.Array[i]); err != nil {
				return err
			}
		}
	case jsonvalue.KindObject:
		for i := range value.Object {
			if err := c.string(value.Object[i].Key); err != nil {
				return err
			}
			if err := c.jsonValue(value.Object[i].Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func measureEventFrame(event Event, max int64) (int64, error) {
	if err := validateEventShape(event); err != nil {
		return 0, err
	}
	c := &eventFrameCounter{max: max}
	if err := c.string("serpe.models.event.v1"); err != nil {
		return 0, err
	}
	for _, value := range []string{string(event.Kind), event.ProviderEventID, event.ItemID, event.CallID} {
		if err := c.string(value); err != nil {
			return 0, err
		}
	}
	if err := c.add(3 * streamFrameNodeBytes); err != nil { // indexes and presence tags
		return 0, err
	}
	if contentSet(event.Part) {
		if err := measureContentFrame(c, event.Part); err != nil {
			return 0, err
		}
	}
	if deltaSet(event.Delta) {
		if err := c.string(string(event.Delta.Kind)); err != nil {
			return 0, err
		}
		if err := c.string(event.Delta.Text); err != nil {
			return 0, err
		}
		if err := c.bytes(event.Delta.Media); err != nil {
			return 0, err
		}
	}
	if event.Response != nil {
		if err := measureResponseInfoFrame(c, event.Response); err != nil {
			return 0, err
		}
	}
	for i := range event.Finishes {
		finish := &event.Finishes[i]
		if err := c.node(); err != nil {
			return 0, err
		}
		if err := c.string(string(finish.Reason)); err != nil {
			return 0, err
		}
		if err := c.string(finish.RawReason); err != nil {
			return 0, err
		}
		if err := measureProviderStateFrame(c, finish.ProviderState); err != nil {
			return 0, err
		}
	}
	if event.Usage != nil {
		if err := c.add(7 * streamFrameNodeBytes); err != nil {
			return 0, err
		}
		if err := c.raw(event.Usage.Raw); err != nil {
			return 0, err
		}
	}
	return c.size, nil
}

func measureResponseInfoFrame(c *eventFrameCounter, info *ResponseInfo) error {
	if err := c.node(); err != nil {
		return err
	}
	for _, value := range []string{info.Provider, info.ID, info.Model, string(info.Status), info.RequestID, info.CreatedAt.String()} {
		if err := c.string(value); err != nil {
			return err
		}
	}
	for key, value := range info.Metadata {
		if err := c.string(key); err != nil {
			return err
		}
		if err := c.string(value); err != nil {
			return err
		}
	}
	return measureProviderStateFrame(c, info.ProviderState)
}

func measureProviderStateFrame(c *eventFrameCounter, state *ProviderState) error {
	if state == nil {
		return c.node()
	}
	if err := c.string(state.Provider); err != nil {
		return err
	}
	return c.raw(state.Data)
}

func measureContentFrame(c *eventFrameCounter, content Content) error {
	if err := c.node(); err != nil {
		return err
	}
	if err := c.string(string(content.Kind)); err != nil {
		return err
	}
	switch content.Kind {
	case ContentText:
		return c.string(content.Text.Text)
	case ContentImage:
		for _, value := range []string{content.Image.URI, content.Image.MIMEType, string(content.Image.Detail)} {
			if err := c.string(value); err != nil {
				return err
			}
		}
		return c.bytes(content.Image.Data)
	case ContentToolCall:
		if err := c.string(content.ToolCall.ID); err != nil {
			return err
		}
		if err := c.string(content.ToolCall.Name); err != nil {
			return err
		}
		return c.raw(content.ToolCall.Arguments)
	case ContentToolResult:
		if err := c.string(content.ToolResult.CallID); err != nil {
			return err
		}
		if err := c.string(content.ToolResult.Name); err != nil {
			return err
		}
		if err := c.node(); err != nil {
			return err
		}
		for i := range content.ToolResult.Content {
			if err := measureContentFrame(c, content.ToolResult.Content[i]); err != nil {
				return err
			}
		}
		return nil
	case ContentReasoningSummary:
		return c.string(content.ReasoningSummary.Text)
	case ContentRefusal:
		return c.string(content.Refusal.Text)
	default:
		return fmt.Errorf("unknown content kind %q", content.Kind)
	}
}

func responseLimitError(provider, message string, cause error) error {
	return &Error{
		Kind: ErrorProtocol, Provider: provider, Operation: "stream_next",
		Code: "response_limit", Message: message, Cause: cause,
	}
}

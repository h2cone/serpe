package canon

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Assemble reconstructs a complete Response from a canonical event stream. The
// event sequence unambiguously rebuilds Response.Content: ContentBlockStart
// opens a block, ContentBlockDelta accumulates into it, ContentBlockStop seals
// it. Used by the agent's streaming path and codec/provider tests.
func Assemble(events <-chan Event) (*Response, error) {
	a := &assembler{
		blocks: map[int]ContentBlock{},
		order:  []int{},
		inputs: map[int]*strings.Builder{},
	}
	for ev := range events {
		if err := a.apply(ev); err != nil {
			return nil, err
		}
	}
	return a.response()
}

// AssembleSlice is a convenience for in-memory event slices (mainly tests).
func AssembleSlice(events []Event) (*Response, error) {
	ch := make(chan Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return Assemble(ch)
}

type assembler struct {
	resp     *Response
	blocks   map[int]ContentBlock
	order    []int
	inputs   map[int]*strings.Builder
	finished bool
}

func (a *assembler) apply(ev Event) error {
	switch e := ev.(type) {
	case MessageStartEvent:
		if e.Response != nil {
			a.resp = e.Response
		} else {
			a.resp = &Response{}
		}
	case ContentBlockStartEvent:
		if a.resp == nil {
			a.resp = &Response{}
		}
		a.startBlock(e.Index, e.Block)
	case ContentBlockDeltaEvent:
		if err := a.applyDelta(e.Index, e.Delta); err != nil {
			return err
		}
	case ContentBlockStopEvent:
		a.stopBlock(e.Index)
	case MessageDeltaEvent:
		if a.resp == nil {
			a.resp = &Response{}
		}
		if e.FinishReason != "" {
			a.resp.FinishReason = e.FinishReason
		}
		if e.Usage != nil {
			a.resp.Usage = *e.Usage
		}
	case MessageStopEvent:
		a.finished = true
	case ErrorEvent:
		if e.Err != nil {
			return e.Err
		}
		return errors.New("stream error")
	}
	return nil
}

func (a *assembler) startBlock(index int, block ContentBlock) {
	if _, exists := a.blocks[index]; !exists {
		a.order = append(a.order, index)
	}
	switch b := block.(type) {
	case *TextBlock:
		a.blocks[index] = &TextBlock{Text: b.Text, Extra: cloneExtra(b.Extra)}
	case *ToolUseBlock:
		a.blocks[index] = &ToolUseBlock{ID: b.ID, Name: b.Name, Extra: cloneExtra(b.Extra)}
		a.inputs[index] = &strings.Builder{}
	case *ThinkingBlock:
		a.blocks[index] = &ThinkingBlock{Text: b.Text, Signature: b.Signature, Extra: cloneExtra(b.Extra)}
	default:
		if b == nil {
			a.blocks[index] = &TextBlock{}
		} else {
			a.blocks[index] = b
		}
	}
}

func (a *assembler) applyDelta(index int, d Delta) error {
	block, ok := a.blocks[index]
	if !ok {
		return fmt.Errorf("delta for unknown block index %d", index)
	}
	switch d.Type {
	case DeltaText:
		if t, ok := block.(*TextBlock); ok {
			t.Text += d.Text
		} else if th, ok := block.(*ThinkingBlock); ok {
			th.Text += d.Text
		}
	case DeltaInputJSON:
		if tu, ok := block.(*ToolUseBlock); ok {
			if a.inputs[index] == nil {
				a.inputs[index] = &strings.Builder{}
			}
			a.inputs[index].WriteString(d.Partial)
			_ = tu
		}
	case DeltaThinking:
		if th, ok := block.(*ThinkingBlock); ok {
			th.Text += d.Text
		}
	case DeltaSignature:
		if th, ok := block.(*ThinkingBlock); ok {
			th.Signature += d.Signature
		}
	}
	return nil
}

func (a *assembler) stopBlock(index int) {
	block, ok := a.blocks[index]
	if !ok {
		return
	}
	if b, ok := block.(*ToolUseBlock); ok {
		if buf := a.inputs[index]; buf != nil && buf.Len() > 0 {
			b.Input = json.RawMessage(buf.String())
		} else if len(b.Input) == 0 {
			b.Input = json.RawMessage("{}")
		}
		delete(a.inputs, index)
	}
}

func (a *assembler) response() (*Response, error) {
	if !a.finished {
		return nil, errors.New("stream ended before message_stop")
	}
	if a.resp == nil {
		a.resp = &Response{}
	}
	indices := make([]int, len(a.order))
	copy(indices, a.order)
	sort.Ints(indices)
	for _, idx := range indices {
		if b := a.blocks[idx]; b != nil {
			a.resp.Content = append(a.resp.Content, b)
		}
	}
	return a.resp, nil
}

func cloneExtra(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

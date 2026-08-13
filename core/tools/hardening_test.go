package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
)

type hardeningTool struct {
	name   string
	schema json.RawMessage
	run    func(context.Context, Invocation) (Output, error)
}

func (t hardeningTool) Definition() models.Tool {
	schema := t.schema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","additionalProperties":false}`)
	}
	return models.NewTool(t.name, t.name+" test tool", schema)
}

func (t hardeningTool) Execute(ctx context.Context, in Invocation) (Output, error) {
	if t.run != nil {
		return t.run(ctx, in)
	}
	return Text("ok"), nil
}

func TestRejectedCallAdvancesCoordinatorFrontier(t *testing.T) {
	exec, err := New(Config{MaxParallel: 2}, hardeningTool{name: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := exec.Start(context.Background(), []models.ToolCall{
		{ID: "bad", Name: "missing", Arguments: json.RawMessage(`{}`)},
		{ID: "good", Name: "ok", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	done := make(chan struct{})
	go func() {
		for batch.Next() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("batch stalled behind a locally rejected call")
	}
	if err := batch.Err(); err != nil {
		t.Fatal(err)
	}
	results := batch.Results()
	if results[0].State != ResultFinished || results[1].State != ResultFinished {
		t.Fatalf("states = %v, %v", results[0].State, results[1].State)
	}
}

func TestFinalizeBudgetClassification(t *testing.T) {
	exec, err := New(Config{Output: OutputLimits{MaxBlocks: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exec.Finalize(Output{Content: []models.Content{models.Text("a"), models.Text("b")}})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("Finalize error = %v, want ErrOutputLimit", err)
	}
}

func TestCollectorReceiptBindsRetainedContent(t *testing.T) {
	exec, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, limits := exec.Limits()
	collector, err := NewTextCollector(limits, HeadTail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Write([]byte("original")); err != nil {
		t.Fatal(err)
	}
	out, err := collector.Output(json.RawMessage(`{"ok":true}`), false)
	if err != nil {
		t.Fatal(err)
	}
	out.Content[0].Text.Text = "mutated"
	if _, err := exec.Finalize(out); !errors.Is(err, ErrExecution) {
		t.Fatalf("Finalize error = %v, want ErrExecution", err)
	}
}

func TestPrefixLifecycle(t *testing.T) {
	exec, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, limits := exec.Limits()
	collector, err := NewTextCollector(limits, Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Output(json.RawMessage(`{}`), false); !errors.Is(err, ErrExecution) {
		t.Fatalf("Output before PreparePrefix = %v", err)
	}
}

func TestSchemaProfileHardening(t *testing.T) {
	tests := []struct {
		name   string
		schema string
	}{
		{"dot", `{"type":"object","properties":{"x":{"type":"string","pattern":"."}}}`},
		{"anchor", `{"type":"object","properties":{"x":{"type":"string","pattern":"^x$"}}}`},
		{"ref assertion sibling", `{"type":"object","properties":{"x":{"$ref":"#/$defs/x","type":"string"}},"$defs":{"x":{"type":"string"}}}`},
		{"numeric enum duplicate", `{"type":"object","properties":{"x":{"enum":[1,1.0]}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(Config{}, hardeningTool{name: "x", schema: json.RawMessage(test.schema)})
			if err == nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New error = %v", err)
			}
		})
	}

	valid := `{"type":"object","properties":{"x":{"type":"string","pattern":"(ab|[a-z])+"}},"additionalProperties":false}`
	if _, err := New(Config{}, hardeningTool{name: "x", schema: json.RawMessage(valid)}); err != nil {
		t.Fatalf("valid portable pattern: %v", err)
	}
}

func TestCollectorMetadataHasUnambiguousFooter(t *testing.T) {
	exec, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, limits := exec.Limits()
	collector, err := NewTextCollector(limits, HeadTail)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = collector.Write([]byte("body\n@serpe-tool-metadata-end:v1 bytes=00000002"))
	out, err := collector.Output(json.RawMessage(`{"b":1,"a":2}`), false)
	if err != nil {
		t.Fatal(err)
	}
	text := out.Content[0].Text.Text
	if !strings.HasSuffix(text, `@serpe-tool-metadata-end:v1 bytes=0000000d`) {
		t.Fatalf("unexpected footer: %q", text)
	}
}

func TestHeadTailCollectorChunkingInvariant(t *testing.T) {
	exec, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, limits := exec.Limits()
	payload := bytes.Repeat([]byte("α🙂z"), 24_000)
	for i := 997; i < len(payload); i += 4093 {
		payload[i] = 0xff
	}

	collect := func(pattern []int) Output {
		t.Helper()
		collector, err := NewTextCollector(limits, HeadTail)
		if err != nil {
			t.Fatal(err)
		}
		for offset, part := 0, 0; offset < len(payload); part++ {
			size := pattern[part%len(pattern)]
			end := offset + size
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := collector.Write(payload[offset:end]); err != nil {
				t.Fatal(err)
			}
			offset = end
		}
		out, err := collector.Output(json.RawMessage(`{"source":"test"}`), false)
		if err != nil {
			t.Fatal(err)
		}
		out, err = exec.Finalize(out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	oneWrite := collect([]int{len(payload)})
	fragmented := collect([]int{1, 2, 3, 5, 31, 32_767})
	if oneWrite.Stats != fragmented.Stats || oneWrite.IsError != fragmented.IsError ||
		oneWrite.Content[0].Text.Text != fragmented.Content[0].Text.Text {
		t.Fatalf("collector output changed with Write chunking:\none=%+v\nfragmented=%+v", oneWrite.Stats, fragmented.Stats)
	}
}

package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

type stub struct {
	name string
	fn   func(context.Context, tools.Invocation) (tools.Output, error)
}

func (s stub) Definition() models.Tool {
	return models.NewTool(s.name, s.name+" tool", json.RawMessage(`{"type":"object","properties":{}}`))
}

func (s stub) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	if s.fn == nil {
		return tools.Text("ok"), nil
	}
	return s.fn(ctx, in)
}

func mustExec(t *testing.T, ts ...tools.Tool) *tools.Executor {
	t.Helper()
	e, err := tools.New(tools.Config{}, ts...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func drain(t *testing.T, b *tools.Batch) []tools.BatchEvent {
	t.Helper()
	defer b.Close()
	var evs []tools.BatchEvent
	for b.Next() {
		evs = append(evs, b.Event())
	}
	return evs
}

func TestNewRejectsNilAndDuplicate(t *testing.T) {
	t.Parallel()
	if _, err := tools.New(tools.Config{}, nil); err == nil || !errors.Is(err, tools.ErrInvalidConfig) {
		t.Fatalf("nil: %v", err)
	}
	var typed *stub
	if _, err := tools.New(tools.Config{}, typed); err == nil || !errors.Is(err, tools.ErrInvalidConfig) {
		t.Fatalf("typed nil: %v", err)
	}
	if _, err := tools.New(tools.Config{}, stub{name: "a"}, stub{name: "a"}); err == nil {
		t.Fatal("duplicate")
	}
}

func TestDefinitionsDefensiveCopy(t *testing.T) {
	t.Parallel()
	e := mustExec(t, stub{name: "echo"})
	defs := e.Definitions()
	defs[0].Name = "mut"
	if e.Definitions()[0].Name != "echo" {
		t.Fatal("definition mutation leaked")
	}
}

func TestStartUnknownToolRecoverable(t *testing.T) {
	t.Parallel()
	e := mustExec(t, stub{name: "echo"})
	b, err := e.Start(context.Background(), []models.ToolCall{{
		ID: "1", Name: "missing", Arguments: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, b)
	if len(evs) != 2 || evs[0].Kind != tools.BatchStarted || evs[0].Executed || evs[1].Kind != tools.BatchFinished {
		t.Fatalf("events=%+v", evs)
	}
	if evs[1].Output == nil || !evs[1].Output.IsError {
		t.Fatal("expected recoverable error")
	}
}

func TestStartSchemaReject(t *testing.T) {
	t.Parallel()
	tool := stub{name: "echo"}
	e := mustExec(t, tool)
	// echo schema allows extra properties; use additionalProperties false via a custom tool
	strict := schemaTool{name: "strict", schema: `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`}
	e = mustExec(t, strict)
	b, err := e.Start(context.Background(), []models.ToolCall{{
		ID: "1", Name: "strict", Arguments: json.RawMessage(`{"n":"x"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, b)
	if len(evs) != 2 || evs[1].Output == nil || !evs[1].Output.IsError {
		t.Fatalf("events=%+v", evs)
	}
}

type schemaTool struct {
	name, schema string
}

func (s schemaTool) Definition() models.Tool {
	return models.NewTool(s.name, s.name+" tool", json.RawMessage(s.schema))
}

func (schemaTool) Execute(context.Context, tools.Invocation) (tools.Output, error) {
	return tools.Text("ok"), nil
}

func TestExecuteAndFinalize(t *testing.T) {
	t.Parallel()
	e := mustExec(t, stub{name: "echo", fn: func(_ context.Context, in tools.Invocation) (tools.Output, error) {
		return tools.Text("hi " + string(in.Arguments)), nil
	}})
	b, err := e.Start(context.Background(), []models.ToolCall{{
		ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, b)
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || !evs[0].Executed || evs[1].Output == nil || evs[1].Output.IsError {
		t.Fatalf("events=%+v", evs)
	}
	if evs[1].Output.Content[0].Text.Text != `hi {"x":1}` {
		t.Fatalf("text=%q", evs[1].Output.Content[0].Text.Text)
	}
	if evs[1].Output.Stats.SHA256 == "" || evs[1].Output.Stats.KeptBytes == 0 {
		t.Fatalf("stats=%+v", evs[1].Output.Stats)
	}
}

func TestFatalExecute(t *testing.T) {
	t.Parallel()
	e := mustExec(t, stub{name: "boom", fn: func(context.Context, tools.Invocation) (tools.Output, error) {
		return tools.Output{}, errors.New("nope")
	}})
	b, err := e.Start(context.Background(), []models.ToolCall{{
		ID: "1", Name: "boom", Arguments: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, b)
	if b.Err() == nil || !errors.Is(b.Err(), tools.ErrExecution) {
		t.Fatalf("err=%v", b.Err())
	}
	if len(evs) < 1 || evs[len(evs)-1].Kind != tools.BatchFailed {
		t.Fatalf("events=%+v", evs)
	}
}

func TestCloseBeforeNext(t *testing.T) {
	t.Parallel()
	started := int32(0)
	e := mustExec(t, stub{name: "echo", fn: func(context.Context, tools.Invocation) (tools.Output, error) {
		atomic.AddInt32(&started, 1)
		return tools.Text("x"), nil
	}})
	b, err := e.Start(context.Background(), []models.ToolCall{{
		ID: "1", Name: "echo", Arguments: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if b.Next() {
		t.Fatal("Next after Close-before-start")
	}
	if atomic.LoadInt32(&started) != 0 {
		t.Fatal("tool ran after Close-before-Next")
	}
	for _, r := range b.Results() {
		if r.State != tools.ResultSkipped {
			t.Fatalf("state=%v", r.State)
		}
	}
}

func TestInputLimitNoBatch(t *testing.T) {
	t.Parallel()
	e := mustExec(t, stub{name: "echo"})
	_, err := e.Start(context.Background(), nil)
	if err == nil || !errors.Is(err, tools.ErrInputLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestFinalizeTruncatesText(t *testing.T) {
	t.Parallel()
	e, err := tools.New(tools.Config{Output: tools.OutputLimits{MaxTextBytes: 512, MaxFramedBytes: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", 2000)
	out, err := e.Finalize(tools.Text(big))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Stats.Truncated || len(out.Content[0].Text.Text) >= 2000 {
		t.Fatalf("stats=%+v len=%d", out.Stats, len(out.Content[0].Text.Text))
	}
	if !strings.Contains(out.Content[0].Text.Text, "serpe-tool-truncated:v1") {
		t.Fatalf("missing marker: %s", out.Content[0].Text.Text)
	}
}

func TestCollectorPrefixBudget(t *testing.T) {
	t.Parallel()
	limits := tools.OutputLimits{MaxTextBytes: 512, MaxFramedBytes: 8192, MaxBlocks: 8, MaxImageBytes: 1024, MaxImageWidth: 8, MaxImageHeight: 8, MaxImagePixels: 64, MaxBatchFramedBytes: 8192, MaxMetadataBytes: 32}
	c, err := tools.NewTextCollector(limits, tools.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(strings.Repeat("x", 4000))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PreparePrefix(); err != nil {
		t.Fatal(err)
	}
	out, err := c.Output(json.RawMessage(`{"cursor":"n"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Content) != 1 {
		t.Fatalf("content=%d", len(out.Content))
	}
}

func TestParallelReads(t *testing.T) {
	t.Parallel()
	var live atomic.Int32
	var max atomic.Int32
	var mu sync.Mutex
	arrived := 0
	ready := make(chan struct{})
	run := func(ctx context.Context, _ tools.Invocation) (tools.Output, error) {
		n := live.Add(1)
		for {
			cur := max.Load()
			if n <= cur || max.CompareAndSwap(cur, n) {
				break
			}
		}
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(ready)
		}
		mu.Unlock()
		<-ready
		live.Add(-1)
		return tools.Text("ok"), nil
	}
	e, err := tools.New(tools.Config{MaxParallel: 2},
		gated{name: "r1", plan: []tools.Claim{{Resource: "file:a", Access: tools.AccessRead}}, fn: run},
		gated{name: "r2", plan: []tools.Claim{{Resource: "file:a", Access: tools.AccessRead}}, fn: run},
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Start(context.Background(), []models.ToolCall{
		{ID: "1", Name: "r1", Arguments: json.RawMessage(`{}`)},
		{ID: "2", Name: "r2", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, b)
	if max.Load() < 2 {
		t.Fatalf("max overlap=%d", max.Load())
	}
}

type gated struct {
	name string
	plan []tools.Claim
	fn   func(context.Context, tools.Invocation) (tools.Output, error)
}

func (g gated) Definition() models.Tool {
	return models.NewTool(g.name, g.name+" tool", json.RawMessage(`{"type":"object","properties":{}}`))
}

func (g gated) Plan(context.Context, tools.Invocation) (tools.Plan, error) {
	return tools.Plan{Claims: g.plan}, nil
}

func (g gated) Execute(ctx context.Context, in tools.Invocation) (tools.Output, error) {
	return g.fn(ctx, in)
}

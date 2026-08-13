package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/core/tools"
)

const benchmarkWait = time.Millisecond

type waitingTool struct {
	name  string
	wait  time.Duration
	after func()
}

func (t waitingTool) Definition() models.Tool {
	return models.NewTool(t.name, "Wait for a controlled I/O completion.", json.RawMessage(`{"type":"object","additionalProperties":false}`))
}

func (t waitingTool) Plan(context.Context, tools.Invocation) (tools.Plan, error) {
	return tools.Plan{}, nil
}

func (t waitingTool) Execute(ctx context.Context, _ tools.Invocation) (tools.Output, error) {
	if err := waitForBenchmark(ctx, t.wait); err != nil {
		return tools.Output{}, err
	}
	if t.after != nil {
		t.after()
	}
	return tools.Text("ok"), nil
}

type activatingTool struct {
	name string
	wait time.Duration
}

func (t activatingTool) Definition() models.Tool {
	return models.NewTool(t.name, "Resolve a resource before running.", json.RawMessage(`{"type":"object","additionalProperties":false}`))
}

func (t activatingTool) Plan(context.Context, tools.Invocation) (tools.Plan, error) {
	return tools.Plan{}, nil
}

func (t activatingTool) Activate(ctx context.Context, _ tools.Invocation) (tools.Activation, error) {
	if err := waitForBenchmark(ctx, t.wait); err != nil {
		return tools.Activation{}, err
	}
	return tools.Activation{
		Run: func(context.Context) (tools.Output, error) {
			return tools.Text("ok"), nil
		},
	}, nil
}

func (t activatingTool) Execute(context.Context, tools.Invocation) (tools.Output, error) {
	panic("Executor must use Activation.Run")
}

func waitForBenchmark(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func BenchmarkIndependentIOBatch(b *testing.B) {
	for _, tc := range []struct {
		name        string
		maxParallel int
	}{
		{name: "serial", maxParallel: 1},
		{name: "parallel", maxParallel: 4},
	} {
		b.Run(tc.name, func(b *testing.B) {
			registered := make([]tools.Tool, 4)
			calls := make([]models.ToolCall, len(registered))
			for i := range registered {
				name := fmt.Sprintf("wait_%d", i)
				registered[i] = waitingTool{name: name, wait: benchmarkWait}
				calls[i] = models.ToolCall{ID: fmt.Sprintf("call_%d", i), Name: name, Arguments: json.RawMessage(`{}`)}
			}
			exec, err := tools.New(tools.Config{MaxParallel: tc.maxParallel}, registered...)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				runBenchmarkBatch(b, exec, calls)
			}
		})
	}
}

func BenchmarkActivatorDiscoveryHeadOfLine(b *testing.B) {
	for _, delay := range []time.Duration{0, benchmarkWait} {
		name := "ready"
		if delay != 0 {
			name = "delayed_discovery"
		}
		b.Run(name, func(b *testing.B) {
			exec, err := tools.New(tools.Config{MaxParallel: 2},
				activatingTool{name: "resolve", wait: delay},
				waitingTool{name: "unrelated"},
			)
			if err != nil {
				b.Fatal(err)
			}
			calls := []models.ToolCall{
				{ID: "resolve", Name: "resolve", Arguments: json.RawMessage(`{}`)},
				{ID: "unrelated", Name: "unrelated", Arguments: json.RawMessage(`{}`)},
			}
			b.ReportAllocs()
			for b.Loop() {
				runBenchmarkBatch(b, exec, calls)
			}
		})
	}
}

func BenchmarkTextCollectorLargeStream(b *testing.B) {
	const sourceBytes = 32 << 20
	exec, err := tools.New(tools.Config{})
	if err != nil {
		b.Fatal(err)
	}
	_, limits := exec.Limits()
	chunk := make([]byte, 32<<10)
	for i := range chunk {
		chunk[i] = byte('a' + i%26)
	}
	b.SetBytes(sourceBytes)
	b.ReportAllocs()
	var retained int64
	for b.Loop() {
		collector, err := tools.NewTextCollector(limits, tools.HeadTail)
		if err != nil {
			b.Fatal(err)
		}
		for written := 0; written < sourceBytes; written += len(chunk) {
			if _, err := collector.Write(chunk); err != nil {
				b.Fatal(err)
			}
		}
		out, err := collector.Output(json.RawMessage(`{}`), false)
		if err != nil {
			b.Fatal(err)
		}
		out, err = exec.Finalize(out)
		if err != nil {
			b.Fatal(err)
		}
		retained = out.Stats.KeptBytes
	}
	b.ReportMetric(float64(retained), "retained-B/op")
}

func runBenchmarkBatch(b *testing.B, exec *tools.Executor, calls []models.ToolCall) {
	b.Helper()
	batch, err := exec.Start(context.Background(), calls)
	if err != nil {
		b.Fatal(err)
	}
	for batch.Next() {
	}
	if err := batch.Err(); err != nil {
		_ = batch.Close()
		b.Fatal(err)
	}
	for i, result := range batch.Results() {
		if result.State != tools.ResultFinished {
			_ = batch.Close()
			b.Fatalf("result %d state = %v", i, result.State)
		}
	}
	if err := batch.Close(); err != nil {
		b.Fatal(err)
	}
}

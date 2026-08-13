package tools

import (
	"context"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

func validToolName(name string) bool {
	if name == "" || len(name) > maxToolNameGrammar {
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

func validCallID(id string, maxBytes int64) error {
	if id == "" {
		return fmt.Errorf("call id is empty")
	}
	if !utf8.ValidString(id) {
		return fmt.Errorf("call id is not valid UTF-8")
	}
	if int64(len(id)) > maxBytes {
		return fmt.Errorf("call id exceeds %d bytes", maxBytes)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return fmt.Errorf("call id contains a control character")
		}
	}
	return nil
}

func sanitizeDiagnostic(s string, limit int) string {
	if !utf8.ValidString(s) {
		s = stringsToValid(s)
	}
	var b []rune
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		b = append(b, r)
		if len(string(b)) >= limit {
			break
		}
	}
	out := string(b)
	if len(out) > limit {
		out = out[:limit]
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
	}
	return out
}

func stringsToValid(s string) string {
	return string([]rune(string([]byte(s))))
}

func checkEnvelope(calls []models.ToolCall, in InputLimits) error {
	n := len(calls)
	if n == 0 {
		return wrapInput("batch is empty")
	}
	if n > in.MaxCalls {
		return wrapInput("batch has %d calls, limit %d", n, in.MaxCalls)
	}
	seen := make(map[string]struct{}, n)
	var args int64
	for i, call := range calls {
		if err := validCallID(call.ID, in.MaxCallIDBytes); err != nil {
			return wrapInput("call %d: %v", i, err)
		}
		if _, ok := seen[call.ID]; ok {
			return wrapInput("duplicate call id %q", call.ID)
		}
		seen[call.ID] = struct{}{}
		if int64(len(call.Name)) > in.MaxToolNameBytes {
			return wrapInput("call %d name exceeds %d bytes", i, in.MaxToolNameBytes)
		}
		if int64(len(call.Arguments)) > in.MaxArgumentsBytes {
			return wrapInput("call %d arguments exceed %d bytes", i, in.MaxArgumentsBytes)
		}
		next, ok := add64(args, int64(len(call.Arguments)))
		if !ok || next > in.MaxBatchArgumentBytes {
			return wrapInput("batch arguments exceed %d bytes", in.MaxBatchArgumentBytes)
		}
		args = next
	}
	return nil
}

func parseArguments(ctx context.Context, raw []byte) (jsonvalue.Value, error) {
	if err := ctx.Err(); err != nil {
		return jsonvalue.Value{}, err
	}
	return jsonvalue.ParseObject(raw, jsonvalue.Limits{
		MaxDepth:       maxJSONDepth,
		MaxNodes:       maxArgumentNodes,
		MaxNumberBytes: 128,
		MaxExponent:    1000,
		MaxScale:       1024,
	})
}

type startConfig struct {
	maxBatchFramed *int64
}

// StartOption is a sealed per-Start override.
type StartOption interface {
	apply(*startConfig) error
}

type maxBatchFramedOption int64

func (o maxBatchFramedOption) apply(cfg *startConfig) error {
	if cfg.maxBatchFramed != nil {
		return wrapInput("WithMaxBatchFramedBytes may be used at most once")
	}
	v := int64(o)
	if v <= 0 {
		return wrapInput("WithMaxBatchFramedBytes must be positive")
	}
	cfg.maxBatchFramed = &v
	return nil
}

// WithMaxBatchFramedBytes tightens this Start's batch framed ceiling.
func WithMaxBatchFramedBytes(n int64) StartOption {
	return maxBatchFramedOption(n)
}

func applyStartOptions(opts []StartOption, base OutputLimits, calls int) (OutputLimits, error) {
	var cfg startConfig
	for _, opt := range opts {
		if opt == nil {
			return OutputLimits{}, wrapInput("nil StartOption")
		}
		if err := opt.apply(&cfg); err != nil {
			return OutputLimits{}, err
		}
	}
	out := base
	if cfg.maxBatchFramed != nil {
		if *cfg.maxBatchFramed > base.MaxBatchFramedBytes {
			return OutputLimits{}, wrapInput("WithMaxBatchFramedBytes cannot raise the executor limit")
		}
		out.MaxBatchFramedBytes = *cfg.maxBatchFramed
	}
	need, ok := mul64(int64(calls), perCallErrorReserve)
	if !ok || out.MaxBatchFramedBytes < need {
		return OutputLimits{}, wrapInput("batch framed reserve %d is below %d calls × %d", out.MaxBatchFramedBytes, calls, perCallErrorReserve)
	}
	return out, nil
}

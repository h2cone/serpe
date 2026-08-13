package tools

import (
	"fmt"
	"runtime"
)

// Config constructs an Executor. Zero fields use the package defaults.
// Positive fields may only tighten the corresponding ceiling.
type Config struct {
	MaxParallel int
	Input       InputLimits
	Output      OutputLimits
}

// InputLimits bounds a Start envelope. Zero fields use package defaults.
type InputLimits struct {
	MaxCalls              int
	MaxCallIDBytes        int64
	MaxToolNameBytes      int64
	MaxArgumentsBytes     int64
	MaxBatchArgumentBytes int64
}

// OutputLimits bounds one finalized Output. Zero fields use package defaults.
type OutputLimits struct {
	MaxBlocks           int
	MaxTextBytes        int64
	MaxImageBytes       int64
	MaxImageWidth       int
	MaxImageHeight      int
	MaxImagePixels      int64
	MaxFramedBytes      int64
	MaxBatchFramedBytes int64
	MaxMetadataBytes    int64
}

const (
	maxRegisteredTools    = 128
	maxDescriptionBytes   = 4 << 10
	maxSchemaBytes        = 64 << 10
	maxDefinitionsBytes   = 4 << 20
	maxToolNameGrammar    = 64
	maxClaimsPerCall      = 64
	maxResourceBytes      = 4 << 10
	maxSchemaRawDepth     = 128
	maxSchemaRawNodes     = 16384
	maxSchemaGraphDepth   = 64
	maxSchemaGraphNodes   = 4096
	maxSchemaMembers      = 4096
	maxRegexBytes         = 1 << 10
	maxEvalSteps          = 1 << 20
	maxEvalScanBytes      = 64 << 20
	maxJSONDepth          = 128
	maxArgumentNodes      = 262144
	maxImageRecords       = 65536
	maxCollectorMetaBytes = 4 << 10
	maxCollectorMetaDepth = 64
	maxCollectorMetaNodes = 512
	minTextBytes          = 512
	minFramedBytes        = 1 << 10
	minArgumentsBytes     = 2
	maxParallelHard       = 32
	minParallelDerived    = 4
	defaultMaxCalls       = 128
	defaultMaxCallIDBytes = 1024
	defaultMaxNameBytes   = 1024
	defaultMaxArgBytes    = 16 << 20
	defaultMaxBatchArg    = 16 << 20
	defaultMaxBlocks      = 64
	defaultMaxTextBytes   = 64 << 10
	defaultMaxImageBytes  = 7 << 20
	defaultMaxImageWidth  = 8192
	defaultMaxImageHeight = 8192
	defaultMaxImagePixels = 40_000_000
	defaultMaxFramedBytes = 8 << 20
	defaultMaxBatchFramed = 8 << 20
	defaultMaxMetadata    = 127
	perCallErrorReserve   = 1 << 10
)

func normalizeConfig(cfg Config) (Config, error) {
	in, err := normalizeInput(cfg.Input)
	if err != nil {
		return Config{}, err
	}
	out, err := normalizeOutput(cfg.Output)
	if err != nil {
		return Config{}, err
	}
	parallel, err := normalizeParallel(cfg.MaxParallel)
	if err != nil {
		return Config{}, err
	}
	if out.MaxBatchFramedBytes < minFramedBytes {
		return Config{}, wrapConfig("MaxBatchFramedBytes must be at least %d", minFramedBytes)
	}
	return Config{MaxParallel: parallel, Input: in, Output: out}, nil
}

func normalizeInput(in InputLimits) (InputLimits, error) {
	set := func(value, def, ceiling int64, name string, min int64) (int64, error) {
		if value < 0 {
			return 0, wrapConfig("%s must not be negative", name)
		}
		if value == 0 {
			value = def
		}
		if value > ceiling {
			return 0, wrapConfig("%s exceeds package ceiling %d", name, ceiling)
		}
		if value < min {
			return 0, wrapConfig("%s must be at least %d", name, min)
		}
		return value, nil
	}
	var err error
	if in.MaxCalls < 0 {
		return InputLimits{}, wrapConfig("MaxCalls must not be negative")
	}
	if in.MaxCalls == 0 {
		in.MaxCalls = defaultMaxCalls
	}
	if in.MaxCalls > defaultMaxCalls {
		return InputLimits{}, wrapConfig("MaxCalls exceeds package ceiling %d", defaultMaxCalls)
	}
	if in.MaxCalls < 1 {
		return InputLimits{}, wrapConfig("MaxCalls must be at least 1")
	}
	if in.MaxCallIDBytes, err = set(in.MaxCallIDBytes, defaultMaxCallIDBytes, defaultMaxCallIDBytes, "MaxCallIDBytes", 1); err != nil {
		return InputLimits{}, err
	}
	if in.MaxToolNameBytes, err = set(in.MaxToolNameBytes, defaultMaxNameBytes, defaultMaxNameBytes, "MaxToolNameBytes", 1); err != nil {
		return InputLimits{}, err
	}
	if in.MaxArgumentsBytes, err = set(in.MaxArgumentsBytes, defaultMaxArgBytes, defaultMaxArgBytes, "MaxArgumentsBytes", minArgumentsBytes); err != nil {
		return InputLimits{}, err
	}
	if in.MaxBatchArgumentBytes, err = set(in.MaxBatchArgumentBytes, defaultMaxBatchArg, defaultMaxBatchArg, "MaxBatchArgumentBytes", int64(in.MaxCalls)*minArgumentsBytes); err != nil {
		return InputLimits{}, err
	}
	return in, nil
}

func normalizeOutput(out OutputLimits) (OutputLimits, error) {
	setI := func(value, def, ceiling, min int, name string) (int, error) {
		if value < 0 {
			return 0, wrapConfig("%s must not be negative", name)
		}
		if value == 0 {
			value = def
		}
		if value > ceiling {
			return 0, wrapConfig("%s exceeds package ceiling %d", name, ceiling)
		}
		if value < min {
			return 0, wrapConfig("%s must be at least %d", name, min)
		}
		return value, nil
	}
	set := func(value, def, ceiling, min int64, name string) (int64, error) {
		if value < 0 {
			return 0, wrapConfig("%s must not be negative", name)
		}
		if value == 0 {
			value = def
		}
		if value > ceiling {
			return 0, wrapConfig("%s exceeds package ceiling %d", name, ceiling)
		}
		if value < min {
			return 0, wrapConfig("%s must be at least %d", name, min)
		}
		return value, nil
	}
	var err error
	if out.MaxBlocks, err = setI(out.MaxBlocks, defaultMaxBlocks, defaultMaxBlocks, 1, "MaxBlocks"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxTextBytes, err = set(out.MaxTextBytes, defaultMaxTextBytes, defaultMaxTextBytes, minTextBytes, "MaxTextBytes"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxImageBytes, err = set(out.MaxImageBytes, defaultMaxImageBytes, defaultMaxImageBytes, 1, "MaxImageBytes"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxImageWidth, err = setI(out.MaxImageWidth, defaultMaxImageWidth, defaultMaxImageWidth, 1, "MaxImageWidth"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxImageHeight, err = setI(out.MaxImageHeight, defaultMaxImageHeight, defaultMaxImageHeight, 1, "MaxImageHeight"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxImagePixels, err = set(out.MaxImagePixels, defaultMaxImagePixels, defaultMaxImagePixels, 1, "MaxImagePixels"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxFramedBytes, err = set(out.MaxFramedBytes, defaultMaxFramedBytes, defaultMaxFramedBytes, minFramedBytes, "MaxFramedBytes"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxBatchFramedBytes, err = set(out.MaxBatchFramedBytes, defaultMaxBatchFramed, defaultMaxBatchFramed, minFramedBytes, "MaxBatchFramedBytes"); err != nil {
		return OutputLimits{}, err
	}
	if out.MaxMetadataBytes, err = set(out.MaxMetadataBytes, defaultMaxMetadata, defaultMaxMetadata, 1, "MaxMetadataBytes"); err != nil {
		return OutputLimits{}, err
	}
	return out, nil
}

func normalizeParallel(n int) (int, error) {
	if n < 0 {
		return 0, wrapConfig("MaxParallel must not be negative")
	}
	if n == 0 {
		n = runtime.GOMAXPROCS(0)
		if n < minParallelDerived {
			n = minParallelDerived
		}
		if n > maxParallelHard {
			n = maxParallelHard
		}
		return n, nil
	}
	if n > maxParallelHard {
		return 0, wrapConfig("MaxParallel exceeds package ceiling %d", maxParallelHard)
	}
	return n, nil
}

func allocateCallQuotas(batch OutputLimits, n int) ([]OutputLimits, error) {
	if n <= 0 {
		return nil, wrapInput("batch is empty")
	}
	need, ok := mul64(int64(n), perCallErrorReserve)
	if !ok {
		return nil, wrapInput("batch framed reserve overflow")
	}
	if batch.MaxBatchFramedBytes < need {
		return nil, wrapInput("MaxBatchFramedBytes %d is below %d-byte reserve for %d calls", batch.MaxBatchFramedBytes, perCallErrorReserve, n)
	}
	base := batch.MaxBatchFramedBytes / int64(n)
	rem := batch.MaxBatchFramedBytes % int64(n)
	out := make([]OutputLimits, n)
	for i := range out {
		limit := batch
		share := base
		if int64(i) < rem {
			share++
		}
		if share > batch.MaxFramedBytes {
			share = batch.MaxFramedBytes
		}
		limit.MaxFramedBytes = share
		out[i] = limit
	}
	return out, nil
}

func mul64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > 0 && b > 0 && a > (1<<63-1)/b {
		return 0, false
	}
	if a < 0 && b < 0 {
		if a == -1<<63 || b == -1<<63 {
			return 0, false
		}
	}
	return a * b, true
}

func add64(a, b int64) (int64, bool) {
	if b > 0 && a > (1<<63-1)-b {
		return 0, false
	}
	if b < 0 && a < (-1<<63)-b {
		return 0, false
	}
	return a + b, true
}

func formatLimit(v int64) string { return fmt.Sprintf("%d", v) }

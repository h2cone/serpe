package builtin

import (
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

// Config is the authorized workspace and process policy for a builtin Set.
type Config struct {
	WorkspaceRoots  []string
	BashPath        string
	BashTimeout     time.Duration
	BashEnvironment map[string]string
	Limits          Limits
}

// Limits are per-tool input/scan ceilings. Zero fields use package defaults;
// positive values may only tighten.
type Limits struct {
	MaxPathBytes     int64
	MaxReadScanBytes int64
	MaxWriteBytes    int64
	MaxEditBytes     int64
	MaxEditWorkBytes int64
	MaxBashCommand   int64
	MaxBashScanBytes int64
}

const (
	defaultMaxPathBytes   = 32 << 10
	defaultMaxReadScan    = 256 << 20
	defaultMaxWriteBytes  = 8 << 20
	defaultMaxEditBytes   = 16 << 20
	defaultMaxEditWork    = 256 << 20
	defaultMaxBashCommand = 64 << 10
	defaultMaxBashScan    = 256 << 20
	minBashTimeout        = time.Second
	maxBashTimeout        = 10 * time.Minute
	defaultBashTimeout    = 2 * time.Minute
)

func normalizeLimits(in Limits) (Limits, error) {
	set := func(v, def, ceil int64, name string) (int64, error) {
		if v < 0 {
			return 0, fmt.Errorf("builtin: %s must not be negative", name)
		}
		if v == 0 {
			v = def
		}
		if v > ceil {
			return 0, fmt.Errorf("builtin: %s exceeds package ceiling", name)
		}
		return v, nil
	}
	var err error
	if in.MaxPathBytes, err = set(in.MaxPathBytes, defaultMaxPathBytes, defaultMaxPathBytes, "MaxPathBytes"); err != nil {
		return Limits{}, err
	}
	if in.MaxReadScanBytes, err = set(in.MaxReadScanBytes, defaultMaxReadScan, defaultMaxReadScan, "MaxReadScanBytes"); err != nil {
		return Limits{}, err
	}
	if in.MaxWriteBytes, err = set(in.MaxWriteBytes, defaultMaxWriteBytes, defaultMaxWriteBytes, "MaxWriteBytes"); err != nil {
		return Limits{}, err
	}
	if in.MaxEditBytes, err = set(in.MaxEditBytes, defaultMaxEditBytes, defaultMaxEditBytes, "MaxEditBytes"); err != nil {
		return Limits{}, err
	}
	if in.MaxEditWorkBytes, err = set(in.MaxEditWorkBytes, defaultMaxEditWork, defaultMaxEditWork, "MaxEditWorkBytes"); err != nil {
		return Limits{}, err
	}
	if in.MaxBashCommand, err = set(in.MaxBashCommand, defaultMaxBashCommand, defaultMaxBashCommand, "MaxBashCommand"); err != nil {
		return Limits{}, err
	}
	if in.MaxBashScanBytes, err = set(in.MaxBashScanBytes, defaultMaxBashScan, defaultMaxBashScan, "MaxBashScanBytes"); err != nil {
		return Limits{}, err
	}
	return in, nil
}

func checkPathString(p string, max int64) error {
	if p == "" {
		return fmt.Errorf("path is required")
	}
	if !utf8.ValidString(p) {
		return fmt.Errorf("path is not valid UTF-8")
	}
	if int64(len(p)) > max {
		return fmt.Errorf("path exceeds %d bytes", max)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("path contains a control character")
		}
	}
	return nil
}

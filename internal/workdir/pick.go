package workdir

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
)

var (
	// ErrCanceled is returned when the user dismisses the folder dialog.
	ErrCanceled = errors.New("directory selection canceled")
	// ErrUnavailable is returned when this process cannot show a folder dialog.
	ErrUnavailable = errors.New("directory picker is unavailable")
	// ErrBusy is returned when another folder dialog is already open.
	ErrBusy = errors.New("directory picker is already open")
)

var pickMu sync.Mutex

// Pick opens a native folder dialog and returns an absolute, usable working
// directory. start is a hint for the initial location and may be empty.
func Pick(ctx context.Context, start string) (string, error) {
	return pickWith(ctx, start, pickNative)
}

func pickWith(ctx context.Context, start string, native func(context.Context, string) (string, error)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !pickMu.TryLock() {
		return "", ErrBusy
	}
	defer pickMu.Unlock()
	if native == nil {
		return "", ErrUnavailable
	}
	path, err := native(ctx, start)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(path)
	if err := Check(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

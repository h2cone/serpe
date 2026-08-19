package workdir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPickWithValidDirectory(t *testing.T) {
	dir := t.TempDir()
	path, err := pickWith(context.Background(), "", func(context.Context, string) (string, error) {
		return dir, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Clean(dir) {
		t.Fatalf("path=%q want %q", path, filepath.Clean(dir))
	}
}

func TestPickWithCanceled(t *testing.T) {
	path, err := pickWith(context.Background(), "", func(context.Context, string) (string, error) {
		return "", ErrCanceled
	})
	if !errors.Is(err, ErrCanceled) || path != "" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestPickWithRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := pickWith(context.Background(), "", func(context.Context, string) (string, error) {
		return file, nil
	})
	if err == nil {
		t.Fatal("expected a validation error")
	}
}

func TestPickWithBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := pickWith(context.Background(), "", func(context.Context, string) (string, error) {
			close(started)
			<-release
			return dir, nil
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("picker did not start")
	}
	_, err := pickWith(context.Background(), "", func(context.Context, string) (string, error) {
		t.Fatal("second picker should not run")
		return "", nil
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPickWithHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pickWith(ctx, "", func(context.Context, string) (string, error) {
		t.Fatal("native picker should not run")
		return "", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

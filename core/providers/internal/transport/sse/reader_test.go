package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReaderCRLFCommentsAndMultilineData(t *testing.T) {
	t.Parallel()
	input := ": ping\r\nid: 7\r\nretry: 1000\r\n\r\nevent: update\r\ndata: first\r\ndata:second\r\n\r\ndata: [DONE]\r\n\r\n"
	reader := NewReader(&oneByteReader{data: []byte(input)}, 1024)
	first, err := reader.Next()
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if first.Name != "update" || first.ID != "7" || first.Data == nil || string(first.Data) != "first\nsecond" {
		t.Fatalf("first event = %#v", first)
	}
	second, err := reader.Next()
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if second.Name != "" || second.ID != "7" || string(second.Data) != "[DONE]" {
		t.Fatalf("second event = %#v", second)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final Next error = %v", err)
	}
}

func TestReaderDispatchesUnterminatedData(t *testing.T) {
	t.Parallel()
	reader := NewReader(strings.NewReader("data: tail"), 64)
	event, err := reader.Next()
	if err != nil || string(event.Data) != "tail" {
		t.Fatalf("Next() = %#v, %v", event, err)
	}
}

func TestReaderLimitsEventAndRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	if _, err := NewReader(strings.NewReader("data: 123456\n\n"), 8).Next(); err == nil {
		t.Fatal("oversized event was accepted")
	}
	if _, err := NewReader(strings.NewReader("data: \xff\n\n"), 64).Next(); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

func TestReaderResetsLimitAfterNonDispatchingEvent(t *testing.T) {
	t.Parallel()
	input := "retry: 1234\n\ndata: okay\n\n"
	reader := NewReader(strings.NewReader(input), 13)
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(event.Data) != "okay" || event.Retry != "" {
		t.Fatalf("event = %#v", event)
	}
}

func TestReaderResetsLimitAfterCommentBlock(t *testing.T) {
	t.Parallel()
	input := ": heartbeat\n\ndata: okay\n\n"
	reader := NewReader(strings.NewReader(input), 13)
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(event.Data) != "okay" {
		t.Fatalf("event = %#v", event)
	}
}

func TestReaderCloseRunsHookBeforeBodyOnce(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("hook failed")
	var order []string
	body := &recordingReadCloser{
		Reader: strings.NewReader(""),
		close:  func() error { order = append(order, "body"); return errors.New("body failed") },
	}
	reader := NewReaderWithClose(body, 64, func() error {
		order = append(order, "hook")
		return wantErr
	})
	if err := reader.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close error = %v, want hook error", err)
	}
	if err := reader.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("second Close error = %v, want hook error", err)
	}
	if got := strings.Join(order, ","); got != "hook,body" {
		t.Fatalf("close order = %q", got)
	}
}

type oneByteReader struct {
	data []byte
}

type recordingReadCloser struct {
	io.Reader
	close func() error
}

func (r *recordingReadCloser) Close() error {
	return r.close()
}

func (r *oneByteReader) Read(target []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	target[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

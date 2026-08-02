// Package sse implements a bounded Server-Sent Events reader without Scanner's
// token-size ceiling.
package sse

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Event is one parsed SSE dispatch.
type Event struct {
	Name  string
	Data  []byte
	ID    string
	Retry string
}

// Reader parses SSE events synchronously in the caller's goroutine.
type Reader struct {
	reader    *bufio.Reader
	closer    io.Closer
	closeFn   func() error
	maxSize   int64
	lastID    string
	closeOnce sync.Once
	closeErr  error
}

// NewReader creates a bounded SSE reader. maxEventBytes includes field names,
// values, and line delimiters for one event.
func NewReader(reader io.Reader, maxEventBytes int64) *Reader {
	result := &Reader{reader: bufio.NewReaderSize(reader, 4096), maxSize: maxEventBytes}
	if closer, ok := reader.(io.Closer); ok {
		result.closer = closer
	}
	return result
}

// NewReaderWithClose creates a bounded SSE reader and runs closeFn before
// closing the underlying reader. This lets protocol-neutral transport cleanup
// stay at the stream boundary without adding a second close channel to every
// provider event source.
func NewReaderWithClose(reader io.Reader, maxEventBytes int64, closeFn func() error) *Reader {
	result := NewReader(reader, maxEventBytes)
	result.closeFn = closeFn
	return result
}

// Next reads the next dispatchable event. Comments and empty events are
// skipped. A final unterminated event is dispatched before io.EOF.
func (r *Reader) Next() (Event, error) {
	var event Event
	event.ID = r.lastID
	var data bytes.Buffer
	var size int64
	hasField := false
	hasData := false
	for {
		line, eof, err := r.readLine(&size)
		if err != nil {
			return Event{}, err
		}
		if len(line) == 0 {
			if !hasField {
				if eof {
					return Event{}, io.EOF
				}
				size = 0
				continue
			}
			r.lastID = event.ID
			if hasData {
				return dispatch(event, &data), nil
			}
			if eof {
				return Event{}, io.EOF
			}
			event = Event{ID: r.lastID}
			data.Reset()
			size = 0
			hasField = false
			continue
		}
		if !utf8.Valid(line) {
			return Event{}, fmt.Errorf("sse: invalid UTF-8")
		}
		if line[0] != ':' {
			field, value := splitField(line)
			switch field {
			case "event":
				event.Name = value
				hasField = true
			case "data":
				data.WriteString(value)
				data.WriteByte('\n')
				hasField = true
				hasData = true
			case "id":
				if !strings.ContainsRune(value, '\x00') {
					event.ID = value
				}
				hasField = true
			case "retry":
				if _, err := strconv.ParseUint(value, 10, 64); err == nil {
					event.Retry = value
				}
				hasField = true
			}
		}
		if eof {
			if hasField && hasData {
				r.lastID = event.ID
				return dispatch(event, &data), nil
			}
			if hasField {
				r.lastID = event.ID
			}
			return Event{}, io.EOF
		}
	}
}

func dispatch(event Event, data *bytes.Buffer) Event {
	payload := data.Bytes()
	event.Data = append([]byte(nil), payload[:len(payload)-1]...)
	return event
}

func (r *Reader) readLine(size *int64) ([]byte, bool, error) {
	var line []byte
	for {
		fragment, err := r.reader.ReadSlice('\n')
		*size += int64(len(fragment))
		if r.maxSize <= 0 || *size > r.maxSize {
			return nil, false, fmt.Errorf("sse: event exceeds %d bytes", r.maxSize)
		}
		line = append(line, fragment...)
		switch err {
		case nil:
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			return line, false, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			line = bytes.TrimSuffix(line, []byte{'\r'})
			return line, true, nil
		default:
			return nil, false, err
		}
	}
}

func splitField(line []byte) (string, string) {
	index := bytes.IndexByte(line, ':')
	if index < 0 {
		return string(line), ""
	}
	value := line[index+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(line[:index]), string(value)
}

// Close closes the underlying body when it implements io.Closer.
func (r *Reader) Close() error {
	r.closeOnce.Do(func() {
		if r.closeFn != nil {
			r.closeErr = r.closeFn()
		}
		if r.closer != nil {
			if err := r.closer.Close(); r.closeErr == nil {
				r.closeErr = err
			}
		}
	})
	return r.closeErr
}

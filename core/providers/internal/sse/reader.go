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
	reader  *bufio.Reader
	closer  io.Closer
	maxSize int64
	lastID  string
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
			if hasField {
				r.lastID = event.ID
				if !hasData {
					event = Event{ID: r.lastID}
					data.Reset()
					size = 0
					hasField = false
					hasData = false
					if eof {
						return Event{}, io.EOF
					}
					continue
				}
				payload := data.Bytes()
				if len(payload) > 0 {
					payload = payload[:len(payload)-1]
				}
				event.Data = append([]byte(nil), payload...)
				return event, nil
			}
			if eof {
				return Event{}, io.EOF
			}
			data.Reset()
			size = 0
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
				payload := data.Bytes()
				if len(payload) > 0 {
					payload = payload[:len(payload)-1]
				}
				event.Data = append([]byte(nil), payload...)
				r.lastID = event.ID
				return event, nil
			}
			if hasField {
				r.lastID = event.ID
			}
			return Event{}, io.EOF
		}
	}
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
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

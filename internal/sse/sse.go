// Package sse provides minimal Server-Sent Events reading and writing helpers
// shared by the codec layer (which parses upstream SSE and renders downstream
// SSE) and tests. It is a leaf package with no dependencies on the rest of ouro.
package sse

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Event is one parsed SSE frame.
type Event struct {
	Type string // from "event:" lines; "" when absent
	Data string // from "data:" lines joined by "\n"
}

// Reader parses an SSE byte stream into Events.
type Reader struct {
	r *bufio.Reader
}

// NewReader wraps r for SSE parsing.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

// Next returns the next event. It returns io.EOF when the stream is exhausted.
func (r *Reader) Next() (Event, error) {
	var ev Event
	var dataLines []string
	for {
		line, err := r.r.ReadString('\n')
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				// Flush a trailing event with no blank-line terminator.
				if ev.Type != "" || len(dataLines) > 0 {
					ev.Data = strings.Join(dataLines, "\n")
					return ev, nil
				}
			}
			return Event{}, err
		}
		// Trim a trailing CR (CRLF line endings).
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			// Blank line dispatches the accumulated event.
			if ev.Type == "" && len(dataLines) == 0 {
				continue
			}
			ev.Data = strings.Join(dataLines, "\n")
			return ev, nil
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		field, value := splitField(line)
		switch field {
		case "event":
			ev.Type = value
		case "data":
			dataLines = append(dataLines, value)
		default:
			// id, retry, unknown fields: ignored.
		}
	}
}

func splitField(line string) (field, value string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	field = line[:idx]
	value = line[idx+1:]
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	return field, value
}

// WriteEvent writes one SSE frame to w. If typ is empty, no "event:" line is
// written (a data-only frame). Data containing newlines is split into multiple
// "data:" lines per the SSE spec.
func WriteEvent(w io.Writer, typ, data string) error {
	var b strings.Builder
	if typ != "" {
		fmt.Fprintf(&b, "event: %s\n", typ)
	}
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteRaw writes pre-formatted bytes (already including terminators) and flushes.
func WriteRaw(w io.Writer, p []byte) error {
	_, err := w.Write(p)
	return err
}

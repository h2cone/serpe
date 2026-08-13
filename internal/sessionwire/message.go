// Package sessionwire owns the JSON projection shared by session admission
// and the HTTP detail API. ProviderState is deliberately excluded: session
// detail exposes only a message's role and portable content records.
package sessionwire

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/jsonvalue"
)

// EncodeMessageFragment returns the exact compact JSON fragment used for one
// message in session detail responses. It never appends a trailing newline.
func EncodeMessageFragment(message models.Message) ([]byte, error) {
	var buffer bytes.Buffer
	if err := WriteMessageFragment(&buffer, message); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// WriteMessageFragment writes the exact compact detail projection of message.
func WriteMessageFragment(writer io.Writer, message models.Message) error {
	if writer == nil {
		return fmt.Errorf("sessionwire: writer is required")
	}
	if err := message.Validate(); err != nil {
		return fmt.Errorf("sessionwire: invalid message: %w", err)
	}
	encoder := fragmentEncoder{writer: writer}
	encoder.raw(`{"role":`)
	encoder.string(string(message.Role))
	encoder.raw(`,"content":[`)
	for index := range message.Content {
		if index > 0 {
			encoder.byte(',')
		}
		encoder.content(message.Content[index])
	}
	encoder.raw(`]}`)
	return encoder.err
}

// MessageFragmentSize computes the exact byte length without materializing
// text escapes, base64 image data, or the complete JSON fragment.
func MessageFragmentSize(message models.Message) (int64, error) {
	counter := &countWriter{}
	if err := WriteMessageFragment(counter, message); err != nil {
		return 0, err
	}
	if counter.overflow {
		return 0, fmt.Errorf("sessionwire: message size overflow")
	}
	return counter.count, nil
}

// MessageSizeAccumulator maintains a monotonic, overflow-safe upper bound for
// a message being assembled incrementally. AddContent charges the exact
// standalone content encoding plus one array separator; callers may use Size
// to reject before appending to their own builders. FinalSize validates an
// assembled message with the authoritative encoder.
type MessageSizeAccumulator struct {
	size     int64
	contents int
	overflow bool
}

// NewMessageSizeAccumulator initializes the fixed role/content wrapper.
func NewMessageSizeAccumulator(role models.Role) (*MessageSizeAccumulator, error) {
	if role == "" || !utf8.ValidString(string(role)) {
		return nil, fmt.Errorf("sessionwire: invalid role")
	}
	roleBytes := escapedStringSize(string(role))
	fixed, ok := checkedAdd(int64(len(`{"role":,"content":[]}`)), roleBytes)
	if !ok {
		return nil, fmt.Errorf("sessionwire: message size overflow")
	}
	return &MessageSizeAccumulator{size: fixed}, nil
}

// AddContent adds an exact encoded content-block contribution.
func (a *MessageSizeAccumulator) AddContent(content models.Content) error {
	if a == nil || a.overflow {
		return fmt.Errorf("sessionwire: accumulator is unavailable")
	}
	counter := &countWriter{}
	encoder := fragmentEncoder{writer: counter}
	encoder.content(content)
	if encoder.err != nil {
		return encoder.err
	}
	addition := counter.count
	if a.contents > 0 {
		addition++
	}
	next, ok := checkedAdd(a.size, addition)
	if !ok {
		a.overflow = true
		return fmt.Errorf("sessionwire: message size overflow")
	}
	a.size = next
	a.contents++
	return nil
}

// Size returns the current exact size for the content already added.
func (a *MessageSizeAccumulator) Size() (int64, error) {
	if a == nil || a.overflow {
		return 0, fmt.Errorf("sessionwire: message size overflow")
	}
	return a.size, nil
}

// FinalSize runs the authoritative projection over the finished message.
func (a *MessageSizeAccumulator) FinalSize(message models.Message) (int64, error) {
	return MessageFragmentSize(message)
}

type fragmentEncoder struct {
	writer io.Writer
	err    error
}

func (e *fragmentEncoder) write(data []byte) {
	if e.err != nil || len(data) == 0 {
		return
	}
	for len(data) > 0 {
		n, err := e.writer.Write(data)
		if err != nil {
			e.err = err
			return
		}
		if n <= 0 || n > len(data) {
			e.err = io.ErrShortWrite
			return
		}
		data = data[n:]
	}
}

func (e *fragmentEncoder) raw(value string) { e.write([]byte(value)) }

func (e *fragmentEncoder) byte(value byte) {
	var data [1]byte
	data[0] = value
	e.write(data[:])
}

func (e *fragmentEncoder) string(value string) {
	e.byte('"')
	start := 0
	for index := 0; index < len(value); {
		char := value[index]
		if char < utf8.RuneSelf {
			if char >= 0x20 && char != '\\' && char != '"' {
				index++
				continue
			}
			e.write([]byte(value[start:index]))
			switch char {
			case '\\', '"':
				e.write([]byte{'\\', char})
			case '\b':
				e.raw(`\b`)
			case '\f':
				e.raw(`\f`)
			case '\n':
				e.raw(`\n`)
			case '\r':
				e.raw(`\r`)
			case '\t':
				e.raw(`\t`)
			default:
				const hexadecimal = "0123456789abcdef"
				e.write([]byte{'\\', 'u', '0', '0', hexadecimal[char>>4], hexadecimal[char&0x0f]})
			}
			index++
			start = index
			continue
		}
		runeValue, width := utf8.DecodeRuneInString(value[index:])
		if runeValue == utf8.RuneError && width == 1 {
			e.err = fmt.Errorf("sessionwire: string is not valid UTF-8")
			return
		}
		if runeValue == '\u2028' || runeValue == '\u2029' {
			e.write([]byte(value[start:index]))
			if runeValue == '\u2028' {
				e.raw(`\u2028`)
			} else {
				e.raw(`\u2029`)
			}
			index += width
			start = index
			continue
		}
		index += width
	}
	e.write([]byte(value[start:]))
	e.byte('"')
}

func (e *fragmentEncoder) field(name, value string) {
	e.byte(',')
	e.string(name)
	e.byte(':')
	e.string(value)
}

func (e *fragmentEncoder) content(content models.Content) {
	if e.err != nil {
		return
	}
	if err := content.Validate(); err != nil {
		e.err = fmt.Errorf("sessionwire: invalid content: %w", err)
		return
	}
	e.raw(`{"type":`)
	e.string(string(content.Kind))
	switch content.Kind {
	case models.ContentText:
		if content.Text.Text != "" {
			e.field("text", content.Text.Text)
		}
	case models.ContentReasoningSummary:
		if content.ReasoningSummary.Text != "" {
			e.field("text", content.ReasoningSummary.Text)
		}
	case models.ContentRefusal:
		if content.Refusal.Text != "" {
			e.field("text", content.Refusal.Text)
		}
	case models.ContentImage:
		if content.Image.URI != "" {
			e.field("uri", content.Image.URI)
		} else {
			if content.Image.MIMEType != "" {
				e.field("mime", content.Image.MIMEType)
			}
			if len(content.Image.Data) > 0 {
				e.raw(`,"data":"`)
				base64Writer := base64.NewEncoder(base64.StdEncoding, writerAdapter{encoder: e})
				_, err := base64Writer.Write(content.Image.Data)
				if closeErr := base64Writer.Close(); err == nil {
					err = closeErr
				}
				if err != nil && e.err == nil {
					e.err = err
				}
				e.byte('"')
			}
		}
		if content.Image.Detail != "" {
			e.field("detail", string(content.Image.Detail))
		}
	case models.ContentToolCall:
		if content.ToolCall.ID != "" {
			e.field("id", content.ToolCall.ID)
		}
		if content.ToolCall.Name != "" {
			e.field("name", content.ToolCall.Name)
		}
		if len(content.ToolCall.Arguments) > 0 {
			if !jsonvalue.IsObject(content.ToolCall.Arguments) {
				e.err = fmt.Errorf("sessionwire: tool arguments must be an object")
				return
			}
			e.raw(`,"arguments":`)
			e.write(content.ToolCall.Arguments)
		}
	case models.ContentToolResult:
		if content.ToolResult.Name != "" {
			e.field("name", content.ToolResult.Name)
		}
		if content.ToolResult.CallID != "" {
			e.field("call_id", content.ToolResult.CallID)
		}
		if content.ToolResult.IsError {
			e.raw(`,"is_error":true`)
		}
		if len(content.ToolResult.Content) > 0 {
			e.raw(`,"content":[`)
			for index := range content.ToolResult.Content {
				if index > 0 {
					e.byte(',')
				}
				e.content(content.ToolResult.Content[index])
			}
			e.byte(']')
		}
	default:
		e.err = fmt.Errorf("sessionwire: unsupported content kind %q", content.Kind)
		return
	}
	e.byte('}')
}

type writerAdapter struct{ encoder *fragmentEncoder }

func (writer writerAdapter) Write(data []byte) (int, error) {
	if writer.encoder.err != nil {
		return 0, writer.encoder.err
	}
	writer.encoder.write(data)
	if writer.encoder.err != nil {
		return 0, writer.encoder.err
	}
	return len(data), nil
}

type countWriter struct {
	count    int64
	overflow bool
}

func (writer *countWriter) Write(data []byte) (int, error) {
	if writer.overflow || int64(len(data)) > math.MaxInt64-writer.count {
		writer.overflow = true
		return 0, fmt.Errorf("sessionwire: message size overflow")
	}
	writer.count += int64(len(data))
	return len(data), nil
}

func escapedStringSize(value string) int64 {
	counter := &countWriter{}
	encoder := fragmentEncoder{writer: counter}
	encoder.string(value)
	if encoder.err != nil {
		return math.MaxInt64
	}
	return counter.count
}

func checkedAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, false
	}
	return left + right, true
}

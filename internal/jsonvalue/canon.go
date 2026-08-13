package jsonvalue

import (
	"bytes"
	"fmt"
	"sort"
)

// CanonicalValue encodes v with sorted object keys and original number
// lexemes. It is used for collector metadata, not as a general RFC 8785 form.
func CanonicalValue(v Value) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v Value) error {
	switch v.Kind {
	case KindNull:
		buf.WriteString("null")
	case KindBool:
		if v.Bool {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case KindNumber:
		if v.Number == "" {
			return fmt.Errorf("jsonvalue: empty number lexeme")
		}
		buf.WriteString(v.Number)
	case KindString:
		writeJSONString(buf, v.String)
	case KindArray:
		buf.WriteByte('[')
		for i, el := range v.Array {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, el); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case KindObject:
		type pair struct {
			key string
			val Value
		}
		pairs := make([]pair, len(v.Object))
		for i, m := range v.Object {
			pairs[i] = pair{m.Key, m.Value}
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
		buf.WriteByte('{')
		for i, m := range pairs {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, m.key)
			buf.WriteByte(':')
			if err := writeCanonical(buf, m.val); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("jsonvalue: unknown value kind")
	}
	return nil
}

func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			buf.WriteByte('\\')
			buf.WriteByte(c)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, c)
				continue
			}
			buf.WriteByte(c)
		}
	}
	buf.WriteByte('"')
}

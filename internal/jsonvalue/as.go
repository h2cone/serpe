package jsonvalue

import (
	"fmt"
	"strconv"
)

// As converts v to T. Supported types are string, bool, and int64 (from a
// JSON number lexeme with no fraction or exponent).
func (v Value) As[T any]() (T, error) {
	var out T
	switch p := any(&out).(type) {
	case *string:
		if v.Kind != KindString {
			return out, fmt.Errorf("jsonvalue: expected string")
		}
		*p = v.String
	case *bool:
		if v.Kind != KindBool {
			return out, fmt.Errorf("jsonvalue: expected bool")
		}
		*p = v.Bool
	case *int64:
		n, err := parseInt64Lexeme(v)
		if err != nil {
			return out, err
		}
		*p = n
	default:
		return out, fmt.Errorf("jsonvalue: unsupported As type")
	}
	return out, nil
}

// LookupAs looks up key and converts the member with As. present is false
// when the key is missing; a type mismatch still reports present.
func (v Value) LookupAs[T any](key string) (T, bool, error) {
	field, ok := v.Lookup(key)
	if !ok {
		var zero T
		return zero, false, nil
	}
	out, err := field.As[T]()
	return out, true, err
}

func parseInt64Lexeme(v Value) (int64, error) {
	if v.Kind != KindNumber || v.Number == "" {
		return 0, fmt.Errorf("jsonvalue: expected integer")
	}
	n, err := strconv.ParseInt(v.Number, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jsonvalue: expected integer")
	}
	return n, nil
}

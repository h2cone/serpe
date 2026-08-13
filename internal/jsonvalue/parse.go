package jsonvalue

import (
	"fmt"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Limits bounds a single strict parse. Zero fields use package defaults for
// that parse kind; callers that need a tighter ceiling must set them.
type Limits struct {
	MaxDepth       int
	MaxNodes       int
	MaxNumberBytes int
	MaxExponent    int
	MaxScale       int
}

// Kind identifies a JSON value in the strict semantic tree.
type Kind uint8

const (
	KindNull Kind = iota + 1
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Member is one object member after key unescaping.
type Member struct {
	Key   string
	Value Value
}

// Value is one node of the strict semantic tree. Numbers keep their original
// lexeme; strings are decoded UTF-8. Object members keep first-seen order.
type Value struct {
	Kind   Kind
	Bool   bool
	Number string
	String string
	Array  []Value
	Object []Member
}

const (
	defaultMaxDepth       = 128
	defaultMaxNodes       = 262144
	defaultMaxNumberBytes = 128
	defaultMaxExponent    = 1000
	defaultMaxScale       = 1024
)

// ObjectLimits is the default envelope used for tool arguments.
func ObjectLimits() Limits {
	return Limits{
		MaxDepth:       defaultMaxDepth,
		MaxNodes:       defaultMaxNodes,
		MaxNumberBytes: defaultMaxNumberBytes,
		MaxExponent:    defaultMaxExponent,
		MaxScale:       defaultMaxScale,
	}
}

// Parse decodes exactly one JSON value with the given limits. It rejects
// trailing values, duplicate object keys after escape decoding, illegal UTF-8,
// unpaired surrogates, and number lexemes that exceed the exponent or scale
// budgets. It does not coerce types.
func Parse(raw []byte, limits Limits) (Value, error) {
	limits = normalizeParseLimits(limits)
	p := parser{src: raw, limits: limits}
	p.skipSpace()
	if p.i >= len(p.src) {
		return Value{}, fmt.Errorf("jsonvalue: empty JSON value")
	}
	v, err := p.parseValue()
	if err != nil {
		return Value{}, err
	}
	p.skipSpace()
	if p.i < len(p.src) {
		return Value{}, fmt.Errorf("jsonvalue: trailing JSON value")
	}
	return v, nil
}

// ParseObject is Parse plus a requirement that the value is a JSON object.
func ParseObject(raw []byte, limits Limits) (Value, error) {
	v, err := Parse(raw, limits)
	if err != nil {
		return Value{}, err
	}
	if v.Kind != KindObject {
		return Value{}, fmt.Errorf("jsonvalue: JSON object required")
	}
	return v, nil
}

func normalizeParseLimits(limits Limits) Limits {
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaultMaxDepth
	}
	if limits.MaxNodes <= 0 {
		limits.MaxNodes = defaultMaxNodes
	}
	if limits.MaxNumberBytes <= 0 {
		limits.MaxNumberBytes = defaultMaxNumberBytes
	}
	if limits.MaxExponent <= 0 {
		limits.MaxExponent = defaultMaxExponent
	}
	if limits.MaxScale <= 0 {
		limits.MaxScale = defaultMaxScale
	}
	return limits
}

type parser struct {
	src    []byte
	i      int
	depth  int
	nodes  int
	limits Limits
}

func (p *parser) parseValue() (Value, error) {
	if err := p.addNode(); err != nil {
		return Value{}, err
	}
	if p.i >= len(p.src) {
		return Value{}, fmt.Errorf("jsonvalue: unexpected end of JSON")
	}
	switch p.src[p.i] {
	case 'n':
		return p.parseLiteral("null", Value{Kind: KindNull})
	case 't':
		return p.parseLiteral("true", Value{Kind: KindBool, Bool: true})
	case 'f':
		return p.parseLiteral("false", Value{Kind: KindBool, Bool: false})
	case '"':
		s, err := p.parseString()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindString, String: s}, nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		n, err := p.parseNumber()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindNumber, Number: n}, nil
	case '[':
		return p.parseArray()
	case '{':
		return p.parseObject()
	default:
		return Value{}, fmt.Errorf("jsonvalue: invalid JSON value")
	}
}

func (p *parser) addNode() error {
	if p.nodes >= p.limits.MaxNodes {
		return fmt.Errorf("jsonvalue: JSON node budget exceeded")
	}
	p.nodes++
	return nil
}

func (p *parser) enter() error {
	if p.depth >= p.limits.MaxDepth {
		return fmt.Errorf("jsonvalue: JSON depth budget exceeded")
	}
	p.depth++
	return nil
}

func (p *parser) leave() { p.depth-- }

func (p *parser) parseLiteral(lit string, v Value) (Value, error) {
	if p.i+len(lit) > len(p.src) || string(p.src[p.i:p.i+len(lit)]) != lit {
		return Value{}, fmt.Errorf("jsonvalue: invalid JSON literal")
	}
	p.i += len(lit)
	return v, nil
}

func (p *parser) parseArray() (Value, error) {
	if err := p.enter(); err != nil {
		return Value{}, err
	}
	defer p.leave()
	p.i++ // [
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == ']' {
		p.i++
		return Value{Kind: KindArray, Array: []Value{}}, nil
	}
	var elems []Value
	for {
		p.skipSpace()
		el, err := p.parseValue()
		if err != nil {
			return Value{}, err
		}
		elems = append(elems, el)
		p.skipSpace()
		if p.i >= len(p.src) {
			return Value{}, fmt.Errorf("jsonvalue: unterminated array")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return Value{Kind: KindArray, Array: elems}, nil
		default:
			return Value{}, fmt.Errorf("jsonvalue: invalid array")
		}
	}
}

func (p *parser) parseObject() (Value, error) {
	if err := p.enter(); err != nil {
		return Value{}, err
	}
	defer p.leave()
	p.i++ // {
	p.skipSpace()
	if p.i < len(p.src) && p.src[p.i] == '}' {
		p.i++
		return Value{Kind: KindObject, Object: []Member{}}, nil
	}
	seen := make(map[string]struct{})
	var members []Member
	for {
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != '"' {
			return Value{}, fmt.Errorf("jsonvalue: object key must be a string")
		}
		if err := p.addNode(); err != nil { // member name is a semantic node
			return Value{}, err
		}
		key, err := p.parseString()
		if err != nil {
			return Value{}, err
		}
		if _, dup := seen[key]; dup {
			return Value{}, fmt.Errorf("jsonvalue: duplicate object key")
		}
		seen[key] = struct{}{}
		p.skipSpace()
		if p.i >= len(p.src) || p.src[p.i] != ':' {
			return Value{}, fmt.Errorf("jsonvalue: expected ':' after object key")
		}
		p.i++
		p.skipSpace()
		val, err := p.parseValue()
		if err != nil {
			return Value{}, err
		}
		members = append(members, Member{Key: key, Value: val})
		p.skipSpace()
		if p.i >= len(p.src) {
			return Value{}, fmt.Errorf("jsonvalue: unterminated object")
		}
		switch p.src[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return Value{Kind: KindObject, Object: members}, nil
		default:
			return Value{}, fmt.Errorf("jsonvalue: invalid object")
		}
	}
}

func (p *parser) parseString() (string, error) {
	if p.i >= len(p.src) || p.src[p.i] != '"' {
		return "", fmt.Errorf("jsonvalue: expected string")
	}
	p.i++
	var buf []byte
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c == '"' {
			p.i++
			if buf == nil {
				return "", nil
			}
			return string(buf), nil
		}
		if c == '\\' {
			p.i++
			if p.i >= len(p.src) {
				return "", fmt.Errorf("jsonvalue: unterminated string escape")
			}
			esc, n, err := p.decodeEscape()
			if err != nil {
				return "", err
			}
			buf = append(buf, esc...)
			p.i += n
			continue
		}
		if c < 0x20 {
			return "", fmt.Errorf("jsonvalue: unescaped control character in string")
		}
		if c < 0x80 {
			buf = append(buf, c)
			p.i++
			continue
		}
		r, size := utf8.DecodeRune(p.src[p.i:])
		if r == utf8.RuneError && size == 1 {
			return "", fmt.Errorf("jsonvalue: invalid UTF-8 in string")
		}
		buf = append(buf, p.src[p.i:p.i+size]...)
		p.i += size
	}
	return "", fmt.Errorf("jsonvalue: unterminated string")
}

func (p *parser) decodeEscape() ([]byte, int, error) {
	switch p.src[p.i] {
	case '"', '\\', '/':
		return []byte{p.src[p.i]}, 1, nil
	case 'b':
		return []byte{'\b'}, 1, nil
	case 'f':
		return []byte{'\f'}, 1, nil
	case 'n':
		return []byte{'\n'}, 1, nil
	case 'r':
		return []byte{'\r'}, 1, nil
	case 't':
		return []byte{'\t'}, 1, nil
	case 'u':
		r, n, err := p.decodeUnicode()
		if err != nil {
			return nil, 0, err
		}
		var enc [utf8.UTFMax]byte
		size := utf8.EncodeRune(enc[:], r)
		return enc[:size], n, nil
	default:
		return nil, 0, fmt.Errorf("jsonvalue: invalid string escape")
	}
}

func (p *parser) decodeUnicode() (rune, int, error) {
	r, err := parseHex4(p.src, p.i+1)
	if err != nil {
		return 0, 0, err
	}
	if r >= 0xDC00 && r <= 0xDFFF {
		return 0, 0, fmt.Errorf("jsonvalue: unpaired UTF-16 surrogate")
	}
	if r >= 0xD800 && r <= 0xDBFF {
		if p.i+11 > len(p.src) || p.src[p.i+5] != '\\' || p.src[p.i+6] != 'u' {
			return 0, 0, fmt.Errorf("jsonvalue: unpaired UTF-16 surrogate")
		}
		low, err := parseHex4(p.src, p.i+7)
		if err != nil {
			return 0, 0, err
		}
		pair := utf16.DecodeRune(r, low)
		if pair == unicode.ReplacementChar {
			return 0, 0, fmt.Errorf("jsonvalue: unpaired UTF-16 surrogate")
		}
		return pair, 11, nil
	}
	return r, 5, nil
}

func parseHex4(src []byte, i int) (rune, error) {
	if i+4 > len(src) {
		return 0, fmt.Errorf("jsonvalue: invalid \\u escape")
	}
	var r rune
	for _, c := range src[i : i+4] {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			r |= rune(c - 'A' + 10)
		default:
			return 0, fmt.Errorf("jsonvalue: invalid \\u escape")
		}
	}
	return r, nil
}

func (p *parser) parseNumber() (string, error) {
	start := p.i
	if p.src[p.i] == '-' {
		p.i++
		if p.i >= len(p.src) || !isDigit(p.src[p.i]) {
			return "", fmt.Errorf("jsonvalue: invalid number")
		}
	}
	if p.i >= len(p.src) || !isDigit(p.src[p.i]) {
		return "", fmt.Errorf("jsonvalue: invalid number")
	}
	if p.src[p.i] == '0' {
		p.i++
		if p.i < len(p.src) && isDigit(p.src[p.i]) {
			return "", fmt.Errorf("jsonvalue: invalid number")
		}
	} else {
		for p.i < len(p.src) && isDigit(p.src[p.i]) {
			p.i++
		}
	}
	fracDigits := 0
	if p.i < len(p.src) && p.src[p.i] == '.' {
		p.i++
		if p.i >= len(p.src) || !isDigit(p.src[p.i]) {
			return "", fmt.Errorf("jsonvalue: invalid number")
		}
		for p.i < len(p.src) && isDigit(p.src[p.i]) {
			fracDigits++
			p.i++
		}
	}
	expValue := 0
	expSeen := false
	expNeg := false
	if p.i < len(p.src) && (p.src[p.i] == 'e' || p.src[p.i] == 'E') {
		expSeen = true
		p.i++
		if p.i < len(p.src) && (p.src[p.i] == '+' || p.src[p.i] == '-') {
			expNeg = p.src[p.i] == '-'
			p.i++
		}
		if p.i >= len(p.src) || !isDigit(p.src[p.i]) {
			return "", fmt.Errorf("jsonvalue: invalid number")
		}
		for p.i < len(p.src) && isDigit(p.src[p.i]) {
			d := int(p.src[p.i] - '0')
			if expValue > (1<<30)/10 {
				return "", fmt.Errorf("jsonvalue: number exponent budget exceeded")
			}
			expValue = expValue*10 + d
			p.i++
		}
	}
	lex := p.src[start:p.i]
	if len(lex) > p.limits.MaxNumberBytes {
		return "", fmt.Errorf("jsonvalue: number lexeme budget exceeded")
	}
	if expSeen {
		if expValue > p.limits.MaxExponent {
			return "", fmt.Errorf("jsonvalue: number exponent budget exceeded")
		}
	}
	signedExp := expValue
	if expNeg {
		signedExp = -expValue
	}
	scale := signedExp - fracDigits
	if scale > p.limits.MaxScale || scale < -p.limits.MaxScale {
		return "", fmt.Errorf("jsonvalue: number scale budget exceeded")
	}
	return string(lex), nil
}

func (p *parser) skipSpace() {
	for p.i < len(p.src) {
		switch p.src[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Lookup returns the first member with key, if present.
func (v Value) Lookup(key string) (Value, bool) {
	for i := range v.Object {
		if v.Object[i].Key == key {
			return v.Object[i].Value, true
		}
	}
	return Value{}, false
}

// Has reports whether an object member is present.
func (v Value) Has(key string) bool {
	_, ok := v.Lookup(key)
	return ok
}

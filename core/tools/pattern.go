package tools

import (
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const (
	maxRegexExpanded = 65_536
	maxRegexTotal    = 262_144
)

// portablePatternParser recognizes the deliberately small regex language
// shared by the local schema validator and provider conformance fixtures. It
// also computes a conservative pre-expansion instruction count before the
// RE2 compiler is invoked.
type portablePatternParser struct {
	s string
	i int
}

func validatePortablePattern(pattern string) (int, error) {
	if !utf8.ValidString(pattern) {
		return 0, fmt.Errorf("schema: pattern is not valid UTF-8")
	}
	p := portablePatternParser{s: pattern}
	cost, err := p.parseExpr(0)
	if err != nil {
		return 0, err
	}
	if p.i != len(pattern) {
		return 0, fmt.Errorf("schema: invalid pattern at byte %d", p.i)
	}
	if cost > maxRegexExpanded {
		return 0, fmt.Errorf("schema: pattern expanded instruction budget exceeded")
	}
	return cost, nil
}

func (p *portablePatternParser) parseExpr(stop byte) (int, error) {
	total := 1
	for {
		seq, err := p.parseSequence(stop)
		if err != nil {
			return 0, err
		}
		total, err = addRegexCost(total, seq)
		if err != nil {
			return 0, err
		}
		if p.i >= len(p.s) || p.s[p.i] != '|' {
			break
		}
		p.i++
		total, err = addRegexCost(total, 1)
		if err != nil {
			return 0, err
		}
	}
	if stop != 0 {
		if p.i >= len(p.s) || p.s[p.i] != stop {
			return 0, fmt.Errorf("schema: unterminated pattern group")
		}
		p.i++
	}
	return total, nil
}

func (p *portablePatternParser) parseSequence(stop byte) (int, error) {
	total := 1
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '|' || (stop != 0 && c == stop) {
			break
		}
		atom, err := p.parseAtom()
		if err != nil {
			return 0, err
		}
		atom, err = p.parseQuantifier(atom)
		if err != nil {
			return 0, err
		}
		total, err = addRegexCost(total, atom)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (p *portablePatternParser) parseAtom() (int, error) {
	if p.i >= len(p.s) {
		return 0, fmt.Errorf("schema: pattern atom is missing")
	}
	switch p.s[p.i] {
	case '(':
		p.i++
		if p.i < len(p.s) && p.s[p.i] == '?' {
			return 0, fmt.Errorf("schema: non-capturing, named, look-around, and inline-flag groups are not supported")
		}
		cost, err := p.parseExpr(')')
		if err != nil {
			return 0, err
		}
		return addRegexCost(cost, 1)
	case '[':
		return p.parseClass()
	case '\\':
		if err := p.parseEscapedPunctuation(); err != nil {
			return 0, err
		}
		return 1, nil
	case '.', '^', '$':
		return 0, fmt.Errorf("schema: pattern metacharacter %q is not supported", p.s[p.i])
	case ')':
		return 0, fmt.Errorf("schema: unmatched ')' in pattern")
	case '*', '+', '?', '{', '}':
		return 0, fmt.Errorf("schema: pattern quantifier has no atom")
	default:
		r, n := utf8.DecodeRuneInString(p.s[p.i:])
		if r == utf8.RuneError && n == 1 {
			return 0, fmt.Errorf("schema: pattern is not valid UTF-8")
		}
		if unicode.IsControl(r) {
			return 0, fmt.Errorf("schema: control characters are not supported in patterns")
		}
		p.i += n
		return 1, nil
	}
}

func (p *portablePatternParser) parseEscapedPunctuation() error {
	p.i++
	if p.i >= len(p.s) {
		return fmt.Errorf("schema: pattern ends with a backslash")
	}
	c := p.s[p.i]
	const punctuation = `\\.^$|?*+()[]{}-`
	if c >= utf8.RuneSelf || !containsByte(punctuation, c) {
		return fmt.Errorf("schema: only escaped ASCII regex punctuation is supported")
	}
	p.i++
	return nil
}

func (p *portablePatternParser) parseClass() (int, error) {
	p.i++
	if p.i >= len(p.s) {
		return 0, fmt.Errorf("schema: unterminated character class")
	}
	if p.s[p.i] == '^' {
		return 0, fmt.Errorf("schema: negated character classes are not supported")
	}
	items := 0
	for p.i < len(p.s) && p.s[p.i] != ']' {
		if p.s[p.i] == '[' {
			return 0, fmt.Errorf("schema: '[' must be escaped inside a character class")
		}
		if p.s[p.i] == '-' {
			return 0, fmt.Errorf("schema: '-' must form a range or be escaped")
		}
		if _, err := p.parseClassScalar(); err != nil {
			return 0, err
		}
		items++
		if p.i < len(p.s) && p.s[p.i] == '-' {
			p.i++
			if p.i >= len(p.s) || p.s[p.i] == ']' {
				return 0, fmt.Errorf("schema: character-class range is missing an endpoint")
			}
			if _, err := p.parseClassScalar(); err != nil {
				return 0, err
			}
			items++
		}
	}
	if p.i >= len(p.s) || p.s[p.i] != ']' {
		return 0, fmt.Errorf("schema: unterminated character class")
	}
	if items == 0 {
		return 0, fmt.Errorf("schema: empty character classes are not supported")
	}
	p.i++
	return items + 1, nil
}

func (p *portablePatternParser) parseClassScalar() (rune, error) {
	if p.s[p.i] == '\\' {
		start := p.i
		if err := p.parseEscapedPunctuation(); err != nil {
			return 0, err
		}
		return rune(p.s[start+1]), nil
	}
	r, n := utf8.DecodeRuneInString(p.s[p.i:])
	if r == utf8.RuneError && n == 1 {
		return 0, fmt.Errorf("schema: pattern is not valid UTF-8")
	}
	if unicode.IsControl(r) {
		return 0, fmt.Errorf("schema: control characters are not supported in patterns")
	}
	p.i += n
	return r, nil
}

func (p *portablePatternParser) parseQuantifier(atom int) (int, error) {
	if p.i >= len(p.s) {
		return atom, nil
	}
	switch p.s[p.i] {
	case '?', '*', '+':
		p.i++
		if p.i < len(p.s) && p.s[p.i] == '?' {
			return 0, fmt.Errorf("schema: lazy quantifiers are not supported")
		}
		return addRegexCost(atom, 1)
	case '{':
		p.i++
		min, ok := p.parseDecimal()
		if !ok {
			return 0, fmt.Errorf("schema: invalid counted repetition")
		}
		max := min
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			if n, present := p.parseDecimal(); present {
				max = n
			} else {
				return 0, fmt.Errorf("schema: counted repetition requires a finite upper bound")
			}
		}
		if p.i >= len(p.s) || p.s[p.i] != '}' {
			return 0, fmt.Errorf("schema: invalid counted repetition")
		}
		p.i++
		if min > 1000 || max > 1000 || min > max {
			return 0, fmt.Errorf("schema: counted repetition must satisfy 0<=m<=n<=1000")
		}
		if p.i < len(p.s) && p.s[p.i] == '?' {
			return 0, fmt.Errorf("schema: lazy quantifiers are not supported")
		}
		cost, err := mulRegexCost(atom, max)
		if err != nil {
			return 0, err
		}
		return addRegexCost(cost, 1)
	default:
		return atom, nil
	}
}

func (p *portablePatternParser) parseDecimal() (int, bool) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if start == p.i {
		return 0, false
	}
	n, err := strconv.Atoi(p.s[start:p.i])
	if err != nil {
		return maxRegexExpanded + 1, true
	}
	return n, true
}

func addRegexCost(a, b int) (int, error) {
	if a > maxRegexExpanded-b {
		return 0, fmt.Errorf("schema: pattern expanded instruction budget exceeded")
	}
	return a + b, nil
}

func mulRegexCost(a, b int) (int, error) {
	if a != 0 && b > maxRegexExpanded/a {
		return 0, fmt.Errorf("schema: pattern expanded instruction budget exceeded")
	}
	return a * b, nil
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

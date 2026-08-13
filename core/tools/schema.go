package tools

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/h2cone/serpe/internal/jsonvalue"
)

const draft202012 = "https://json-schema.org/draft/2020-12/schema"

type compiledSchema struct {
	acceptAll bool
	rejectAll bool
	regexCost int

	typ              string
	enum             []jsonvalue.Value
	constValue       *jsonvalue.Value
	properties       map[string]*compiledSchema
	required         []string
	additional       *compiledSchema
	additionalFalse  bool
	minProperties    *int
	maxProperties    *int
	items            *compiledSchema
	minItems         *int
	maxItems         *int
	minLength        *int
	maxLength        *int
	pattern          *regexp.Regexp
	minimum          *big.Rat
	maximum          *big.Rat
	exclusiveMinimum *big.Rat
	exclusiveMaximum *big.Rat
	multipleOf       *big.Rat
}

type schemaCompiler struct {
	raw      jsonvalue.Value
	nodes    map[string]*schemaNode
	compiled map[string]*compiledSchema
	members  int
	graph    int
	regex    int
}

type schemaNode struct {
	ptr    string
	value  jsonvalue.Value
	schema bool
}

func compileSchema(raw []byte) (*compiledSchema, error) {
	val, err := jsonvalue.Parse(raw, jsonvalue.Limits{
		MaxDepth:       maxSchemaRawDepth,
		MaxNodes:       maxSchemaRawNodes,
		MaxNumberBytes: 128,
		MaxExponent:    1000,
		MaxScale:       1024,
	})
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	c := &schemaCompiler{
		raw:      val,
		nodes:    make(map[string]*schemaNode),
		compiled: make(map[string]*compiledSchema),
	}
	if err := c.mark("#", val, true, 0); err != nil {
		return nil, err
	}
	if err := c.checkRoot(val); err != nil {
		return nil, err
	}
	compiled, err := c.compile("#", val, 0)
	if err != nil {
		return nil, err
	}
	compiled.regexCost = c.regex
	return compiled, nil
}

func (c *schemaCompiler) checkRoot(val jsonvalue.Value) error {
	if val.Kind != jsonvalue.KindObject {
		return fmt.Errorf("schema: root must be an object")
	}
	if s, ok := val.Lookup("$schema"); ok {
		if s.Kind != jsonvalue.KindString || s.String != draft202012 {
			return fmt.Errorf("schema: $schema must be %s", draft202012)
		}
	}
	typ, ok := val.Lookup("type")
	if !ok || typ.Kind != jsonvalue.KindString || typ.String != "object" {
		return fmt.Errorf("schema: root type must be the string %q", "object")
	}
	if val.Has("$ref") {
		return fmt.Errorf("schema: root must not contain $ref")
	}
	return nil
}

func (c *schemaCompiler) mark(ptr string, val jsonvalue.Value, schema bool, depth int) error {
	if depth > maxSchemaGraphDepth {
		return fmt.Errorf("schema: graph depth budget exceeded")
	}
	if c.graph >= maxSchemaGraphNodes {
		return fmt.Errorf("schema: graph node budget exceeded")
	}
	c.graph++
	c.nodes[ptr] = &schemaNode{ptr: ptr, value: val, schema: schema}
	if !schema || val.Kind != jsonvalue.KindObject {
		if schema && val.Kind == jsonvalue.KindBool {
			return nil
		}
		return nil
	}
	for _, m := range val.Object {
		if err := c.walkKeyword(ptr, m.Key, m.Value, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (c *schemaCompiler) walkKeyword(parent, key string, val jsonvalue.Value, depth int) error {
	switch key {
	case "$schema":
		if parent != "#" {
			return fmt.Errorf("schema: $schema is only allowed at the root")
		}
	case "$id", "$anchor", "$dynamicAnchor", "$dynamicRef", "$vocabulary":
		return fmt.Errorf("schema: keyword %q is not supported", key)
	case "$ref":
		if val.Kind != jsonvalue.KindString {
			return fmt.Errorf("schema: $ref must be a string")
		}
		if err := validateLocalRef(val.String); err != nil {
			return err
		}
	case "$defs", "properties":
		if val.Kind != jsonvalue.KindObject {
			return fmt.Errorf("schema: %s must be an object", key)
		}
		for _, m := range val.Object {
			c.members++
			if c.members > maxSchemaMembers {
				return fmt.Errorf("schema: member budget exceeded")
			}
			if err := c.mark(parent+"/"+escapePointer(key)+"/"+escapePointer(m.Key), m.Value, true, depth); err != nil {
				return err
			}
		}
	case "items", "additionalProperties", "contentSchema":
		if key == "additionalProperties" && val.Kind == jsonvalue.KindBool {
			return nil
		}
		if val.Kind != jsonvalue.KindObject && val.Kind != jsonvalue.KindBool {
			return fmt.Errorf("schema: %s must be a schema", key)
		}
		if err := c.mark(parent+"/"+escapePointer(key), val, true, depth); err != nil {
			return err
		}
	case "type", "enum", "const", "required", "minProperties", "maxProperties",
		"minItems", "maxItems", "minLength", "maxLength", "pattern",
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		"title", "description", "$comment", "default", "examples", "deprecated",
		"readOnly", "writeOnly", "format", "contentEncoding", "contentMediaType":
		// instance data or scalar keywords: not schema-valued
	default:
		return fmt.Errorf("schema: keyword %q is not supported", key)
	}
	return nil
}

func (c *schemaCompiler) compile(ptr string, val jsonvalue.Value, depth int) (*compiledSchema, error) {
	if depth > maxSchemaGraphDepth {
		return nil, fmt.Errorf("schema: graph depth budget exceeded")
	}
	if got, ok := c.compiled[ptr]; ok {
		if got == nil {
			return nil, fmt.Errorf("schema: cyclic $ref %q", ptr)
		}
		return got, nil
	}
	c.compiled[ptr] = nil
	out, err := c.compileBody(ptr, val, depth)
	if err != nil {
		return nil, err
	}
	c.compiled[ptr] = out
	return out, nil
}

func (c *schemaCompiler) compileBody(ptr string, val jsonvalue.Value, depth int) (*compiledSchema, error) {
	if val.Kind == jsonvalue.KindBool {
		if val.Bool {
			return &compiledSchema{acceptAll: true}, nil
		}
		return &compiledSchema{rejectAll: true}, nil
	}
	if val.Kind != jsonvalue.KindObject {
		return nil, fmt.Errorf("schema: expected a schema object at %s", ptr)
	}
	if ref, ok := val.Lookup("$ref"); ok {
		for _, member := range val.Object {
			if member.Key == "$ref" {
				continue
			}
			if annotationKeyword(member.Key) {
				if err := validateAnnotation(member.Key, member.Value); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("schema: $ref may only have annotation siblings")
		}
		target, err := c.resolveRef(ref.String)
		if err != nil {
			return nil, err
		}
		return c.compile(target.ptr, target.value, depth+1)
	}
	out := &compiledSchema{properties: map[string]*compiledSchema{}}
	for _, m := range val.Object {
		if err := c.applyKeyword(out, ptr, m.Key, m.Value, depth); err != nil {
			return nil, err
		}
	}
	if out.minProperties != nil && out.maxProperties != nil && *out.minProperties > *out.maxProperties {
		return nil, fmt.Errorf("schema: minProperties exceeds maxProperties")
	}
	if out.minItems != nil && out.maxItems != nil && *out.minItems > *out.maxItems {
		return nil, fmt.Errorf("schema: minItems exceeds maxItems")
	}
	if out.minLength != nil && out.maxLength != nil && *out.minLength > *out.maxLength {
		return nil, fmt.Errorf("schema: minLength exceeds maxLength")
	}
	if err := validateNumericBounds(out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateNumericBounds(schema *compiledSchema) error {
	type bound struct {
		value     *big.Rat
		exclusive bool
	}
	lowers := []bound{{schema.minimum, false}, {schema.exclusiveMinimum, true}}
	uppers := []bound{{schema.maximum, false}, {schema.exclusiveMaximum, true}}
	for _, lower := range lowers {
		if lower.value == nil {
			continue
		}
		for _, upper := range uppers {
			if upper.value == nil {
				continue
			}
			comparison := lower.value.Cmp(upper.value)
			if comparison > 0 || (comparison == 0 && (lower.exclusive || upper.exclusive)) {
				return fmt.Errorf("schema: numeric lower bound exceeds upper bound")
			}
		}
	}
	return nil
}

func (c *schemaCompiler) applyKeyword(out *compiledSchema, ptr, key string, val jsonvalue.Value, depth int) error {
	switch key {
	case "$schema":
		if val.Kind != jsonvalue.KindString || val.String != draft202012 {
			return fmt.Errorf("schema: $schema must be %s", draft202012)
		}
		return nil
	case "title", "description", "$comment", "format", "contentEncoding", "contentMediaType":
		if val.Kind != jsonvalue.KindString {
			return fmt.Errorf("schema: annotation %s must be a string", key)
		}
		return nil
	case "deprecated", "readOnly", "writeOnly":
		if val.Kind != jsonvalue.KindBool {
			return fmt.Errorf("schema: annotation %s must be a boolean", key)
		}
		return nil
	case "examples":
		if val.Kind != jsonvalue.KindArray {
			return fmt.Errorf("schema: examples must be an array")
		}
		return nil
	case "default", "contentSchema", "$defs":
		return nil
	case "type":
		if val.Kind != jsonvalue.KindString {
			return fmt.Errorf("schema: type must be a single string")
		}
		switch val.String {
		case "object", "array", "string", "number", "integer", "boolean", "null":
			out.typ = val.String
		default:
			return fmt.Errorf("schema: unknown type %q", val.String)
		}
	case "enum":
		if val.Kind != jsonvalue.KindArray || len(val.Array) == 0 || len(val.Array) > 128 {
			return fmt.Errorf("schema: enum must be 1–128 scalars")
		}
		seen := make(map[string]struct{}, len(val.Array))
		for i, item := range val.Array {
			if !isScalar(item) {
				return fmt.Errorf("schema: enum item %d is not a scalar", i)
			}
			key, err := scalarEqualityKey(item)
			if err != nil {
				return err
			}
			if _, ok := seen[key]; ok {
				return fmt.Errorf("schema: enum items must be unique")
			}
			seen[key] = struct{}{}
			out.enum = append(out.enum, item)
		}
	case "const":
		if !isScalar(val) {
			return fmt.Errorf("schema: const must be a scalar")
		}
		cp := val
		out.constValue = &cp
	case "properties":
		if val.Kind != jsonvalue.KindObject {
			return fmt.Errorf("schema: properties must be an object")
		}
		for _, m := range val.Object {
			child, err := c.compile(ptr+"/properties/"+escapePointer(m.Key), m.Value, depth+1)
			if err != nil {
				return err
			}
			out.properties[m.Key] = child
		}
	case "required":
		if val.Kind != jsonvalue.KindArray {
			return fmt.Errorf("schema: required must be an array")
		}
		seen := make(map[string]struct{}, len(val.Array))
		for i, item := range val.Array {
			c.members++
			if c.members > maxSchemaMembers {
				return fmt.Errorf("schema: member budget exceeded")
			}
			if item.Kind != jsonvalue.KindString {
				return fmt.Errorf("schema: required[%d] must be a string", i)
			}
			if _, ok := seen[item.String]; ok {
				return fmt.Errorf("schema: required items must be unique")
			}
			seen[item.String] = struct{}{}
			out.required = append(out.required, item.String)
		}
	case "additionalProperties":
		if val.Kind == jsonvalue.KindBool {
			out.additionalFalse = !val.Bool
			return nil
		}
		child, err := c.compile(ptr+"/additionalProperties", val, depth+1)
		if err != nil {
			return err
		}
		out.additional = child
	case "minProperties":
		n, err := requireNonNegInt(val, "minProperties")
		if err != nil {
			return err
		}
		out.minProperties = &n
	case "maxProperties":
		n, err := requireNonNegInt(val, "maxProperties")
		if err != nil {
			return err
		}
		out.maxProperties = &n
	case "items":
		if val.Kind == jsonvalue.KindArray {
			return fmt.Errorf("schema: tuple items are not supported")
		}
		child, err := c.compile(ptr+"/items", val, depth+1)
		if err != nil {
			return err
		}
		out.items = child
	case "minItems":
		n, err := requireNonNegInt(val, "minItems")
		if err != nil {
			return err
		}
		out.minItems = &n
	case "maxItems":
		n, err := requireNonNegInt(val, "maxItems")
		if err != nil {
			return err
		}
		out.maxItems = &n
	case "minLength":
		n, err := requireNonNegInt(val, "minLength")
		if err != nil {
			return err
		}
		out.minLength = &n
	case "maxLength":
		n, err := requireNonNegInt(val, "maxLength")
		if err != nil {
			return err
		}
		out.maxLength = &n
	case "pattern":
		if val.Kind != jsonvalue.KindString {
			return fmt.Errorf("schema: pattern must be a string")
		}
		if len(val.String) > maxRegexBytes {
			return fmt.Errorf("schema: pattern exceeds %d bytes", maxRegexBytes)
		}
		cost, err := validatePortablePattern(val.String)
		if err != nil {
			return err
		}
		if c.regex > maxRegexTotal-cost {
			return fmt.Errorf("schema: total pattern instruction budget exceeded")
		}
		c.regex += cost
		re, err := regexp.Compile(val.String)
		if err != nil {
			return fmt.Errorf("schema: pattern: %w", err)
		}
		out.pattern = re
	case "minimum":
		r, err := requireNumber(val, "minimum")
		if err != nil {
			return err
		}
		out.minimum = r
	case "maximum":
		r, err := requireNumber(val, "maximum")
		if err != nil {
			return err
		}
		out.maximum = r
	case "exclusiveMinimum":
		r, err := requireNumber(val, "exclusiveMinimum")
		if err != nil {
			return err
		}
		out.exclusiveMinimum = r
	case "exclusiveMaximum":
		r, err := requireNumber(val, "exclusiveMaximum")
		if err != nil {
			return err
		}
		out.exclusiveMaximum = r
	case "multipleOf":
		r, err := requireNumber(val, "multipleOf")
		if err != nil {
			return err
		}
		if r.Sign() <= 0 {
			return fmt.Errorf("schema: multipleOf must be greater than zero")
		}
		out.multipleOf = r
	default:
		return fmt.Errorf("schema: keyword %q is not supported", key)
	}
	return nil
}

func annotationKeyword(key string) bool {
	switch key {
	case "title", "description", "$comment", "default", "examples", "deprecated",
		"readOnly", "writeOnly", "format", "contentEncoding", "contentMediaType",
		"contentSchema":
		return true
	default:
		return false
	}
}

func validateAnnotation(key string, value jsonvalue.Value) error {
	switch key {
	case "title", "description", "$comment", "format", "contentEncoding", "contentMediaType":
		if value.Kind != jsonvalue.KindString {
			return fmt.Errorf("schema: annotation %s must be a string", key)
		}
	case "deprecated", "readOnly", "writeOnly":
		if value.Kind != jsonvalue.KindBool {
			return fmt.Errorf("schema: annotation %s must be a boolean", key)
		}
	case "examples":
		if value.Kind != jsonvalue.KindArray {
			return fmt.Errorf("schema: examples must be an array")
		}
	}
	return nil
}

func scalarEqualityKey(value jsonvalue.Value) (string, error) {
	if value.Kind == jsonvalue.KindNumber {
		r, err := ratFromLexeme(value.Number)
		if err != nil {
			return "", err
		}
		return "number:" + r.RatString(), nil
	}
	raw, err := jsonvalue.CanonicalValue(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%s", value.Kind, raw), nil
}

func (c *schemaCompiler) resolveRef(ref string) (*schemaNode, error) {
	if err := validateLocalRef(ref); err != nil {
		return nil, err
	}
	ptr := ref
	if ptr == "#" {
		ptr = "#"
	}
	node, ok := c.nodes[ptr]
	if !ok || !node.schema {
		return nil, fmt.Errorf("schema: $ref %q does not target a schema node", ref)
	}
	return node, nil
}

func validateLocalRef(ref string) error {
	if ref == "#" {
		return nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return fmt.Errorf("schema: $ref must be a same-document pointer")
	}
	for _, seg := range strings.Split(ref[2:], "/") {
		decoded, err := decodePointerSegment(seg)
		if err != nil {
			return err
		}
		_ = decoded
	}
	return nil
}

func decodePointerSegment(seg string) (string, error) {
	if strings.Contains(seg, "%") {
		return "", fmt.Errorf("schema: $ref must not use percent-encoding")
	}
	var b strings.Builder
	for i := 0; i < len(seg); i++ {
		if seg[i] != '~' {
			b.WriteByte(seg[i])
			continue
		}
		if i+1 >= len(seg) || (seg[i+1] != '0' && seg[i+1] != '1') {
			return "", fmt.Errorf("schema: invalid JSON pointer escape")
		}
		if seg[i+1] == '0' {
			b.WriteByte('~')
		} else {
			b.WriteByte('/')
		}
		i++
	}
	return b.String(), nil
}

func escapePointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

func isScalar(v jsonvalue.Value) bool {
	switch v.Kind {
	case jsonvalue.KindNull, jsonvalue.KindBool, jsonvalue.KindNumber, jsonvalue.KindString:
		return true
	default:
		return false
	}
}

func requireNonNegInt(v jsonvalue.Value, name string) (int, error) {
	if v.Kind != jsonvalue.KindNumber {
		return 0, fmt.Errorf("schema: %s must be a number", name)
	}
	r, err := ratFromLexeme(v.Number)
	if err != nil {
		return 0, fmt.Errorf("schema: %s: %w", name, err)
	}
	if !r.IsInt() || r.Sign() < 0 {
		return 0, fmt.Errorf("schema: %s must be a non-negative integer", name)
	}
	maxInt := new(big.Int).SetUint64(uint64(^uint(0) >> 1))
	if r.Num().Cmp(maxInt) > 0 {
		return 0, fmt.Errorf("schema: %s is too large", name)
	}
	return int(r.Num().Uint64()), nil
}

func requireNumber(v jsonvalue.Value, name string) (*big.Rat, error) {
	if v.Kind != jsonvalue.KindNumber {
		return nil, fmt.Errorf("schema: %s must be a number", name)
	}
	r, err := ratFromLexeme(v.Number)
	if err != nil {
		return nil, fmt.Errorf("schema: %s: %w", name, err)
	}
	return r, nil
}

func ratFromLexeme(lex string) (*big.Rat, error) {
	r := new(big.Rat)
	if _, ok := r.SetString(lex); !ok {
		return nil, fmt.Errorf("invalid number %q", lex)
	}
	return r, nil
}

type evalBudget struct {
	steps int64
	scan  int64
}

func (s *compiledSchema) validate(ctx context.Context, inst jsonvalue.Value, b *evalBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.step(); err != nil {
		return err
	}
	if s.acceptAll {
		return nil
	}
	if s.rejectAll {
		return fmt.Errorf("schema: value rejected")
	}
	if s.typ != "" {
		if err := checkType(s.typ, inst); err != nil {
			return err
		}
	}
	if s.constValue != nil {
		if !instanceEqual(*s.constValue, inst) {
			return fmt.Errorf("schema: const mismatch")
		}
	}
	if len(s.enum) > 0 {
		ok := false
		for _, item := range s.enum {
			if instanceEqual(item, inst) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("schema: value is not in enum")
		}
	}
	switch inst.Kind {
	case jsonvalue.KindObject:
		return s.validateObject(ctx, inst, b)
	case jsonvalue.KindArray:
		return s.validateArray(ctx, inst, b)
	case jsonvalue.KindString:
		return s.validateString(ctx, inst, b)
	case jsonvalue.KindNumber:
		return s.validateNumber(inst, b)
	default:
		return nil
	}
}

func (s *compiledSchema) validateObject(ctx context.Context, inst jsonvalue.Value, b *evalBudget) error {
	if s.minProperties != nil && len(inst.Object) < *s.minProperties {
		return fmt.Errorf("schema: too few properties")
	}
	if s.maxProperties != nil && len(inst.Object) > *s.maxProperties {
		return fmt.Errorf("schema: too many properties")
	}
	present := make(map[string]struct{}, len(inst.Object))
	for _, m := range inst.Object {
		present[m.Key] = struct{}{}
		if child, ok := s.properties[m.Key]; ok {
			if err := child.validate(ctx, m.Value, b); err != nil {
				return err
			}
			continue
		}
		if s.additionalFalse {
			return fmt.Errorf("schema: unexpected property")
		}
		if s.additional != nil {
			if err := s.additional.validate(ctx, m.Value, b); err != nil {
				return err
			}
		}
	}
	for _, req := range s.required {
		if _, ok := present[req]; !ok {
			return fmt.Errorf("schema: missing required property")
		}
	}
	return nil
}

func (s *compiledSchema) validateArray(ctx context.Context, inst jsonvalue.Value, b *evalBudget) error {
	if s.minItems != nil && len(inst.Array) < *s.minItems {
		return fmt.Errorf("schema: too few items")
	}
	if s.maxItems != nil && len(inst.Array) > *s.maxItems {
		return fmt.Errorf("schema: too many items")
	}
	if s.items != nil {
		for i := range inst.Array {
			if err := s.items.validate(ctx, inst.Array[i], b); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *compiledSchema) validateString(ctx context.Context, inst jsonvalue.Value, b *evalBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.addScan(int64(len(inst.String))); err != nil {
		return err
	}
	for off := 0; off < len(inst.String); off += 64 << 10 {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	n := utf8.RuneCountInString(inst.String)
	if s.minLength != nil && n < *s.minLength {
		return fmt.Errorf("schema: string is shorter than minLength")
	}
	if s.maxLength != nil && n > *s.maxLength {
		return fmt.Errorf("schema: string is longer than maxLength")
	}
	if s.pattern != nil && !s.pattern.MatchString(inst.String) {
		return fmt.Errorf("schema: string does not match pattern")
	}
	return nil
}

func (s *compiledSchema) validateNumber(inst jsonvalue.Value, b *evalBudget) error {
	r, err := ratFromLexeme(inst.Number)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if err := b.chargeLimbs(r); err != nil {
		return err
	}
	if s.minimum != nil && r.Cmp(s.minimum) < 0 {
		return fmt.Errorf("schema: number is below minimum")
	}
	if s.maximum != nil && r.Cmp(s.maximum) > 0 {
		return fmt.Errorf("schema: number is above maximum")
	}
	if s.exclusiveMinimum != nil && r.Cmp(s.exclusiveMinimum) <= 0 {
		return fmt.Errorf("schema: number is not above exclusiveMinimum")
	}
	if s.exclusiveMaximum != nil && r.Cmp(s.exclusiveMaximum) >= 0 {
		return fmt.Errorf("schema: number is not below exclusiveMaximum")
	}
	if s.multipleOf != nil {
		quo := new(big.Rat).Quo(r, s.multipleOf)
		if err := b.chargeLimbs(quo); err != nil {
			return err
		}
		if !quo.IsInt() {
			return fmt.Errorf("schema: number is not a multiple of multipleOf")
		}
	}
	return nil
}

func checkType(typ string, inst jsonvalue.Value) error {
	ok := false
	switch typ {
	case "object":
		ok = inst.Kind == jsonvalue.KindObject
	case "array":
		ok = inst.Kind == jsonvalue.KindArray
	case "string":
		ok = inst.Kind == jsonvalue.KindString
	case "number":
		ok = inst.Kind == jsonvalue.KindNumber
	case "integer":
		if inst.Kind == jsonvalue.KindNumber {
			r, err := ratFromLexeme(inst.Number)
			ok = err == nil && r.IsInt()
		}
	case "boolean":
		ok = inst.Kind == jsonvalue.KindBool
	case "null":
		ok = inst.Kind == jsonvalue.KindNull
	}
	if !ok {
		return fmt.Errorf("schema: type %s mismatch", typ)
	}
	return nil
}

func instanceEqual(a, b jsonvalue.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case jsonvalue.KindNull:
		return true
	case jsonvalue.KindBool:
		return a.Bool == b.Bool
	case jsonvalue.KindNumber:
		ra, errA := ratFromLexeme(a.Number)
		rb, errB := ratFromLexeme(b.Number)
		if errA != nil || errB != nil {
			return a.Number == b.Number
		}
		return ra.Cmp(rb) == 0
	case jsonvalue.KindString:
		return a.String == b.String
	default:
		return false
	}
}

func (b *evalBudget) step() error {
	if b.steps >= maxEvalSteps {
		return fmt.Errorf("schema evaluation budget exceeded")
	}
	b.steps++
	if b.steps > maxEvalSteps {
		return fmt.Errorf("schema evaluation budget exceeded")
	}
	if b.steps%4096 == 0 {
		return nil
	}
	return nil
}

func (b *evalBudget) addScan(n int64) error {
	next, ok := add64(b.scan, n)
	if !ok || next > maxEvalScanBytes {
		return fmt.Errorf("schema evaluation budget exceeded")
	}
	b.scan = next
	return nil
}

func (b *evalBudget) chargeLimbs(r *big.Rat) error {
	if r == nil {
		return nil
	}
	// The conformance accounting uses fixed 32-bit limbs on every platform.
	n := (r.Num().BitLen() + r.Denom().BitLen() + 31) / 32
	if n < 1 {
		n = 1
	}
	if int64(n) > int64(maxEvalSteps)-b.steps {
		return fmt.Errorf("schema evaluation budget exceeded")
	}
	b.steps += int64(n)
	return nil
}

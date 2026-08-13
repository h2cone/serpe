package tools

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/h2cone/serpe/core/models"
)

var executorSeq atomic.Uint64

// Executor is an immutable registry plus a shared resource coordinator.
type Executor struct {
	id       uint64
	tools    []registered
	byName   map[string]int
	input    InputLimits
	output   OutputLimits
	parallel int
	coord    *coordinator
}

type registered struct {
	tool      Tool
	def       models.Tool
	schema    *compiledSchema
	planner   Planner
	activator Activator
}

// New constructs an immutable Executor. Any failure returns no Executor.
func New(cfg Config, tools ...Tool) (*Executor, error) {
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if len(tools) > maxRegisteredTools {
		return nil, wrapConfig("at most %d tools may be registered", maxRegisteredTools)
	}
	exec := &Executor{
		id:       executorSeq.Add(1),
		byName:   make(map[string]int, len(tools)),
		input:    norm.Input,
		output:   norm.Output,
		parallel: norm.MaxParallel,
		coord:    newCoordinator(norm.MaxParallel),
	}
	var defBytes int64
	var regexCost int
	for i, tool := range tools {
		reg, n, err := registerOne(tool, i, norm.Input)
		if err != nil {
			return nil, err
		}
		next, ok := add64(defBytes, n)
		if !ok || next > maxDefinitionsBytes {
			return nil, wrapConfig("registered definitions exceed %d bytes", maxDefinitionsBytes)
		}
		defBytes = next
		if reg.schema.regexCost > maxRegexTotal-regexCost {
			return nil, wrapConfig("registered patterns exceed %d compiled instructions", maxRegexTotal)
		}
		regexCost += reg.schema.regexCost
		if _, exists := exec.byName[reg.def.Name]; exists {
			return nil, wrapConfig("duplicate tool name %q", reg.def.Name)
		}
		exec.byName[reg.def.Name] = len(exec.tools)
		exec.tools = append(exec.tools, reg)
	}
	return exec, nil
}

func registerOne(tool Tool, index int, in InputLimits) (registered, int64, error) {
	if tool == nil {
		return registered{}, 0, wrapConfig("tool %d is nil", index)
	}
	if isTypedNil(tool) {
		return registered{}, 0, wrapConfig("tool %d is a typed nil", index)
	}
	_, hasAct := tool.(Activator)
	_, hasPlan := tool.(Planner)
	if hasAct && !hasPlan {
		return registered{}, 0, wrapConfig("tool %d implements Activator without Planner", index)
	}
	var def models.Tool
	var panicErr error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				panicErr = wrapConfig("tool %d Definition panic: %v", index, rec)
			}
		}()
		def = tool.Definition()
	}()
	if panicErr != nil {
		return registered{}, 0, panicErr
	}
	if err := checkDefinitionBounds(def, in); err != nil {
		return registered{}, 0, wrapConfig("tool %d: %v", index, err)
	}
	def = def.Clone()
	if err := def.Validate(); err != nil {
		return registered{}, 0, wrapConfig("tool %d: %v", index, err)
	}
	if !validToolName(def.Name) {
		return registered{}, 0, wrapConfig("tool %d: name %q is not a portable identifier", index, def.Name)
	}
	if int64(len(def.Name)) > in.MaxToolNameBytes {
		return registered{}, 0, wrapConfig("tool %d: name exceeds MaxToolNameBytes", index)
	}
	if def.Description == "" {
		return registered{}, 0, wrapConfig("tool %d: description is required", index)
	}
	for _, r := range def.Description {
		if r == 0 {
			return registered{}, 0, wrapConfig("tool %d: description contains NUL", index)
		}
	}
	schema, err := compileSchema(def.Parameters)
	if err != nil {
		return registered{}, 0, wrapConfig("tool %d: %v", index, err)
	}
	reg := registered{tool: tool, def: def, schema: schema}
	if p, ok := tool.(Planner); ok {
		reg.planner = p
	}
	if a, ok := tool.(Activator); ok {
		reg.activator = a
	}
	n := int64(len(def.Description) + len(def.Parameters))
	return reg, n, nil
}

func checkDefinitionBounds(def models.Tool, _ InputLimits) error {
	if !utf8.ValidString(def.Name) || !utf8.ValidString(def.Description) {
		return fmt.Errorf("definition is not valid UTF-8")
	}
	if len(def.Description) > maxDescriptionBytes {
		return fmt.Errorf("description exceeds %d bytes", maxDescriptionBytes)
	}
	if len(def.Parameters) > maxSchemaBytes {
		return fmt.Errorf("schema exceeds %d bytes", maxSchemaBytes)
	}
	return nil
}

func isTypedNil(tool Tool) bool {
	if tool == nil {
		return true
	}
	v := reflect.ValueOf(tool)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// Definitions returns a defensive copy of the registered tool definitions.
func (e *Executor) Definitions() []models.Tool {
	if e == nil || len(e.tools) == 0 {
		return nil
	}
	out := make([]models.Tool, len(e.tools))
	for i := range e.tools {
		out[i] = e.tools[i].def.Clone()
	}
	return out
}

// Limits returns copies of the effective input and output limits.
func (e *Executor) Limits() (InputLimits, OutputLimits) {
	if e == nil {
		return InputLimits{}, OutputLimits{}
	}
	return e.input, e.output
}

func (e *Executor) lookup(name string) (registered, bool) {
	i, ok := e.byName[name]
	if !ok {
		return registered{}, false
	}
	return e.tools[i], true
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

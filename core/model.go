// Package core exposes process-wide logical model registration and lookup.
// Provider protocol adaptation remains in core/providers.
package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/h2cone/ouro/core/models"
)

var (
	// ErrModelNotFound is returned when a logical model name is unknown.
	ErrModelNotFound = errors.New("logical model not found")
	// ErrModelAlreadyRegistered is returned when registration would replace an
	// existing logical model implicitly.
	ErrModelAlreadyRegistered = errors.New("logical model already registered")
)

var logicalModels = struct {
	sync.RWMutex
	values map[string]models.Model
}{values: make(map[string]models.Model)}

// RegisterModel binds a logical name to a model. Registration never replaces
// an existing entry implicitly.
func RegisterModel(name string, model models.Model) error {
	if strings.TrimSpace(name) != name || name == "" {
		return fmt.Errorf("register model: logical name is empty or has surrounding whitespace")
	}
	if model == nil {
		return fmt.Errorf("register model %q: model is nil", name)
	}
	logicalModels.Lock()
	defer logicalModels.Unlock()
	if _, exists := logicalModels.values[name]; exists {
		return fmt.Errorf("%w: %s", ErrModelAlreadyRegistered, name)
	}
	logicalModels.values[name] = model
	return nil
}

// UnregisterModel removes and returns a logical model. It is intended for
// controlled shutdown and tests; callers already holding a model are unaffected.
func UnregisterModel(name string) (models.Model, bool) {
	logicalModels.Lock()
	defer logicalModels.Unlock()
	model, exists := logicalModels.values[name]
	if exists {
		delete(logicalModels.values, name)
	}
	return model, exists
}

// Model resolves a previously registered logical model name.
func Model(logicalName string) (models.Model, error) {
	logicalModels.RLock()
	model, exists := logicalModels.values[logicalName]
	logicalModels.RUnlock()
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrModelNotFound, logicalName)
	}
	return model, nil
}

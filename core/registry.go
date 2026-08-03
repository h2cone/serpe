// Package core exposes process-wide model alias registration and lookup.
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
	// ErrModelNotFound is returned when a model alias is unknown.
	ErrModelNotFound = errors.New("model alias not found")
	// ErrModelAlreadyRegistered is returned when registration would replace an
	// existing model alias implicitly.
	ErrModelAlreadyRegistered = errors.New("model alias already registered")
)

var modelsByAlias = struct {
	sync.RWMutex
	values map[string]models.Model
}{values: make(map[string]models.Model)}

// RegisterModel binds a model alias to an upstream model. Registration never replaces
// an existing entry implicitly.
func RegisterModel(modelAlias string, upstreamModel models.Model) error {
	if strings.TrimSpace(modelAlias) != modelAlias || modelAlias == "" {
		return fmt.Errorf("register model: model alias is empty or has surrounding whitespace")
	}
	if upstreamModel == nil {
		return fmt.Errorf("register model %q: upstream model is nil", modelAlias)
	}
	modelsByAlias.Lock()
	defer modelsByAlias.Unlock()
	if _, exists := modelsByAlias.values[modelAlias]; exists {
		return fmt.Errorf("%w: %s", ErrModelAlreadyRegistered, modelAlias)
	}
	modelsByAlias.values[modelAlias] = upstreamModel
	return nil
}

// UnregisterModel removes a model alias and returns its upstream model. It is
// intended for controlled shutdown and tests; callers already holding the
// upstream model are unaffected.
func UnregisterModel(modelAlias string) (models.Model, bool) {
	modelsByAlias.Lock()
	defer modelsByAlias.Unlock()
	upstreamModel, exists := modelsByAlias.values[modelAlias]
	delete(modelsByAlias.values, modelAlias)
	return upstreamModel, exists
}

// Model resolves the upstream model registered under modelAlias.
func Model(modelAlias string) (models.Model, error) {
	modelsByAlias.RLock()
	upstreamModel, exists := modelsByAlias.values[modelAlias]
	modelsByAlias.RUnlock()
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrModelNotFound, modelAlias)
	}
	return upstreamModel, nil
}

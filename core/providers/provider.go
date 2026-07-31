package providers

import "github.com/h2cone/ouro/core/models"

// Provider is an immutable protocol and transport binding. Model validates and
// binds a physical model ID without network access.
type Provider interface {
	Model(modelID string) (models.Model, error)
}

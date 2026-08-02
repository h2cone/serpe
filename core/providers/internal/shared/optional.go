package shared

import "github.com/h2cone/ouro/core/models"

// OptionalPointer and OptionalValue bridge canonical optionals and wire pointers.
func OptionalPointer[T any](value models.Optional[T]) *T {
	if !value.Set {
		return nil
	}
	return &value.Value
}

func OptionalValue[T any](value *T) models.Optional[T] {
	if value == nil {
		return models.None[T]()
	}
	return models.Some(*value)
}

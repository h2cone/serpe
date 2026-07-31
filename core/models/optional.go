package models

// Optional preserves the distinction between an omitted value and its zero
// value. The zero value of Optional is unset.
type Optional[T any] struct {
	Value T
	Set   bool
}

// Some returns an Optional containing value.
func Some[T any](value T) Optional[T] {
	return Optional[T]{Value: value, Set: true}
}

// None returns an unset Optional.
func None[T any]() Optional[T] {
	return Optional[T]{}
}

// Get returns the value and whether it is set.
func (o Optional[T]) Get() (T, bool) {
	return o.Value, o.Set
}

// Or returns the contained value or fallback when the optional is unset.
func (o Optional[T]) Or(fallback T) T {
	if o.Set {
		return o.Value
	}
	return fallback
}

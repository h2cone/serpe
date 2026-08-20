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

// FromPointer returns Some(*value) or None when value is nil.
func FromPointer[T any](value *T) Optional[T] {
	if value == nil {
		return None[T]()
	}
	return Some(*value)
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

// Pointer returns a pointer to the contained value, or nil if unset.
func (o Optional[T]) Pointer() *T {
	if !o.Set {
		return nil
	}
	return &o.Value
}

// Map applies f to the contained value when set.
func (o Optional[T]) Map[U any](f func(T) U) Optional[U] {
	if !o.Set {
		return None[U]()
	}
	return Some(f(o.Value))
}

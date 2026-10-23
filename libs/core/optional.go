package core

// Optional is a field of a partial update: Set reports whether the request
// carries the field, and Value is its new value (possibly the zero value,
// which clears it). It maps the presence of a request parameter, such as
// a field named in an update mask.
type Optional[T any] struct {
	// Value is the new value; meaningful only when Set.
	Value T
	// Set reports whether the field is updated.
	Set bool
}

// Some returns an Optional that sets v.
func Some[T any](v T) Optional[T] { return Optional[T]{Value: v, Set: true} }

// Apply assigns the value to *dst when o is set, and reports whether it
// did.
func (o Optional[T]) Apply(dst *T) bool {
	if o.Set {
		*dst = o.Value
	}
	return o.Set
}

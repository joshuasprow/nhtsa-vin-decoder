package contract

import (
	"bytes"
	"encoding/json"
)

// Null is a value that may be SQL NULL; the zero value is NULL. It keeps
// NULL and the empty string distinct, as the contract does, and marshals
// NULL as JSON null.
type Null[T any] struct {
	V     T
	Valid bool
}

// Value returns v as a non-NULL Null.
func Value[T any](v T) Null[T] { return Null[T]{V: v, Valid: true} }

// MarshalJSON writes null, or V as encoding/json writes it.
func (n Null[T]) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(n.V)
}

// UnmarshalJSON reads null as NULL, anything else into V.
func (n *Null[T]) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*n = Null[T]{}
		return nil
	}
	if err := json.Unmarshal(b, &n.V); err != nil {
		return err
	}
	n.Valid = true
	return nil
}

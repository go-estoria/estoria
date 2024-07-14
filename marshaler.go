package estoria

import "encoding/json"

// A Marshaler marshals and unmarshals data to/from bytes.
type Marshaler[T any, PT *T] interface {
	Marshal(src PT) ([]byte, error)
	Unmarshal(data []byte, dest PT) error
}

type JSONMarshaler[T any] struct{}

func (m JSONMarshaler[T]) Marshal(src *T) ([]byte, error) {
	return json.Marshal(src)
}

func (m JSONMarshaler[T]) Unmarshal(data []byte, dest *T) error {
	return json.Unmarshal(data, dest)
}

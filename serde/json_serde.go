package serde

import (
	"encoding/json"

	"github.com/go-estoria/estoria"
)

type JSONEntity[E estoria.Entity] struct{}

func (JSONEntity[E]) Marshal(entity E) ([]byte, error) {
	return json.Marshal(entity)
}

func (JSONEntity[E]) Unmarshal(data []byte, dest *E) error {
	return json.Unmarshal(data, dest)
}

type JSONEventData struct{}

func (s JSONEventData) Unmarshal(b []byte, d estoria.EntityEventData) error {
	return json.Unmarshal(b, d)
}

func (s JSONEventData) Marshal(d estoria.EntityEventData) ([]byte, error) {
	return json.Marshal(d)
}

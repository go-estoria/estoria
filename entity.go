package estoria

import (
	"context"
	"encoding/json"

	"github.com/go-estoria/estoria/typeid"
)

// An Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	EntityID() typeid.TypeID
	SetEntityID(id typeid.TypeID)
	EventTypes() []EntityEvent
	ApplyEvent(ctx context.Context, eventData EntityEvent) error
}

type EntityMarshaler[E Entity] interface {
	Marshal(entity E) ([]byte, error)
	Unmarshal(data []byte, dest *E) error
}

type JSONEntityMarshaler[E Entity] struct{}

func (JSONEntityMarshaler[E]) Marshal(entity E) ([]byte, error) {
	return json.Marshal(entity)
}

func (JSONEntityMarshaler[E]) Unmarshal(data []byte, dest *E) error {
	return json.Unmarshal(data, dest)
}

// EntityEvent is an event that can be applied to an entity to change its state.
type EntityEvent interface {
	EventType() string
	New() EntityEvent
}

type EntityEventMarshaler interface {
	Unmarshal(b []byte, d EntityEvent) error
	Marshal(d EntityEvent) ([]byte, error)
}

type JSONEntityEventMarshaler struct{}

func (s JSONEntityEventMarshaler) Unmarshal(b []byte, d EntityEvent) error {
	return json.Unmarshal(b, d)
}

func (s JSONEntityEventMarshaler) Marshal(d EntityEvent) ([]byte, error) {
	return json.Marshal(d)
}

// A DiffableEntity is an entity that can be diffed against another entity
// of the same type to produce a series of events that represent the state
// changes between the two.
type DiffableEntity interface {
	Entity
	Diff(newer DiffableEntity) ([]any, error)
}

// An EntityFactory is a function that creates a new instance of an entity.
type EntityFactory[E Entity] func() E

package estoria

import (
	"context"
	"encoding/json"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// An Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	// EntityID returns the entity's typed identifier.
	EntityID() typeid.UUID
}

// An EntityFactory is a function that creates a new instance of an entity of type E.
type EntityFactory[E Entity] func(id uuid.UUID) E

// EntityMarshaler is an interface for marshaling entities to and from a type T.
type EntityMarshaler[E Entity] interface {
	// MarshalEntity marshals an entity to a type T.
	MarshalEntity(entity E) ([]byte, error)
	// UnmarshalEntity unmarshals an entity from a type T.
	UnmarshalEntity(data []byte, dest *E) error
}

type JSONMarshaler[E Entity] struct{}

var _ EntityMarshaler[Entity] = JSONMarshaler[Entity]{}

func (m JSONMarshaler[E]) MarshalEntity(entity E) ([]byte, error) {
	b, err := json.Marshal(entity)
	GetLogger().Debug("marshaled entity", "entity", entity, "data", string(b))
	return b, err
}

func (m JSONMarshaler[E]) UnmarshalEntity(data []byte, dest *E) error {
	GetLogger().Debug("unmarshaling entity", "data", string(data))
	return json.Unmarshal(data, &dest)
}

// EntityEvent is an event that can be applied to an entity to change its state.
type EntityEvent[E Entity] interface {
	// EventType returns the type of event.
	EventType() string
	// New returns a new instance of the event.
	New() EntityEvent[E]
	// ApplyTo applies the event to an entity, returning the new entity state.
	ApplyTo(ctx context.Context, entity E) (E, error)
}

// EntityEventMarshaler is an interface for marshaling entity events to and from a type T.
type EntityEventMarshaler[E Entity] interface {
	// MarshalEntityEvent marshals an entity event to a type T.
	MarshalEntityEvent(event EntityEvent[E]) ([]byte, error)
	// UnmarshalEntityEvent unmarshals an entity event from a type T.
	UnmarshalEntityEvent(data []byte, dest EntityEvent[E]) error
}

type JSONEntityEventMarshaler[E Entity] struct{}

var _ EntityEventMarshaler[Entity] = JSONEntityEventMarshaler[Entity]{}

func (m JSONEntityEventMarshaler[E]) MarshalEntityEvent(event EntityEvent[E]) ([]byte, error) {
	return json.Marshal(event)
}

func (m JSONEntityEventMarshaler[E]) UnmarshalEntityEvent(data []byte, dest EntityEvent[E]) error {
	return json.Unmarshal(data, dest)
}

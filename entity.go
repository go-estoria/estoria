package estoria

import (
	"context"

	"github.com/go-estoria/estoria/typeid"
)

// An Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	EntityID() typeid.TypeID
	SetEntityID(id typeid.TypeID)
	EventTypes() []EntityEventData
	ApplyEvent(ctx context.Context, eventData EntityEventData) error
}

// EntityEventData is the data of an event.
type EntityEventData interface {
	EventType() string
	New() EntityEventData
}

// A DiffableEntity is aggregate data that can be diffed against another aggregate data
// of the same type to produce a series of events that represent the state changes between the two.
type DiffableEntity interface {
	Entity
	Diff(newer DiffableEntity) ([]any, error)
}

// An EntityFactory is a function that creates a new instance of an entity.
type EntityFactory[E Entity] func() E

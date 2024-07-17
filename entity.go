package estoria

import (
	"context"

	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// An Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	EntityID() typeid.UUID
	EventTypes() []EntityEvent
	ApplyEvent(ctx context.Context, eventData EntityEvent) error
}

// EntityEvent is an event that can be applied to an entity to change its state.
type EntityEvent interface {
	EventType() string
	New() EntityEvent
}

// A DiffableEntity is an entity that can be diffed against another entity
// of the same type to produce a series of events that represent the state
// changes between the two.
type DiffableEntity interface {
	Entity
	Diff(newer DiffableEntity) ([]any, error)
}

// An EntityFactory is a function that creates a new instance of an entity.
type EntityFactory[E Entity] func(id uuid.UUID) E

package estoria

import (
	"context"
)

// An Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	ApplyEvent(ctx context.Context, eventData EntityEventData) error
	EntityType() string
}

// A DiffableEntity is aggregate data that can be diffed against another aggregate data
// of the same type to produce a series of events that represent the state changes between the two.
type DiffableEntity interface {
	Entity
	Diff(newer DiffableEntity) ([]any, error)
}

// An EntityFactory is a function that creates a new instance of an entity.
type EntityFactory[E Entity] func() E

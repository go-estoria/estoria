package continuum

import "context"

// Entity is anything whose state can be constructed by applying a series of events.
type Entity interface {
	ApplyEvent(ctx context.Context, eventData EventData) error
}

// DiffableEntity is aggregate data that can be diffed against another aggregate data
// of the same type to produce a series of events that represent the state changes between the two.
type DiffableEntity interface {
	Entity
	Diff(newer DiffableEntity) ([]any, error)
}

type EntityFactory[E Entity] func() E

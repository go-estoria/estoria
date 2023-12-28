package continuum

import "context"

// An Entity is anything whose state can be represented by a series of events.
// Every entity must have an ID and a type name, and must be able to apply events
// to its state.
type Entity interface {
	AggregateID() Identifier
	AggregateType() string
	ApplyEvent(ctx context.Context, event EventData) error
}

// DiffableEntity is an entity that can be diffed against another entity to produce a
// series of events that represent the state changes between the two.
type DiffableEntity interface {
	Diff(newer Entity) ([]EventData, error)
}

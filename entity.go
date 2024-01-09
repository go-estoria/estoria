package continuum

import "context"

// An AggregateData is anything whose state can be represented by a series of events.
// Every entity must have an ID and a type name, and must be able to apply events
// to its state.
type AggregateData interface {
	ApplyEvent(ctx context.Context, event EventData) error
}

// DiffableAggregateData is an entity that can be diffed against another entity to produce a
// series of events that represent the state changes between the two.
type DiffableAggregateData interface {
	AggregateData
	Diff(newer DiffableAggregateData) ([]EventData, error)
}

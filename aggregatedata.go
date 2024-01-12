package continuum

import "context"

// AggregateData is anything whose state can be constructed by applying a series of events.
type AggregateData interface {
	ApplyEvent(ctx context.Context, event EventData) error
}

// DiffableAggregateData is aggregate data that can be diffed against another aggregate data
// of the same type to produce a series of events that represent the state changes between the two.
type DiffableAggregateData interface {
	AggregateData
	Diff(newer DiffableAggregateData) ([]EventData, error)
}

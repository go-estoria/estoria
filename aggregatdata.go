package continuum

import "context"

// An AggregateData is anything whose state can be represented by a series of events.
// Every entity must have an ID and a type name, and must be able to apply events
// to its state.
type AggregateData interface {
	ApplyEvent(ctx context.Context, event EventData) error
}

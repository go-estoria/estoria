package continuum

import "context"

// AggregateData is anything whose state can be constructed by applying a series of events.
type AggregateData interface {
	ApplyEvent(ctx context.Context, event EventData) error
}

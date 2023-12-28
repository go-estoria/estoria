package continuum

import "context"

// An AggregateStore can create, load, and save aggregates.
type AggregateStore[E Entity] interface {
	// Create creates a new aggregate with the given ID.
	Create(aggregateID Identifier) (*Aggregate[E], error)

	// Load loads an aggregate with the given ID.
	Load(ctx context.Context, aggregateID Identifier) (*Aggregate[E], error)

	// Save saves the given aggregate.
	Save(ctx context.Context, a *Aggregate[E]) error
}

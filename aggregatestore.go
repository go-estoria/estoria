package continuum

import "context"

// An AggregateStore can create, load, and save aggregates.
type AggregateStore[E Entity] interface {
	Create(aggregateID Identifier) (*Aggregate[E], error)
	Load(ctx context.Context, aggregateID Identifier) (*Aggregate[E], error)
	Save(ctx context.Context, a *Aggregate[E]) error
}

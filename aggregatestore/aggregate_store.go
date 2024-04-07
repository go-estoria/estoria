package aggregatestore

import (
	"context"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type AggregateStore[E estoria.Entity] interface {
	NewAggregate() (*estoria.Aggregate[E], error)
	Allow(prototypes ...estoria.EventData)
	Load(ctx context.Context, id typeid.AnyID) (*estoria.Aggregate[E], error)
	Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E]) error
	Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error
}

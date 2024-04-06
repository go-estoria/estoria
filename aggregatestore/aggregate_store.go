package aggregatestore

import (
	"context"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type AggregateStore[E estoria.Entity] interface {
	Load(ctx context.Context, id typeid.AnyID) (*estoria.Aggregate[E], error)
	Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E]) error
	Save(ctx context.Context, aggregate *estoria.Aggregate[E]) error
}

package aggregatestore

import (
	"context"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.UUID) (*estoria.Aggregate[E], error)
	PutAggregate(ctx context.Context, aggregate *estoria.Aggregate[E]) error
}

type CachedAggregateStore[E estoria.Entity] struct {
	inner Store[E]
	cache AggregateCache[E]
	log   *slog.Logger
}

func NewCachedAggregateStore[E estoria.Entity](
	inner Store[E],
	cacher AggregateCache[E],
) *CachedAggregateStore[E] {
	return &CachedAggregateStore[E]{
		inner: inner,
		cache: cacher,
		log:   slog.Default().WithGroup("cachedaggregatestore"),
	}
}

var _ Store[estoria.Entity] = (*CachedAggregateStore[estoria.Entity])(nil)

// NewAggregate creates a new aggregate.
func (s *CachedAggregateStore[E]) NewAggregate(id *typeid.UUID) (*estoria.Aggregate[E], error) {
	return s.inner.NewAggregate(id)
}

func (s *CachedAggregateStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*estoria.Aggregate[E], error) {
	aggregate, err := s.cache.GetAggregate(ctx, id)
	if err != nil {
		s.log.Warn("failed to read cache", "error", err)
		return s.inner.Load(ctx, id, opts)
	} else if aggregate == nil {
		s.log.Debug("aggregate not in cache", "aggregate_id", id)
		return s.inner.Load(ctx, id, opts)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *CachedAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E], opts HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *CachedAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts SaveOptions) error {
	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return err
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "error", err)
	}

	return nil
}

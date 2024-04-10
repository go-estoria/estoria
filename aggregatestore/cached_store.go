package aggregatestore

import (
	"context"
	"log/slog"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.AnyID) (*estoria.Aggregate[E], error)
	PutAggregate(ctx context.Context, aggregate *estoria.Aggregate[E]) error
}

type CachedAggregateStore[E estoria.Entity] struct {
	store AggregateStore[E]
	cache AggregateCache[E]
}

func NewCachedAggregateStore[E estoria.Entity](
	inner AggregateStore[E],
	cacher AggregateCache[E],
) *CachedAggregateStore[E] {
	return &CachedAggregateStore[E]{
		store: inner,
		cache: cacher,
	}
}

// Allow allows an event type to be used with the aggregate store.
func (s *CachedAggregateStore[E]) Allow(prototypes ...estoria.EventData) {
	s.store.Allow(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *CachedAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	return s.store.NewAggregate()
}

func (s *CachedAggregateStore[E]) Load(ctx context.Context, id typeid.AnyID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	aggregate, err := s.cache.GetAggregate(ctx, id)
	if err != nil {
		slog.Warn("failed to read cache", "error", err)
		return s.store.Load(ctx, id, opts)
	} else if aggregate == nil {
		slog.Debug("aggregate not in cache", "aggregate_id", id)
		return s.store.Load(ctx, id, opts)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *CachedAggregateStore[E]) Hydrate(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.HydrateAggregateOptions) error {
	return s.store.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *CachedAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E], opts estoria.SaveAggregateOptions) error {
	if err := s.store.Save(ctx, aggregate, opts); err != nil {
		return err
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		slog.Warn("failed to write cache", "error", err)
	}

	return nil
}

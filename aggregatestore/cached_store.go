package aggregatestore

import (
	"context"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
)

type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.TypeID) (*estoria.Aggregate[E], error)
	PutAggregate(ctx context.Context, aggregate *estoria.Aggregate[E]) error
}

type CachedAggregateStore[E estoria.Entity] struct {
	store estoria.AggregateStore[E]
	cache AggregateCache[E]
	log   *slog.Logger
}

func NewCachedAggregateStore[E estoria.Entity](
	inner estoria.AggregateStore[E],
	cacher AggregateCache[E],
) *CachedAggregateStore[E] {
	return &CachedAggregateStore[E]{
		store: inner,
		cache: cacher,
		log:   slog.Default().WithGroup("cachedaggregatestore"),
	}
}

var _ estoria.AggregateStore[estoria.Entity] = (*CachedAggregateStore[estoria.Entity])(nil)

// Allow allows an event type to be used with the aggregate store.
func (s *CachedAggregateStore[E]) AllowEvents(prototypes ...estoria.EntityEventData) {
	s.store.AllowEvents(prototypes...)
}

// NewAggregate creates a new aggregate.
func (s *CachedAggregateStore[E]) NewAggregate() (*estoria.Aggregate[E], error) {
	return s.store.NewAggregate()
}

func (s *CachedAggregateStore[E]) Load(ctx context.Context, id typeid.TypeID, opts estoria.LoadAggregateOptions) (*estoria.Aggregate[E], error) {
	aggregate, err := s.cache.GetAggregate(ctx, id)
	if err != nil {
		s.log.Warn("failed to read cache", "error", err)
		return s.store.Load(ctx, id, opts)
	} else if aggregate == nil {
		s.log.Debug("aggregate not in cache", "aggregate_id", id)
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
		s.log.Warn("failed to write cache", "error", err)
	}

	return nil
}

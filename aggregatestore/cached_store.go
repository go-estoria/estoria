package aggregatestore

import (
	"context"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// An AggregateCache is a cache for aggregates.
type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.UUID) (*Aggregate[E], error)
	PutAggregate(ctx context.Context, aggregate *Aggregate[E]) error
}

// CachedStore wraps an aggreate store with an AggregateCache to cache aggregates.
type CachedStore[E estoria.Entity] struct {
	inner Store[E]
	cache AggregateCache[E]
	log   estoria.Logger
}

// NewCachedStore creates a new CachedStore.
func NewCachedStore[E estoria.Entity](
	inner Store[E],
	cacher AggregateCache[E],
) *CachedStore[E] {
	return &CachedStore[E]{
		inner: inner,
		cache: cacher,
		log:   slog.Default().WithGroup("cachedaggregatestore"),
	}
}

var _ Store[estoria.Entity] = (*CachedStore[estoria.Entity])(nil)

// New creates a new Aggregate.
func (s *CachedStore[E]) New(id uuid.UUID) (*Aggregate[E], error) {
	return s.inner.New(id)
}

// Load loads an aggregate by ID.
func (s *CachedStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*Aggregate[E], error) {
	aggregate, err := s.cache.GetAggregate(ctx, id)
	switch {
	case err == nil && aggregate != nil:
		return aggregate, nil
	case err != nil:
		s.log.Warn("failed to read cache", "aggregate_id", id, "error", err)
	case aggregate == nil:
		s.log.Debug("aggregate not in cache", "aggregate_id", id)
	}

	aggregate, err = s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadAggregateError{AggregateID: id, Operation: "loading from inner aggregate store", Err: err}
	} else if aggregate == nil {
		return nil, LoadAggregateError{AggregateID: id, Err: ErrAggregateNotFound}
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", id, "error", err)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate.
func (s *CachedStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *CachedStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveAggregateError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "error", err)
	}

	return nil
}

package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/gofrs/uuid/v5"
)

// An AggregateCache is a cache for aggregates.
type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID uuid.UUID) (*Aggregate[E], error)
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
) (*CachedStore[E], error) {
	if inner == nil {
		return nil, errors.New("inner store is required")
	}

	return &CachedStore[E]{
		inner: inner,
		cache: cacher,
		log:   estoria.GetLogger().WithGroup("cachedstore"),
	}, nil
}

var _ Store[estoria.Entity] = (*CachedStore[estoria.Entity])(nil)

func (s *CachedStore[E]) New(id uuid.UUID) *Aggregate[E] {
	return s.inner.New(id)
}

// Load loads an aggregate by ID.
func (s *CachedStore[E]) Load(ctx context.Context, id uuid.UUID, opts LoadOptions) (*Aggregate[E], error) {
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
		return nil, LoadError{Operation: "loading from inner aggregate store", Err: err}
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
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "error", err)
	}

	return nil
}

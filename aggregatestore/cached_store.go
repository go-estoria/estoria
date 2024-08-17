package aggregatestore

import (
	"context"
	"log/slog"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.UUID) (*Aggregate[E], error)
	PutAggregate(ctx context.Context, aggregate *Aggregate[E]) error
}

type CachedStore[E estoria.Entity] struct {
	inner Store[E]
	cache AggregateCache[E]
	log   *slog.Logger
}

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

// NewAggregate creates a new aggregate.
func (s *CachedStore[E]) New(id uuid.UUID) (*Aggregate[E], error) {
	return s.inner.New(id)
}

func (s *CachedStore[E]) Load(ctx context.Context, id typeid.UUID, opts LoadOptions) (*Aggregate[E], error) {
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
func (s *CachedStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate.
func (s *CachedStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts SaveOptions) error {
	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return err
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "error", err)
	}

	return nil
}

package aggregatestore

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-estoria/estoria"
	"go.jetpack.io/typeid"
)

type Cacher[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.AnyID) (*estoria.Aggregate[E], error)
	PutAggregate(ctx context.Context, aggregate *estoria.Aggregate[E]) error
}

type CacheEvictionPolicy struct {
	// EvictionInterval is the interval at which the cache is checked for items to evict.
	// The default is 0, which means no periodic eviction.
	EvictionInterval time.Duration

	// MaxAge is the maximum age of an item in the cache before it is evicted.
	// A non-zero EvictionInterval is required for this to take effect.
	// The default is 0, which means no eviction based on age.
	MaxAge time.Duration

	// MaxIdle is the maximum time an item can be idle in the cache before it is evicted.
	// A non-zero EvictionInterval is required for this to take effect.
	// The default is 0, which means no eviction based on idle time.
	MaxIdle time.Duration

	// MaxSize is the maximum number of items in the cache before it starts evicting.
	// The default is 0, which means no eviction based on size.
	MaxSize int
}

type CachedAggregateStore[E estoria.Entity] struct {
	store  AggregateStore[E]
	cacher Cacher[E]
}

func NewCachedAggregateStore[E estoria.Entity](
	inner AggregateStore[E],
	cacher Cacher[E],
) *CachedAggregateStore[E] {
	return &CachedAggregateStore[E]{
		store:  inner,
		cacher: cacher,
	}
}

func (s *CachedAggregateStore[E]) Load(ctx context.Context, id typeid.AnyID) (*estoria.Aggregate[E], error) {
	aggregate, err := s.cacher.GetAggregate(ctx, id)
	if err != nil {
		slog.Warn("failed to read cache", "error", err)
		return s.store.Load(ctx, id)
	} else if aggregate == nil {
		slog.Debug("aggregate not in cache")
		return s.store.Load(ctx, id)
	}

	return aggregate, nil
}

// Save saves an aggregate.
func (s *CachedAggregateStore[E]) Save(ctx context.Context, aggregate *estoria.Aggregate[E]) error {
	if err := s.store.Save(ctx, aggregate); err != nil {
		return err
	}

	if err := s.cacher.PutAggregate(ctx, aggregate); err != nil {
		slog.Warn("failed to write cache", "error", err)
	}

	return nil
}

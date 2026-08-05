package aggregatestore

import (
	"context"
	"errors"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// An AggregateCache is a cache for aggregates.
//
// Aggregates are keyed by their typed ID rather than a bare UUID so that a cache backend
// shared across aggregate types produces distinct, self-describing keys.
type AggregateCache[E estoria.Entity] interface {
	GetAggregate(ctx context.Context, aggregateID typeid.ID) (*Aggregate[E], error)
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
	switch {
	case inner == nil:
		return nil, errors.New("inner store is required")
	case cacher == nil:
		return nil, errors.New("aggregate cache is required")
	}

	return &CachedStore[E]{
		inner: inner,
		cache: cacher,
		log:   estoria.GetLogger().WithGroup("cachedstore"),
	}, nil
}

var _ Store[estoria.Entity] = (*CachedStore[estoria.Entity])(nil)

// New creates a new aggregate with the given ID.
func (s *CachedStore[E]) New(id uuid.UUID) *Aggregate[E] {
	return s.inner.New(id)
}

// Load loads an aggregate, first checking the cache before deferring to the inner store.
// If the aggregate is loaded from the inner store, it is added to the cache.
// A load for a specific version bypasses the cache entirely, in both directions. The cache
// holds whatever version an aggregate was last saved or loaded at, which is not necessarily
// the requested one, and writing a deliberately-truncated aggregate back would then serve
// that stale version to subsequent full loads.
func (s *CachedStore[E]) Load(ctx context.Context, id uuid.UUID, opts *LoadOptions) (*Aggregate[E], error) {
	if opts != nil && opts.ToVersion > 0 {
		s.log.Debug("bypassing cache for versioned load", "aggregate_id", id, "to_version", opts.ToVersion)

		aggregate, err := s.inner.Load(ctx, id, opts)
		if err != nil {
			return nil, LoadError{Operation: "loading from inner aggregate store", Err: err}
		}

		return aggregate, nil
	}

	aggregateID := s.inner.New(id).ID()

	aggregate, err := s.cache.GetAggregate(ctx, aggregateID)
	switch {
	case err == nil && aggregate != nil:
		return aggregate, nil
	case err != nil:
		s.log.Warn("failed to read cache", "aggregate_id", aggregateID, "error", err)
	case aggregate == nil:
		s.log.Debug("aggregate not in cache", "aggregate_id", aggregateID)
	}

	aggregate, err = s.inner.Load(ctx, id, opts)
	if err != nil {
		return nil, LoadError{Operation: "loading from inner aggregate store", Err: err}
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregateID, "error", err)
	}

	return aggregate, nil
}

// Hydrate hydrates an aggregate by deferring to the inner store.
func (s *CachedStore[E]) Hydrate(ctx context.Context, aggregate *Aggregate[E], opts *HydrateOptions) error {
	return s.inner.Hydrate(ctx, aggregate, opts)
}

// Save saves an aggregate using the inner store, then updates the cache.
func (s *CachedStore[E]) Save(ctx context.Context, aggregate *Aggregate[E], opts *SaveOptions) error {
	if aggregate == nil {
		return SaveError{Err: ErrNilAggregate}
	}

	if err := s.inner.Save(ctx, aggregate, opts); err != nil {
		return SaveError{AggregateID: aggregate.ID(), Operation: "saving to inner aggregate store", Err: err}
	}

	// An aggregate saved with SkipApply still has events queued, so its entity and version
	// trail what was just persisted. Caching it would serve that trailing state to later
	// loads, so leave the previous entry alone and let the next load repopulate it.
	if len(aggregate.state.unappliedEvents) > 0 {
		s.log.Debug("skipping cache write for aggregate with unapplied events",
			"aggregate_id", aggregate.ID(),
			"unapplied_events", len(aggregate.state.unappliedEvents))
		return nil
	}

	if err := s.cache.PutAggregate(ctx, aggregate); err != nil {
		s.log.Warn("failed to write cache", "aggregate_id", aggregate.ID(), "error", err)
	}

	return nil
}
